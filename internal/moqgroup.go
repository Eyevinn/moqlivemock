package internal

import (
	"context"
	"log/slog"
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
	groupNr    uint64
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
		groupNr:    groupNr,
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

// calcMoQGroup calculates the start and end sample numbers for a MoQGroup
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
// Takes into account sample offset and object duration - groups don't start exactly at full seconds.
func CurrMoQGroupNr(track *ContentTrack, nowMS uint64, constantDurMS uint32) uint64 {
	// Calculate object duration in milliseconds
	objectDurMS := float64(track.SampleDur*uint32(track.SampleBatch)) * 1000.0 / float64(track.TimeScale)

	// Calculate sample offset based on content type
	var sampleOffsetMS float64
	if track.ContentType == "audio" {
		// For audio, offset is minimal time later than video given audio sample duration
		audioSampleDurMS := float64(track.SampleDur) * 1000.0 / float64(track.TimeScale)
		sampleOffsetMS = audioSampleDurMS
	} else {
		// Video has no sample offset
		sampleOffsetMS = 0
	}

	// The effective start time for groups is shifted by sampleOffset + objectDuration
	// Group 0 first object becomes available at: sampleOffset + objectDuration
	// Group 1 first object becomes available at: 1000 + sampleOffset + objectDuration
	// So group G starts effectively at: G * constantDurMS + sampleOffset + objectDuration

	groupStartOffset := sampleOffsetMS + objectDurMS

	// If we're before the first group starts, we're in group 0
	if float64(nowMS) < groupStartOffset {
		return 0
	}

	// Calculate which group we're in based on the effective start time
	adjustedTime := float64(nowMS) - groupStartOffset
	return uint64(adjustedTime / float64(constantDurMS))
}

// WriteMoQGroup writes all MoQGroup objects to an ObjectWriter.
// The MoQGroup is sent in the correct time order and at appropriate times if ongoing session.
// If the context is done, the function returns the error from the context.
//
// Availability time calculation:
// - Object (G, 0) is available at G seconds + sampleOffset + objectDuration relative to Epoch
// - Object (G, N) is available N*objectDuration later
// - Video has sampleOffset = 0
// - Audio has sampleOffset = minimal time later than video given audio sample duration
func WriteMoQGroup(ctx context.Context, logger *slog.Logger, track *ContentTrack,
	moq *MoQGroup, ow ObjectWriter) error {
	groupNr := moq.groupNr

	// Calculate object duration in milliseconds
	objectDurMS := float64(track.SampleDur*uint32(track.SampleBatch)) * 1000.0 / float64(track.TimeScale)

	// Calculate sample offset based on content type
	var sampleOffsetMS float64
	if track.ContentType == "audio" {
		// For audio, offset is minimal time later than video given audio sample duration
		// This ensures audio objects are available slightly after corresponding video
		audioSampleDurMS := float64(track.SampleDur) * 1000.0 / float64(track.TimeScale)
		sampleOffsetMS = audioSampleDurMS
	} else {
		// Video has no sample offset
		sampleOffsetMS = 0
	}

	for objNr, moqObj := range moq.MoQObjects {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		// Calculate availability time:
		// Object (G, 0) = G seconds + sampleOffset + objectDuration (available when object ENDS)
		// Object (G, N) = Object (G, 0) + N * objectDuration
		baseAvailabilityMS := float64(groupNr)*1000.0 + sampleOffsetMS + objectDurMS
		objAvailabilityMS := baseAvailabilityMS + float64(objNr)*objectDurMS

		now := time.Now().UnixMilli()
		waitTime := int64(objAvailabilityMS) - now

		if waitTime <= 0 {
			logger.Info("write MoQ object", "group", groupNr, "object", objNr)
			_, err := ow(uint64(objNr), moqObj)
			if err != nil {
				return err
			}
			continue
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(waitTime) * time.Millisecond):
			logger.Info("write MoQ object", "group", groupNr, "object", objNr)
			_, err := ow(uint64(objNr), moqObj)
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
// Objects are available when they END, not when they start.
func GetLargestObject(track *ContentTrack, nowMS uint64, constantDurMS uint32) Location {
	// Calculate object duration in milliseconds
	objectDurMS := float64(track.SampleDur*uint32(track.SampleBatch)) * 1000.0 / float64(track.TimeScale)

	// Calculate sample offset based on content type
	var sampleOffsetMS float64
	if track.ContentType == "audio" {
		// For audio, offset is minimal time later than video given audio sample duration
		audioSampleDurMS := float64(track.SampleDur) * 1000.0 / float64(track.TimeScale)
		sampleOffsetMS = audioSampleDurMS
	} else {
		// Video has no sample offset
		sampleOffsetMS = 0
	}

	// Find the largest available object by iterating through groups and objects
	// Start from current group and work backwards if needed
	currentGroupNr := CurrMoQGroupNr(track, nowMS, constantDurMS)

	// Check objects in current and previous groups
	for groupNr := int64(currentGroupNr); groupNr >= 0; groupNr-- {
		// Calculate how many objects are in this group
		startNr, endNr := calcMoQGroup(track, uint64(groupNr), constantDurMS)
		samplesInGroup := endNr - startNr
		objectsInGroup := (samplesInGroup + uint64(track.SampleBatch) - 1) / uint64(track.SampleBatch)

		// Check objects in reverse order (highest object number first)
		for objNr := int64(objectsInGroup) - 1; objNr >= 0; objNr-- {
			// Calculate availability time for this object:
			// Object (G, 0) = G seconds + sampleOffset + objectDuration (when object ENDS)
			// Object (G, N) = Object (G, 0) + N * objectDuration
			baseAvailabilityMS := float64(groupNr)*1000.0 + sampleOffsetMS + objectDurMS
			objAvailabilityMS := baseAvailabilityMS + float64(objNr)*objectDurMS

			// If this object is available now (has ended), it's our largest object
			if float64(nowMS) >= objAvailabilityMS {
				return Location{
					Group:  uint64(groupNr),
					Object: uint64(objNr),
				}
			}
		}
	}

	// If no objects are available yet, return (0, 0)
	return Location{Group: 0, Object: 0}
}
