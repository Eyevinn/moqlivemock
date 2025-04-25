package internal

import (
	"context"
	"time"
)

type ObjectWriter func(objectID uint64, data []byte) (n int, err error)

// MoQGroup represents a MoQ group, a series of MoQ Objects.
// It corresponds to a GoP of video frames, or the corresponding
// time interval of audio frames

type MoQGroup struct {
	id         uint32
	startTime  uint64
	endTime    uint64
	startNr    uint64
	endNr      uint64
	MoQObjects []MoQObject
}

type MoQObject []byte

// GenMoQGroup generates a MoQGroup for a given track and number.
// The MoQGroup is generated based on the track's sample duration and the
// constant (average) duration of all MoQGroups for this track.
func GenMoQGroup(track *ContentTrack, nr uint64, constantDurMS uint32) *MoQGroup {
	startNr, endNr := calcMoQGroup(track, nr, constantDurMS)
	startTime := startNr * uint64(track.sampleDur)
	endTime := endNr * uint64(track.sampleDur)
	mq := &MoQGroup{
		id:         uint32(nr),
		startTime:  startTime,
		endTime:    endTime,
		startNr:    startNr,
		endNr:      endNr,
		MoQObjects: make([]MoQObject, 0, endNr-startNr),
	}
	for i := startNr; i < endNr; i++ {
		chunk, err := track.GetCMAFChunk(i)
		if err != nil {
			return nil
		}
		mq.MoQObjects = append(mq.MoQObjects, chunk)
	}
	return mq
}

func calcMoQGroup(track *ContentTrack, nr uint64, constantDurMS uint32) (startNr, endNr uint64) {
	startTime := nr * uint64(constantDurMS) * uint64(track.timeScale) / 1000
	endTime := (nr + 1) * uint64(constantDurMS) * uint64(track.timeScale) / 1000
	startNr = startTime / uint64(track.sampleDur)
	if startTime%uint64(track.sampleDur) != 0 {
		startNr++
	}
	endNr = endTime / uint64(track.sampleDur)
	if endTime%uint64(track.sampleDur) != 0 {
		endNr++
	}
	return startNr, endNr
}

// CurrMoQGroupNr returns the current MoQGroup number/ID for a given time.
func CurrMoQGroupNr(track *ContentTrack, nowMS uint64, constantDurMS uint32) uint64 {
	return nowMS / uint64(constantDurMS)
}

// WriteMoQGroup write all MoQGroup objects to a MoQWriter.
// The MoQGroup is sent in the correct time order and at appropriate times if ongoing session.
// If the context is done, the function returns the error from the context.
func WriteMoQGroup(ctx context.Context, track *ContentTrack, moq *MoQGroup, cb ObjectWriter) error {
	factorMS := 1000 / float64(track.timeScale)
	for nr, moqObj := range moq.MoQObjects {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		now := time.Now().UnixMilli()
		objTimeMS := int64(float64(int64(moq.startTime)+int64(nr+1)*int64(track.sampleDur)) * factorMS)
		waitTime := objTimeMS - now
		if waitTime <= 0 {
			_, err := cb(uint64(nr), moqObj)
			if err != nil {
				return err
			}
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(waitTime) * time.Millisecond):
			_, err := cb(uint64(nr), moqObj)
			if err != nil {
				return err
			}
		}
	}
	return nil
}
