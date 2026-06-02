package locmafv02

// State carries the per-group reconstruction reference both compressor
// and decompressor consult. Callers retain it between calls inside one
// MoQ group and pass nil (or call Reset) when a new group begins.
//
// The state is keyed by field ID and stores the *decoded* values from
// the most recently emitted/reconstructed chunk. Encoded bytes are not
// retained — the codec re-encodes from these decoded values on every
// chunk, which lets the encoder and decoder share the same delta
// computation logic.
type State struct {
	// scalars holds the most recent uint64 value of each scalar
	// (even-ID) field. Absent keys mean the field was not present
	// in the previous chunk.
	scalars map[fieldID]uint64

	// scalarsSigned holds scalar fields whose semantic value is
	// already a signed offset (currently none in v0.2, kept for
	// future styp/emsg extensions).
	scalarsSigned map[fieldID]int64

	// lists holds varint-list fields as decoded unsigned slices.
	lists map[fieldID][]uint64

	// signedLists holds signed-varint lists (only
	// idTrunSampleCompositionTimeOffsets in v0.2).
	signedLists map[fieldID][]int64

	// rawBlobs holds opaque length-prefixed bytes
	// (idSencInitializationVector and the prft/styp/emsg blobs once
	// they land).
	rawBlobs map[fieldID][]byte

	// hasAny indicates the state holds a "previous" chunk;
	// determines full-vs-delta dispatch in Compress.
	hasAny bool
}

// NewState returns an empty State.
func NewState() *State {
	return &State{
		scalars:       make(map[fieldID]uint64),
		scalarsSigned: make(map[fieldID]int64),
		lists:         make(map[fieldID][]uint64),
		signedLists:   make(map[fieldID][]int64),
		rawBlobs:      make(map[fieldID][]byte),
	}
}

// Reset zeroes the State so the next chunk is encoded as a Full
// header (e.g. at the start of a new MoQ group). Provided as a
// convenience; callers may also just pass a fresh *State.
func (s *State) Reset() {
	*s = State{
		scalars:       make(map[fieldID]uint64),
		scalarsSigned: make(map[fieldID]int64),
		lists:         make(map[fieldID][]uint64),
		signedLists:   make(map[fieldID][]int64),
		rawBlobs:      make(map[fieldID][]byte),
	}
}
