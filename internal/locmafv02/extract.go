package locmafv02

import (
	"fmt"

	"github.com/Eyevinn/mp4ff/mp4"
)

// chunkValues is the in-memory representation of one LOCMAF v0.2
// chunk's moof properties, as decoded uint64 / int64 / []byte values.
// Both compressor and decompressor work from this shape; the wire
// encoding (full vs delta) is computed by comparing against a prior
// chunkValues held in *State.
type chunkValues struct {
	// presence per field ID. Determines what makes it onto the wire.
	scalars     map[fieldID]uint64
	signed      map[fieldID]int64 // currently unused; reserved for future
	lists       map[fieldID][]uint64
	signedLists map[fieldID][]int64
	rawBlobs    map[fieldID][]byte
}

func newChunkValues() *chunkValues {
	return &chunkValues{
		scalars:     make(map[fieldID]uint64),
		signed:      make(map[fieldID]int64),
		lists:       make(map[fieldID][]uint64),
		signedLists: make(map[fieldID][]int64),
		rawBlobs:    make(map[fieldID][]byte),
	}
}

// extractMoofFields walks a CMAF moof + paired moov and produces the
// decoded chunkValues that feed the encoder. Emission rules follow
// draft §15.1.
func extractMoofFields(moof *mp4.MoofBox, moov *mp4.MoovBox) (*chunkValues, error) {
	if moof == nil || moof.Traf == nil {
		return nil, fmt.Errorf("locmafv02: moof or traf not defined")
	}
	if moov == nil || moov.Mvex == nil || moov.Mvex.Trex == nil {
		return nil, fmt.Errorf("locmafv02: moov or trex not defined")
	}

	cv := newChunkValues()
	traf := moof.Traf
	trun := traf.Trun
	if trun == nil {
		return nil, fmt.Errorf("locmafv02: trun is nil")
	}
	tfhd := traf.Tfhd
	if tfhd == nil {
		return nil, fmt.Errorf("locmafv02: tfhd is nil")
	}
	tfdt := traf.Tfdt
	if tfdt == nil {
		return nil, fmt.Errorf("locmafv02: tfdt is nil")
	}
	trex := moov.Mvex.Trex
	sampleCount := uint64(len(trun.Samples))
	singleSample := sampleCount == 1

	// Scalar tfhd fields.
	if tfhd.HasSampleDescriptionIndex() && tfhd.SampleDescriptionIndex != trex.DefaultSampleDescriptionIndex {
		cv.scalars[idTfhdSampleDescriptionIndex] = uint64(tfhd.SampleDescriptionIndex)
	}
	if tfhd.HasDefaultSampleDuration() && tfhd.DefaultSampleDuration != trex.DefaultSampleDuration {
		cv.scalars[idTfhdDefaultSampleDuration] = uint64(tfhd.DefaultSampleDuration)
	}
	// Sample size emission is decided below together with the per-sample
	// list, per draft §15.1 sample-size-derivation.
	if tfhd.HasDefaultSampleFlags() && tfhd.DefaultSampleFlags != trex.DefaultSampleFlags {
		packed, err := packSampleFlags(tfhd.DefaultSampleFlags)
		if err != nil {
			return nil, err
		}
		cv.scalars[idTfhdDefaultSampleFlags] = packed
	}

	// Always emit.
	cv.scalars[idTfdtBaseMediaDecodeTime] = tfdt.BaseMediaDecodeTime()
	cv.scalars[idTrunSampleCount] = sampleCount

	// trun first_sample_flags.
	if firstFlags, present := trun.FirstSampleFlags(); present {
		packed, err := packSampleFlags(firstFlags)
		if err != nil {
			return nil, err
		}
		cv.scalars[idTrunFirstSampleFlags] = packed
	}

	// Sample sizes per draft §15.1 sample-size-derivation:
	// - sample_count == 1: omit both (receiver derives from mdat).
	// - sample_count > 1, all sizes equal: emit moofDefaultSampleSize
	//   when ≠ trex.default_sample_size; receiver expands to n samples.
	// - sample_count > 1, sizes vary: emit moofSampleSizes with n−1
	//   entries; receiver derives the last from mdat length.
	if !singleSample && len(trun.Samples) > 0 {
		firstSize := trun.Samples[0].Size
		allEqual := true
		for _, s := range trun.Samples {
			if s.Size != firstSize {
				allEqual = false
				break
			}
		}
		if allEqual {
			if firstSize != trex.DefaultSampleSize {
				cv.scalars[idTfhdDefaultSampleSize] = uint64(firstSize)
			}
		} else {
			n := len(trun.Samples) - 1
			sizes := make([]uint64, n)
			for i := 0; i < n; i++ {
				sizes[i] = uint64(trun.Samples[i].Size)
			}
			cv.lists[idTrunSampleSizes] = sizes
		}
	}
	if trun.HasSampleDuration() {
		durations := make([]uint64, sampleCount)
		for i, s := range trun.Samples {
			durations[i] = uint64(s.Dur)
		}
		cv.lists[idTrunSampleDurations] = durations
	}
	if trun.HasSampleFlags() {
		flags := make([]uint64, sampleCount)
		for i, s := range trun.Samples {
			packed, err := packSampleFlags(s.Flags)
			if err != nil {
				return nil, err
			}
			flags[i] = packed
		}
		cv.lists[idTrunSampleFlags] = flags
	}
	if trun.HasSampleCompositionTimeOffset() {
		ctsOffsets := make([]int64, sampleCount)
		for i, s := range trun.Samples {
			ctsOffsets[i] = int64(s.CompositionTimeOffset)
		}
		cv.signedLists[idTrunSampleCompositionTimeOffsets] = ctsOffsets
	}

	// senc / encryption metadata. We mirror v0.1's helper so the
	// two codecs agree on what counts as "present".
	senc, perSampleIVSize, err := parseSenc(moof, moov)
	if err != nil {
		return nil, err
	}
	if senc != nil {
		defaultIVSize := defaultPerSampleIVSize(moov, traf.Tfhd.TrackID)
		if perSampleIVSize != defaultIVSize {
			cv.scalars[idSencPerSampleIVSize] = uint64(perSampleIVSize)
		}
		if perSampleIVSize > 0 && len(senc.IVs) > 0 {
			allIVs := make([]byte, 0, int(perSampleIVSize)*len(senc.IVs))
			for _, iv := range senc.IVs {
				allIVs = append(allIVs, iv...)
			}
			cv.rawBlobs[idSencInitializationVector] = allIVs
		}
		if len(senc.SubSamples) > 0 {
			subCounts := make([]uint64, 0, len(senc.SubSamples))
			var clear []uint64
			var prot []uint64
			for _, ss := range senc.SubSamples {
				subCounts = append(subCounts, uint64(len(ss)))
				for _, s := range ss {
					clear = append(clear, uint64(s.BytesOfClearData))
					prot = append(prot, uint64(s.BytesOfProtectedData))
				}
			}
			cv.lists[idSencSubsampleCount] = subCounts
			cv.lists[idSencBytesOfClearData] = clear
			cv.lists[idSencBytesOfProtectedData] = prot
		}
	}

	return cv, nil
}
