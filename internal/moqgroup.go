package internal

import (
	"context"
	"time"
)

// Location represents a MoQ object location as defined in draft-11.
// It consists of a Group ID and Object ID tuple, where objects are ordered
// first by Group, then by Object within the same group.
type Location struct {
	Group  uint64
	Object uint64
}

type ObjectWriter func(objectID uint64, data []byte) (n int, err error)

// MoQGroup represents a MoQ group, a series of MoQ Objects.
// It corresponds to a GoP of video frames, or the corresponding
// time interval of audio frames.
// startNr and endNr refers to sample numbers, relative a
// start at Epoch (1970-01-01T00:00:00Z).

type MoQGroup struct {
	id         uint32
	startTime  uint64
	endTime    uint64
	startNr    uint64
	endNr      uint64
	MoQObjects []MoQObject
}

type MoQObject []byte

// GenMoQGroup generates a MoQGroup for a given track and group number.
// The MoQGroup is generated based on the track's sample duration and the
// constant (average) duration of all MoQGroups for this track.
func GenMoQGroup(track *ContentTrack, groupNr uint64, sampleBatch int, constantDurMS uint32) *MoQGroup {
	startNr, endNr := calcMoQGroup(track, groupNr, constantDurMS)
	startTime := startNr * uint64(track.SampleDur)
	endTime := endNr * uint64(track.SampleDur)
	mq := &MoQGroup{
		id:         uint32(groupNr),
		startTime:  startTime,
		endTime:    endTime,
		startNr:    startNr,
		endNr:      endNr,
		MoQObjects: make([]MoQObject, 0, endNr-startNr),
	}
	for i := startNr; i < endNr; i += uint64(sampleBatch) {
		firstSample := i
		endSample := min(i+uint64(sampleBatch), endNr)
		chunk, err := track.GenCMAFChunk(uint32(groupNr), firstSample, endSample)
		if err != nil {
			return nil
		}
		mq.MoQObjects = append(mq.MoQObjects, chunk)
	}
	return mq
}

func calcMoQGroup(track *ContentTrack, nr uint64, constantDurMS uint32) (startNr, endNr uint64) {
	startTime := nr * uint64(constantDurMS) * uint64(track.TimeScale) / 1000
	endTime := (nr + 1) * uint64(constantDurMS) * uint64(track.TimeScale) / 1000
	startNr = startTime / uint64(track.SampleDur)
	if startTime%uint64(track.SampleDur) != 0 {
		startNr++
	}
	endNr = endTime / uint64(track.SampleDur)
	if endTime%uint64(track.SampleDur) != 0 {
		endNr++
	}
	return startNr, endNr
}

// CurrMoQGroupNr returns the current MoQGroup number/ID for a given time.
func CurrMoQGroupNr(track *ContentTrack, nowMS uint64, constantDurMS uint32) uint64 {
	return nowMS / uint64(constantDurMS)
}

// WriteMoQGroup writes all MoQGroup objects to an ObjectWriter.
// The MoQGroup is sent in the correct time order and at appropriate times if ongoing session.
// If the context is done, the function returns the error from the context.
func WriteMoQGroup(ctx context.Context, track *ContentTrack, moq *MoQGroup, ow ObjectWriter) error {
	factorMS := 1000 / float64(track.TimeScale)
	for nr, moqObj := range moq.MoQObjects {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		now := time.Now().UnixMilli()
		objTime := moq.startTime + uint64(nr+1)*uint64(track.SampleDur)*uint64(track.SampleBatch)
		objTimeMS := int64(float64(objTime) * factorMS)
		waitTime := objTimeMS - now
		if waitTime <= 0 {
			_, err := ow(uint64(nr), moqObj)
			if err != nil {
				return err
			}
			continue
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(waitTime) * time.Millisecond):
			_, err := ow(uint64(nr), moqObj)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// GetLargestObject returns the largest object location for a track at the current time.
// According to draft-11, this is the object with the largest Location {Group, Object}
// from the perspective of the current publishing state.
func GetLargestObject(track *ContentTrack, nowMS uint64, constantDurMS uint32) Location {
	currentGroupNr := CurrMoQGroupNr(track, nowMS, constantDurMS)
	
	// Use calcMoQGroup to get the sample range for this group
	startNr, endNr := calcMoQGroup(track, currentGroupNr, constantDurMS)
	
	// Calculate which sample should be available at the current time
	currentTimeTrackUnits := nowMS * uint64(track.TimeScale) / 1000
	currentSampleNr := currentTimeTrackUnits / uint64(track.SampleDur)
	
	// Clamp to the group boundaries
	if currentSampleNr < startNr {
		// We're before this group starts, return previous group's last object
		if currentGroupNr > 0 {
			prevStartNr, prevEndNr := calcMoQGroup(track, currentGroupNr-1, constantDurMS)
			samplesInPrevGroup := prevEndNr - prevStartNr
			objectsInPrevGroup := (samplesInPrevGroup + uint64(track.SampleBatch) - 1) / uint64(track.SampleBatch)
			return Location{
				Group:  currentGroupNr - 1,
				Object: objectsInPrevGroup - 1, // 0-based index
			}
		}
		return Location{Group: 0, Object: 0}
	}
	
	if currentSampleNr >= endNr {
		currentSampleNr = endNr - 1 // Last sample in this group
	}
	
	// Calculate which object this sample belongs to within the group
	samplesIntoGroup := currentSampleNr - startNr
	objectNr := samplesIntoGroup / uint64(track.SampleBatch)
	
	return Location{
		Group:  currentGroupNr,
		Object: objectNr,
	}
}
