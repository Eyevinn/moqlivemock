package locmafv02

import (
	"fmt"
	"log/slog"
	"sort"

	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/quic-go/quic-go/quicvarint"
)

// Compress encodes one CMAF chunk's moof + mdat payload as a LOCMAF
// v0.2 object. If prev is nil or empty (hasAny == false) the result
// uses LocmafFullHeader (ID 23); otherwise it uses LocmafDeltaHeader
// (ID 25) and the value of every emitted field is interpreted
// relative to prev.
//
// The mdat payload follows the property block unchanged. The caller
// is responsible for raw mdat bytes (no surrounding box header) per
// draft §12.2.
//
// Compress mutates prev in place to reflect the just-emitted chunk
// so the caller can keep using the same *State across the group. If
// prev is nil, Compress allocates and returns a fresh State.
func Compress(moof *mp4.MoofBox, prev *State, moov *mp4.MoovBox) ([]byte, *State, error) {
	current, err := extractMoofFields(moof, moov)
	if err != nil {
		return nil, prev, err
	}

	if prev == nil {
		prev = NewState()
	}

	var headerID uint64
	var props []byte
	if !prev.hasAny {
		headerID = LocmafFullHeaderID
		props = encodeFullProperties(current)
	} else {
		headerID = LocmafDeltaHeaderID
		props, err = encodeDeltaProperties(current, prev, moov)
		if err != nil {
			return nil, prev, err
		}
	}

	// LOCMAF object: header_id | properties_length | properties.
	// The caller appends the mdat payload.
	out := make([]byte, 0, len(props)+8)
	out = appendVarint(out, headerID)
	out = appendVarint(out, uint64(len(props)))
	out = append(out, props...)

	// Update state to the just-emitted chunk's values.
	prev.hasAny = true
	storeChunkValues(prev, current)
	return out, prev, nil
}

// Decompress decodes a LOCMAF v0.2 object back into an mp4 MoofBox.
// The returned MoofBox does NOT include the mdat — the caller pairs
// it with the trailing object bytes (everything past the property
// block). Decompress mutates prev so the caller can keep using the
// same *State for subsequent chunks in the group.
func Decompress(payload []byte, prev *State, moov *mp4.MoovBox) (*mp4.MoofBox, *State, error) {
	if prev == nil {
		prev = NewState()
	}
	if len(payload) == 0 {
		return nil, prev, fmt.Errorf("locmafv02: empty payload")
	}

	headerID, n, err := quicvarint.Parse(payload)
	if err != nil {
		return nil, prev, fmt.Errorf("locmafv02: invalid header_id")
	}
	pos := n
	propsLen, n, err := quicvarint.Parse(payload[pos:])
	if err != nil {
		return nil, prev, fmt.Errorf("locmafv02: invalid properties_length")
	}
	pos += n
	// Compare in uint64 space: int(propsLen) can wrap negative on a
	// corrupt/adversarial object and slip past a signed bounds check.
	if propsLen > uint64(len(payload)-pos) {
		return nil, prev, fmt.Errorf("locmafv02: properties exceed payload")
	}
	propsBytes := payload[pos : pos+int(propsLen)]
	mdatLen := len(payload) - (pos + int(propsLen))

	switch headerID {
	case LocmafFullHeaderID:
		props, err := parseFullProperties(propsBytes)
		if err != nil {
			return nil, prev, err
		}
		moofOut, err := reconstructMoof(props, 0, moov, mdatLen)
		if err != nil {
			return nil, prev, err
		}
		prev.Reset()
		prev.hasAny = true
		storeChunkValues(prev, props)
		return moofOut, prev, nil
	case LocmafDeltaHeaderID:
		if !prev.hasAny {
			return nil, prev, fmt.Errorf("locmafv02: delta header before any full header")
		}
		props, err := parseDeltaProperties(propsBytes, prev, moov)
		if err != nil {
			return nil, prev, err
		}
		moofOut, err := reconstructMoof(props, 0, moov, mdatLen)
		if err != nil {
			return nil, prev, err
		}
		storeChunkValues(prev, props)
		return moofOut, prev, nil
	default:
		slog.Warn("locmafv02: skipping unknown header", "id", headerID, "propertiesLength", propsLen)
		return nil, prev, nil
	}
}

// storeChunkValues copies cv into the prev state. State already has
// nil-safe maps thanks to NewState/Reset.
func storeChunkValues(prev *State, cv *chunkValues) {
	// Replace wholesale — every chunk re-emits every field that
	// matters, and unmentioned fields drop out either by absence
	// (full) or by the deletion marker (delta).
	prev.scalars = make(map[fieldID]uint64, len(cv.scalars))
	for k, v := range cv.scalars {
		prev.scalars[k] = v
	}
	prev.scalarsSigned = make(map[fieldID]int64, len(cv.signed))
	for k, v := range cv.signed {
		prev.scalarsSigned[k] = v
	}
	prev.lists = make(map[fieldID][]uint64, len(cv.lists))
	for k, v := range cv.lists {
		prev.lists[k] = append([]uint64(nil), v...)
	}
	prev.signedLists = make(map[fieldID][]int64, len(cv.signedLists))
	for k, v := range cv.signedLists {
		prev.signedLists[k] = append([]int64(nil), v...)
	}
	prev.rawBlobs = make(map[fieldID][]byte, len(cv.rawBlobs))
	for k, v := range cv.rawBlobs {
		prev.rawBlobs[k] = append([]byte(nil), v...)
	}
}

// encodeFullProperties emits the properties block of a
// LocmafFullHeader: each field as an absolute MOQT-varint or
// length-prefixed bytes, sorted by ID for deterministic output.
func encodeFullProperties(cv *chunkValues) []byte {
	type entry struct {
		id      fieldID
		payload []byte
	}
	var entries []entry
	appendScalar := func(id fieldID, v uint64) {
		entries = append(entries, entry{id, appendVarint(nil, v)})
	}
	appendList := func(id fieldID, payload []byte) {
		entries = append(entries, entry{id, payload})
	}

	for id, v := range cv.scalars {
		appendScalar(id, v)
	}
	for id, list := range cv.lists {
		var payload []byte
		for _, v := range list {
			payload = appendVarint(payload, v)
		}
		appendList(id, payload)
	}
	for id, list := range cv.signedLists {
		var payload []byte
		for _, v := range list {
			payload = appendZigzag(payload, v)
		}
		appendList(id, payload)
	}
	for id, blob := range cv.rawBlobs {
		appendList(id, blob)
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].id < entries[j].id })
	var out []byte
	for _, e := range entries {
		out = appendVarint(out, uint64(e.id))
		if e.id.isList() {
			out = appendVarint(out, uint64(len(e.payload)))
		}
		out = append(out, e.payload...)
	}
	return out
}

// encodeDeltaProperties emits the properties block of a
// LocmafDeltaHeader: each emitted field carries the difference vs the
// in-group reference, and a deletion marker lists fields that
// disappeared since the previous chunk.
func encodeDeltaProperties(cv *chunkValues, prev *State, moov *mp4.MoovBox) ([]byte, error) {
	type entry struct {
		id      fieldID
		payload []byte
		isList  bool
	}
	var entries []entry
	addBytes := func(id fieldID, payload []byte, list bool) {
		entries = append(entries, entry{id, payload, list})
	}

	// Scalars: zigzag(current - previous). idTfdtBaseMediaDecodeTime is
	// special-cased — see encodeBMDTDelta.
	for id, v := range cv.scalars {
		if id == idTfdtBaseMediaDecodeTime {
			data, emit, err := encodeBMDTDelta(v, prev, moov)
			if err != nil {
				return nil, err
			}
			if !emit {
				continue
			}
			addBytes(id, data, false)
			continue
		}
		prevV, prevHas := prev.scalars[id]
		if prevHas && prevV == v {
			continue
		}
		var delta int64
		if prevHas {
			delta = int64(v) - int64(prevV)
		} else {
			delta = int64(v)
		}
		addBytes(id, appendZigzag(nil, delta), false)
	}

	// Varint lists: per-element zigzag delta with the list-length
	// rule from draft §16.1.
	for id, list := range cv.lists {
		prevList := prev.lists[id]
		if equalUint64(list, prevList) {
			continue
		}
		var payload []byte
		for i, v := range list {
			var prevV uint64
			if i < len(prevList) {
				prevV = prevList[i]
			}
			payload = appendZigzag(payload, int64(v)-int64(prevV))
		}
		addBytes(id, payload, true)
	}
	for id, list := range cv.signedLists {
		prevList := prev.signedLists[id]
		if equalInt64(list, prevList) {
			continue
		}
		var payload []byte
		for i, v := range list {
			var prevV int64
			if i < len(prevList) {
				prevV = prevList[i]
			}
			payload = appendZigzag(payload, v-prevV)
		}
		addBytes(id, payload, true)
	}

	// Raw blobs: full new bytes verbatim.
	for id, blob := range cv.rawBlobs {
		prevBlob := prev.rawBlobs[id]
		if equalBytes(blob, prevBlob) {
			continue
		}
		addBytes(id, append([]byte(nil), blob...), true)
	}

	// Deletion marker: fields that were in prev but are missing now.
	var deleted []uint64
	collectMissing := func(seen map[fieldID]struct{}) {
		for id := range prev.scalars {
			if _, ok := seen[id]; !ok {
				deleted = append(deleted, uint64(id))
			}
		}
		for id := range prev.lists {
			if _, ok := seen[id]; !ok {
				deleted = append(deleted, uint64(id))
			}
		}
		for id := range prev.signedLists {
			if _, ok := seen[id]; !ok {
				deleted = append(deleted, uint64(id))
			}
		}
		for id := range prev.rawBlobs {
			if _, ok := seen[id]; !ok {
				deleted = append(deleted, uint64(id))
			}
		}
	}
	currentIDs := make(map[fieldID]struct{})
	for id := range cv.scalars {
		currentIDs[id] = struct{}{}
	}
	for id := range cv.lists {
		currentIDs[id] = struct{}{}
	}
	for id := range cv.signedLists {
		currentIDs[id] = struct{}{}
	}
	for id := range cv.rawBlobs {
		currentIDs[id] = struct{}{}
	}
	collectMissing(currentIDs)
	if len(deleted) > 0 {
		sort.Slice(deleted, func(i, j int) bool { return deleted[i] < deleted[j] })
		var payload []byte
		for _, id := range deleted {
			payload = appendVarint(payload, id)
		}
		addBytes(idDeltaDeletedLocmafIDs, payload, true)
	}

	sort.Slice(entries, func(i, j int) bool { return entries[i].id < entries[j].id })
	var out []byte
	for _, e := range entries {
		out = appendVarint(out, uint64(e.id))
		if e.id.isList() {
			out = appendVarint(out, uint64(len(e.payload)))
		}
		out = append(out, e.payload...)
	}
	return out, nil
}

// encodeBMDTDelta implements the draft §16.2 special case:
// idTfdtBaseMediaDecodeTime is normally derived as
// previous_bmdt + sum(previous_sample_durations); the encoder emits
// it (absolutely, as a plain MOQT varint, like in a full chunk) only
// when that derivation does not match the actual current BMDT.
func encodeBMDTDelta(currentBMDT uint64, prev *State, moov *mp4.MoovBox) ([]byte, bool, error) {
	derived, ok := deriveNextBMDT(prev, moov)
	if ok && derived == currentBMDT {
		return nil, false, nil
	}
	// Absolute MOQT varint, NOT zigzag — same encoding as in a full
	// chunk.
	return appendVarint(nil, currentBMDT), true, nil
}

func deriveNextBMDT(prev *State, moov *mp4.MoovBox) (uint64, bool) {
	bmdt, ok := prev.scalars[idTfdtBaseMediaDecodeTime]
	if !ok {
		return 0, false
	}
	dur, ok := computePrevDuration(prev, moov)
	if !ok {
		return 0, false
	}
	if bmdt > ^uint64(0)-dur {
		return 0, false
	}
	return bmdt + dur, true
}

func computePrevDuration(prev *State, moov *mp4.MoovBox) (uint64, bool) {
	sampleCount, ok := prev.scalars[idTrunSampleCount]
	if !ok {
		return 0, false
	}
	if durs, ok := prev.lists[idTrunSampleDurations]; ok {
		if uint64(len(durs)) != sampleCount {
			return 0, false
		}
		var total uint64
		for _, d := range durs {
			total += d
		}
		return total, true
	}
	defaultDur, ok := prev.scalars[idTfhdDefaultSampleDuration]
	if !ok {
		if moov == nil || moov.Mvex == nil || moov.Mvex.Trex == nil {
			return 0, false
		}
		defaultDur = uint64(moov.Mvex.Trex.DefaultSampleDuration)
	}
	return defaultDur * sampleCount, true
}

func equalUint64(a, b []uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalInt64(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
