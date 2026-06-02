package locmafv02

import (
	"fmt"

	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/quic-go/quic-go/quicvarint"
)

// rawProperties splits a property block into a map of raw bytes per
// field ID, applying the parity rule from draft §12.3:
// even IDs are scalar varints (no length prefix); odd IDs are
// length-prefixed bytes.
func rawProperties(data []byte) (map[fieldID][]byte, error) {
	out := make(map[fieldID][]byte)
	pos := 0
	for pos < len(data) {
		idValue, n, err := quicvarint.Parse(data[pos:])
		if err != nil {
			return nil, fmt.Errorf("locmafv02: invalid field id at offset %d", pos)
		}
		pos += n
		id := fieldID(idValue)
		if !id.isList() {
			// scalar varint
			_, n, err := quicvarint.Parse(data[pos:])
			if err != nil {
				return nil, fmt.Errorf("locmafv02: invalid scalar value for id=%d", id)
			}
			if pos+n > len(data) {
				return nil, errShort(id)
			}
			out[id] = append([]byte(nil), data[pos:pos+n]...)
			pos += n
		} else {
			length, n, err := quicvarint.Parse(data[pos:])
			if err != nil {
				return nil, fmt.Errorf("locmafv02: invalid length for id=%d", id)
			}
			pos += n
			if pos+int(length) > len(data) {
				return nil, errShort(id)
			}
			out[id] = append([]byte(nil), data[pos:pos+int(length)]...)
			pos += int(length)
		}
	}
	return out, nil
}

// parseFullProperties parses a full chunk's properties into
// chunkValues. Each field is interpreted in its absolute form.
func parseFullProperties(data []byte) (*chunkValues, error) {
	raw, err := rawProperties(data)
	if err != nil {
		return nil, err
	}
	cv := newChunkValues()

	for id, bytes := range raw {
		switch {
		case isScalarMoofID(id):
			v, _, err := quicvarint.Parse(bytes)
			if err != nil {
				return nil, fmt.Errorf("locmafv02: invalid scalar id=%d", id)
			}
			cv.scalars[id] = v
		case id == idTrunSampleCompositionTimeOffsets:
			list, err := parseZigzagList(bytes)
			if err != nil {
				return nil, fmt.Errorf("locmafv02: invalid signed list id=%d: %w", id, err)
			}
			cv.signedLists[id] = list
		case id == idSencInitializationVector:
			cv.rawBlobs[id] = append([]byte(nil), bytes...)
		case isVarintListMoofID(id):
			list, err := parseVarintList(bytes)
			if err != nil {
				return nil, fmt.Errorf("locmafv02: invalid varint list id=%d: %w", id, err)
			}
			cv.lists[id] = list
		default:
			// Forward-compat: keep unknown IDs as raw blobs so the
			// receiver doesn't trip. A follow-up revision can decide
			// what to do with them.
			cv.rawBlobs[id] = append([]byte(nil), bytes...)
		}
	}

	return cv, nil
}

// parseDeltaProperties applies a delta property block to prev and
// returns the reconstructed current chunkValues. Deletions are
// applied before delta deltas, per draft §16.4.
func parseDeltaProperties(data []byte, prev *State, moov *mp4.MoovBox) (*chunkValues, error) {
	raw, err := rawProperties(data)
	if err != nil {
		return nil, err
	}

	cv := snapshotPrev(prev)

	// Step 1: deletions.
	if delBytes, ok := raw[idDeltaDeletedLocmafIDs]; ok {
		ids, err := parseVarintList(delBytes)
		if err != nil {
			return nil, fmt.Errorf("locmafv02: invalid deletion list: %w", err)
		}
		for _, id := range ids {
			delete(cv.scalars, fieldID(id))
			delete(cv.lists, fieldID(id))
			delete(cv.signedLists, fieldID(id))
			delete(cv.rawBlobs, fieldID(id))
		}
		delete(raw, idDeltaDeletedLocmafIDs)
	}

	// Step 2: apply field deltas. Order matters for sample-count
	// dependent list sizing — do it via a sweep keyed by field ID,
	// and look up the *current* sample count when sizing per-sample
	// lists.
	currentSampleCount := uint64(0)
	if bytes, ok := raw[idTrunSampleCount]; ok {
		delta, _, err := quicvarint.Parse(bytes)
		if err != nil {
			return nil, fmt.Errorf("locmafv02: invalid scalar id=%d", idTrunSampleCount)
		}
		dec := int64(delta >> 1)
		if delta&1 != 0 {
			dec = ^dec
		}
		prevSC := int64(cv.scalars[idTrunSampleCount])
		newSC := prevSC + dec
		if newSC < 0 {
			return nil, fmt.Errorf("locmafv02: negative sample_count after delta")
		}
		currentSampleCount = uint64(newSC)
		cv.scalars[idTrunSampleCount] = currentSampleCount
		delete(raw, idTrunSampleCount)
	} else {
		currentSampleCount = cv.scalars[idTrunSampleCount]
	}

	// idTfdtBaseMediaDecodeTime: explicit override if present, else
	// derived.
	if bytes, ok := raw[idTfdtBaseMediaDecodeTime]; ok {
		// Absolute encoding, plain unsigned varint, per §16.2.
		v, _, err := quicvarint.Parse(bytes)
		if err != nil {
			return nil, fmt.Errorf("locmafv02: invalid absolute BMDT")
		}
		cv.scalars[idTfdtBaseMediaDecodeTime] = v
		delete(raw, idTfdtBaseMediaDecodeTime)
	} else {
		// Use previous state's BMDT + prev duration sum.
		derived, ok := deriveNextBMDT(prev, moov)
		if ok {
			cv.scalars[idTfdtBaseMediaDecodeTime] = derived
		}
	}

	// Apply the rest.
	for id, bytes := range raw {
		switch {
		case isScalarMoofID(id):
			z, _, err := quicvarint.Parse(bytes)
			if err != nil {
				return nil, fmt.Errorf("locmafv02: invalid delta scalar id=%d", id)
			}
			dec := int64(z >> 1)
			if z&1 != 0 {
				dec = ^dec
			}
			prevV, has := prev.scalars[id]
			var newV int64
			if has {
				newV = int64(prevV) + dec
			} else {
				newV = dec
			}
			if newV < 0 {
				return nil, fmt.Errorf("locmafv02: negative scalar id=%d after delta", id)
			}
			cv.scalars[id] = uint64(newV)
		case id == idTrunSampleCompositionTimeOffsets:
			deltas, err := parseZigzagList(bytes)
			if err != nil {
				return nil, fmt.Errorf("locmafv02: invalid signed delta list id=%d: %w", id, err)
			}
			prevList := prev.signedLists[id]
			out := make([]int64, len(deltas))
			for i, d := range deltas {
				var p int64
				if i < len(prevList) {
					p = prevList[i]
				}
				out[i] = p + d
			}
			cv.signedLists[id] = out
		case id == idSencInitializationVector:
			cv.rawBlobs[id] = append([]byte(nil), bytes...)
		case isVarintListMoofID(id):
			deltas, err := parseZigzagList(bytes)
			if err != nil {
				return nil, fmt.Errorf("locmafv02: invalid delta list id=%d: %w", id, err)
			}
			prevList := prev.lists[id]
			out := make([]uint64, len(deltas))
			for i, d := range deltas {
				var p int64
				if i < len(prevList) {
					p = int64(prevList[i])
				}
				v := p + d
				if v < 0 {
					return nil, fmt.Errorf("locmafv02: negative list value id=%d at index %d", id, i)
				}
				out[i] = uint64(v)
			}
			cv.lists[id] = out
		default:
			cv.rawBlobs[id] = append([]byte(nil), bytes...)
		}
	}

	// Step 3: truncate per-sample lists to the current sample count.
	// Draft §16.1 says a list shorter than previous is just truncated
	// — no bytes are emitted for the missing tail. So if the wire
	// carried fewer delta elements than current sample count, the
	// remaining positions are unchanged from prev — which our
	// snapshotPrev seeded but our overwrite above wiped. Restore
	// from prev for any short delta list.
	fixListLen := func(id fieldID) {
		list, ok := cv.lists[id]
		if !ok {
			return
		}
		want := int(currentSampleCount)
		switch id {
		case idSencBytesOfClearData, idSencBytesOfProtectedData:
			// Subsample lists are sized by sum(subsampleCount),
			// not sample count. Skip the per-sample truncation.
			return
		case idTrunSampleSizes:
			// Per §15.1 sample-size-derivation, sample_sizes carries
			// sample_count − 1 entries (the last is derived).
			if want > 0 {
				want--
			}
		}
		if len(list) > want {
			cv.lists[id] = list[:want]
			return
		}
		if len(list) < want {
			// Pad from prev list, then truncate.
			padded := make([]uint64, want)
			copy(padded, list)
			prevList := prev.lists[id]
			for i := len(list); i < want && i < len(prevList); i++ {
				padded[i] = prevList[i]
			}
			cv.lists[id] = padded
		}
	}
	fixSignedListLen := func(id fieldID) {
		list, ok := cv.signedLists[id]
		if !ok {
			return
		}
		want := int(currentSampleCount)
		if len(list) > want {
			cv.signedLists[id] = list[:want]
			return
		}
		if len(list) < want {
			padded := make([]int64, want)
			copy(padded, list)
			prevList := prev.signedLists[id]
			for i := len(list); i < want && i < len(prevList); i++ {
				padded[i] = prevList[i]
			}
			cv.signedLists[id] = padded
		}
	}
	fixListLen(idTrunSampleSizes)
	fixListLen(idTrunSampleDurations)
	fixListLen(idTrunSampleFlags)
	fixSignedListLen(idTrunSampleCompositionTimeOffsets)

	return cv, nil
}

// snapshotPrev returns a chunkValues seeded from prev — every field
// is inherited unchanged. Delta processing then overwrites entries
// that the wire carried explicitly.
func snapshotPrev(prev *State) *chunkValues {
	cv := newChunkValues()
	for k, v := range prev.scalars {
		cv.scalars[k] = v
	}
	for k, v := range prev.lists {
		cv.lists[k] = append([]uint64(nil), v...)
	}
	for k, v := range prev.signedLists {
		cv.signedLists[k] = append([]int64(nil), v...)
	}
	for k, v := range prev.rawBlobs {
		cv.rawBlobs[k] = append([]byte(nil), v...)
	}
	return cv
}

// isScalarMoofID returns true for moof fields with the scalar
// (even-ID) encoding.
func isScalarMoofID(id fieldID) bool {
	switch id {
	case idTfhdSampleDescriptionIndex, idTfhdDefaultSampleDuration,
		idTfhdDefaultSampleSize, idTfhdDefaultSampleFlags,
		idTfdtBaseMediaDecodeTime, idTrunFirstSampleFlags,
		idTrunSampleCount, idSencPerSampleIVSize:
		return true
	}
	return false
}

// isVarintListMoofID returns true for moof fields whose payload is
// an unsigned varint list (odd ID, with the exception of
// idSencInitializationVector which is a raw blob and
// idTrunSampleCompositionTimeOffsets which uses zigzag).
func isVarintListMoofID(id fieldID) bool {
	switch id {
	case idTrunSampleSizes, idTrunSampleDurations, idTrunSampleFlags,
		idSencSubsampleCount, idSencBytesOfClearData, idSencBytesOfProtectedData:
		return true
	}
	return false
}

// reconstructMoof builds an mp4 MoofBox from the decoded chunkValues
// plus the catalog moov. seqnum is the MoQ group ID the caller wants
// to put in the mfhd; mdatPayloadLength is used to derive
// single-sample sizes per draft §15.1.
func reconstructMoof(cv *chunkValues, seqnum uint32, moov *mp4.MoovBox, mdatPayloadLength int) (*mp4.MoofBox, error) {
	if moov == nil || moov.Mvex == nil || moov.Mvex.Trex == nil {
		return nil, fmt.Errorf("locmafv02: moov or trex not defined")
	}
	trackID := uint32(1)
	if moov.Trak != nil && moov.Trak.Tkhd != nil && moov.Trak.Tkhd.TrackID != 0 {
		trackID = moov.Trak.Tkhd.TrackID
	}
	frag, err := mp4.CreateFragment(seqnum, trackID)
	if err != nil {
		return nil, fmt.Errorf("locmafv02: create fragment: %w", err)
	}
	traf := frag.Moof.Traf
	trex := moov.Mvex.Trex

	// Seed defaults from trex.
	traf.Tfhd.SampleDescriptionIndex = trex.DefaultSampleDescriptionIndex
	traf.Tfhd.DefaultSampleDuration = trex.DefaultSampleDuration
	traf.Tfhd.DefaultSampleSize = trex.DefaultSampleSize
	traf.Tfhd.DefaultSampleFlags = trex.DefaultSampleFlags

	if v, ok := cv.scalars[idTfhdSampleDescriptionIndex]; ok {
		traf.Tfhd.SampleDescriptionIndex = uint32(v)
		traf.Tfhd.Flags |= mp4.TfhdSampleDescriptionIndexPresentFlag
	}
	if v, ok := cv.scalars[idTfhdDefaultSampleDuration]; ok {
		traf.Tfhd.DefaultSampleDuration = uint32(v)
		traf.Tfhd.Flags |= mp4.TfhdDefaultSampleDurationPresentFlag
	}
	defaultSize, hasDefaultSize := cv.scalars[idTfhdDefaultSampleSize]
	if hasDefaultSize {
		traf.Tfhd.DefaultSampleSize = uint32(defaultSize)
		traf.Tfhd.Flags |= mp4.TfhdDefaultSampleSizePresentFlag
	}
	if v, ok := cv.scalars[idTfhdDefaultSampleFlags]; ok {
		traf.Tfhd.DefaultSampleFlags = unpackSampleFlags(v)
		traf.Tfhd.Flags |= mp4.TfhdDefaultSampleFlagsPresentFlag
	}

	perSampleIVSize := defaultPerSampleIVSize(moov, traf.Tfhd.TrackID)
	if v, ok := cv.scalars[idSencPerSampleIVSize]; ok {
		perSampleIVSize = uint8(v)
	}

	bmdt, ok := cv.scalars[idTfdtBaseMediaDecodeTime]
	if !ok {
		return nil, fmt.Errorf("locmafv02: missing BMDT in reconstructed chunk")
	}
	traf.Tfdt.SetBaseMediaDecodeTime(bmdt)

	if v, ok := cv.scalars[idTrunFirstSampleFlags]; ok {
		traf.Trun.SetFirstSampleFlags(unpackSampleFlags(v))
	}

	sampleCount64, ok := cv.scalars[idTrunSampleCount]
	if !ok {
		return nil, fmt.Errorf("locmafv02: missing SampleCount in reconstructed chunk")
	}
	sampleCount := int(sampleCount64)

	sampleSizes, hasSizes := cv.lists[idTrunSampleSizes]
	if hasSizes {
		// Variable sizes per §15.1: list carries n−1 entries; derive
		// the last from the mdat payload length.
		if len(sampleSizes) != sampleCount-1 {
			return nil, fmt.Errorf("locmafv02: sample_sizes list has %d entries, expected %d (sample_count − 1)",
				len(sampleSizes), sampleCount-1)
		}
		if mdatPayloadLength < 0 {
			return nil, fmt.Errorf("locmafv02: variable-sized samples need mdat payload length")
		}
		var sum uint64
		for _, s := range sampleSizes {
			sum += s
		}
		if uint64(mdatPayloadLength) < sum {
			return nil, fmt.Errorf("locmafv02: mdat payload length %d < sum of listed sample sizes %d",
				mdatPayloadLength, sum)
		}
		full := make([]uint64, sampleCount)
		copy(full, sampleSizes)
		full[sampleCount-1] = uint64(mdatPayloadLength) - sum
		sampleSizes = full
	} else if sampleCount == 1 {
		if mdatPayloadLength < 0 {
			return nil, fmt.Errorf("locmafv02: single-sample chunk needs mdat payload length")
		}
		sampleSizes = []uint64{uint64(mdatPayloadLength)}
	} else {
		// Uniform sizes: moofDefaultSampleSize or trex.default_sample_size.
		sampleSizes = repeatUint64(uint64(traf.Tfhd.DefaultSampleSize), sampleCount)
	}
	if len(sampleSizes) != sampleCount {
		return nil, fmt.Errorf("locmafv02: sample_sizes length mismatch")
	}

	sampleDurations, hasDurations := cv.lists[idTrunSampleDurations]
	if !hasDurations {
		sampleDurations = repeatUint64(uint64(traf.Tfhd.DefaultSampleDuration), sampleCount)
	}
	if len(sampleDurations) != sampleCount {
		return nil, fmt.Errorf("locmafv02: sample_durations length mismatch")
	}

	sampleFlags, hasFlags := cv.lists[idTrunSampleFlags]
	if !hasFlags {
		// Pad to sampleCount with the default-flags value already
		// installed in tfhd (unpacked back to 32-bit form).
		sampleFlags = make([]uint64, sampleCount)
		for i := range sampleFlags {
			// Store the *packed* form so the unpack at the loop
			// below produces the right 32-bit flags. We re-pack
			// the default tfhd flags through pack→unpack so the
			// shape is the same as a per-sample value.
			packed, err := packSampleFlags(traf.Tfhd.DefaultSampleFlags)
			if err != nil {
				return nil, err
			}
			sampleFlags[i] = packed
		}
	}
	if len(sampleFlags) != sampleCount {
		return nil, fmt.Errorf("locmafv02: sample_flags length mismatch")
	}

	cts, hasCTS := cv.signedLists[idTrunSampleCompositionTimeOffsets]
	if !hasCTS {
		cts = make([]int64, sampleCount)
		traf.Trun.Flags &^= mp4.TrunSampleCompositionTimeOffsetPresentFlag
	}
	if len(cts) != sampleCount {
		return nil, fmt.Errorf("locmafv02: cts_offsets length mismatch")
	}

	for i := 0; i < sampleCount; i++ {
		flags32 := unpackSampleFlags(sampleFlags[i])
		traf.Trun.AddSample(mp4.NewSample(flags32,
			uint32(sampleDurations[i]),
			uint32(sampleSizes[i]),
			int32(cts[i])))
	}

	if hasCTS {
		// AddSample already set the present flag; nothing to do.
		_ = hasCTS
	}

	senc, err := reconstructSenc(cv, sampleCount, perSampleIVSize,
		shouldCreateEmptySenc(moov, traf.Tfhd.TrackID, perSampleIVSize))
	if err != nil {
		return nil, err
	}
	if senc != nil {
		if err := traf.AddChild(senc); err != nil {
			return nil, fmt.Errorf("locmafv02: attach senc: %w", err)
		}
	}

	return frag.Moof, nil
}

func reconstructSenc(cv *chunkValues, sampleCount int,
	perSampleIVSize uint8, createEmpty bool) (*mp4.SencBox, error) {

	ivPayload, hasIVs := cv.rawBlobs[idSencInitializationVector]
	subCounts, hasSubCounts := cv.lists[idSencSubsampleCount]
	clear, hasClear := cv.lists[idSencBytesOfClearData]
	prot, hasProt := cv.lists[idSencBytesOfProtectedData]

	if !createEmpty && !hasIVs && !hasSubCounts && !hasClear && !hasProt {
		return nil, nil
	}

	senc := mp4.CreateSencBox()

	if hasIVs {
		if perSampleIVSize == 0 {
			return nil, fmt.Errorf("locmafv02: IVs present but per-sample IV size is 0")
		}
		if len(ivPayload)%int(perSampleIVSize) != 0 {
			return nil, fmt.Errorf("locmafv02: IV payload length not a multiple of per-sample IV size")
		}
		if len(ivPayload)/int(perSampleIVSize) != sampleCount {
			return nil, fmt.Errorf("locmafv02: IV count != sample_count")
		}
		senc.SetPerSampleIVSize(perSampleIVSize)
	}

	if hasSubCounts && len(subCounts) != sampleCount {
		return nil, fmt.Errorf("locmafv02: subsample_count list length != sample_count")
	}

	totalSubsamples := 0
	if hasSubCounts {
		for _, c := range subCounts {
			totalSubsamples += int(c)
		}
	}
	if hasClear && len(clear) != totalSubsamples {
		return nil, fmt.Errorf("locmafv02: bytes_of_clear length mismatch")
	}
	if hasProt && len(prot) != totalSubsamples {
		return nil, fmt.Errorf("locmafv02: bytes_of_protected length mismatch")
	}
	if (hasClear || hasProt) && !hasSubCounts {
		return nil, fmt.Errorf("locmafv02: subsample byte lists without subsample_count")
	}

	subsampleIdx := 0
	for i := 0; i < sampleCount; i++ {
		var sampleIV []byte
		if hasIVs {
			s := int(perSampleIVSize)
			sampleIV = append([]byte(nil), ivPayload[i*s:(i+1)*s]...)
		}
		var subs []mp4.SubSamplePattern
		if hasSubCounts {
			cnt := int(subCounts[i])
			subs = make([]mp4.SubSamplePattern, cnt)
			for j := 0; j < cnt; j++ {
				c := clear[subsampleIdx]
				p := prot[subsampleIdx]
				subsampleIdx++
				if c > 0xFFFF {
					return nil, fmt.Errorf("locmafv02: clear bytes overflow uint16")
				}
				subs[j] = mp4.SubSamplePattern{
					BytesOfClearData:     uint16(c),
					BytesOfProtectedData: uint32(p),
				}
			}
		}
		if err := senc.AddSample(mp4.SencSample{IV: sampleIV, SubSamples: subs}); err != nil {
			return nil, fmt.Errorf("locmafv02: senc.AddSample: %w", err)
		}
	}

	return senc, nil
}

func repeatUint64(v uint64, n int) []uint64 {
	out := make([]uint64, n)
	for i := range out {
		out[i] = v
	}
	return out
}
