package locmafv02

import (
	"bytes"
	"testing"

	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/quic-go/quic-go/quicvarint"
	"github.com/stretchr/testify/require"
)

// TestSampleFlagsPackingRoundTrip exercises every 5-bit transport value
// against the 32-bit ISO BMFF form so we know the bit layout matches
// draft §17.
func TestSampleFlagsPackingRoundTrip(t *testing.T) {
	for transport := uint64(0); transport < 32; transport++ {
		// Build the 32-bit form from the transport and verify pack
		// recovers the same transport.
		expanded := unpackSampleFlags(transport)
		packed, err := packSampleFlags(expanded)
		require.NoError(t, err)
		require.Equal(t, transport, packed,
			"5-bit transport %d round-tripped through 32-bit form (=0x%08x) gave %d",
			transport, expanded, packed)
	}
}

// TestSampleFlagsRejectsOutOfScope verifies that a sample_flags value
// touching bits LOCMAF v0.2 does not preserve raises an error rather
// than silently dropping the source signal.
func TestSampleFlagsRejectsOutOfScope(t *testing.T) {
	// Bit 25 (sample_has_redundancy) is outside the supported mask.
	_, err := packSampleFlags(0x02000000 | 0x00100000)
	require.Error(t, err)
}

// TestZigzagSpotChecks: the first few mappings from draft §12.4.
func TestZigzagSpotChecks(t *testing.T) {
	cases := []struct {
		in  int64
		out uint64
	}{
		{0, 0}, {-1, 1}, {1, 2}, {-2, 3}, {2, 4}, {-3, 5}, {3, 6},
	}
	for _, c := range cases {
		got := appendZigzag(nil, c.in)
		v, _, err := quicvarint.Parse(got)
		require.NoError(t, err)
		require.Equal(t, c.out, v, "encode %d", c.in)

		back, _, err := parseZigzag(got)
		require.NoError(t, err)
		require.Equal(t, c.in, back, "decode %d", c.in)
	}
}

// buildSyntheticMoov builds the smallest possible moov sufficient for
// the codec to work — a single video trak with a populated trex.
// Used by tests that don't need real media.
func buildSyntheticMoov(t *testing.T) *mp4.MoovBox {
	t.Helper()
	init := mp4.CreateEmptyInit()
	trak := init.AddEmptyTrack(90000, "video", "und")
	require.NotNil(t, trak)
	// Plain default trex values; the v0.2 codec only consults
	// these as fallback when a tfhd default is absent.
	init.Moov.Mvex.Trex.DefaultSampleDuration = 3000
	init.Moov.Mvex.Trex.DefaultSampleSize = 1000
	init.Moov.Mvex.Trex.DefaultSampleFlags = 0
	return init.Moov
}

// makeFragment builds a fragment with the given samples and returns
// its decoded moof box, ready for compression. flags param is per-
// sample 32-bit ISO BMFF sample_flags.
func makeFragment(t *testing.T, seqnum uint32, bmdt uint64, samples []mp4.FullSample) *mp4.MoofBox {
	t.Helper()
	f, err := mp4.CreateFragment(seqnum, 1)
	require.NoError(t, err)
	for _, s := range samples {
		f.AddFullSample(s)
	}
	f.Moof.Traf.Tfdt.SetBaseMediaDecodeTime(bmdt)
	f.EncOptimize = mp4.OptimizeTrun
	f.SetTrunDataOffsets()
	return f.Moof
}

// TestRoundTripFullChunk: compress a fragment as a Full header, parse
// it back, and verify sample-by-sample equivalence.
func TestRoundTripFullChunk(t *testing.T) {
	moov := buildSyntheticMoov(t)
	samples := []mp4.FullSample{
		{Sample: mp4.Sample{Dur: 3000, Size: 1500, Flags: 0x02000000}, Data: bytes.Repeat([]byte{0xAA}, 1500)},
		{Sample: mp4.Sample{Dur: 3000, Size: 1500, Flags: 0x01010000}, Data: bytes.Repeat([]byte{0xBB}, 1500)},
		{Sample: mp4.Sample{Dur: 3000, Size: 1500, Flags: 0x01010000}, Data: bytes.Repeat([]byte{0xCC}, 1500)},
	}
	moof := makeFragment(t, 1, 90000, samples)

	state := NewState()
	encoded, _, err := Compress(moof, state, moov)
	require.NoError(t, err)
	require.Greater(t, len(encoded), 0)

	// Sanity: first varint is the full header ID.
	id, _, err := quicvarint.Parse(encoded)
	require.NoError(t, err)
	require.Equal(t, LocmafFullHeaderID, id)

	rxState := NewState()
	moofOut, _, err := Decompress(encoded, rxState, moov)
	require.NoError(t, err)
	require.NotNil(t, moofOut)

	require.Equal(t, uint64(90000), moofOut.Traf.Tfdt.BaseMediaDecodeTime())
	require.Len(t, moofOut.Traf.Trun.Samples, len(samples))
	for i, want := range samples {
		got := moofOut.Traf.Trun.Samples[i]
		require.Equal(t, want.Dur, got.Dur, "sample %d duration", i)
		require.Equal(t, want.Size, got.Size, "sample %d size", i)
		require.Equal(t, want.Flags, got.Flags, "sample %d flags", i)
	}
}

// TestDeltaSequenceBMDTDerivation: full + several deltas with steady
// duration cadence; the receiver must derive BMDT from the previous
// chunk without seeing it on the wire.
func TestDeltaSequenceBMDTDerivation(t *testing.T) {
	moov := buildSyntheticMoov(t)
	dur := uint32(3000)
	mkSamples := func(n int, baseSize uint32) []mp4.FullSample {
		out := make([]mp4.FullSample, n)
		for i := range out {
			out[i] = mp4.FullSample{
				Sample: mp4.Sample{Dur: dur, Size: baseSize + uint32(i)*10,
					Flags: 0x01010000},
				Data: bytes.Repeat([]byte{0x55}, int(baseSize)+i*10),
			}
		}
		return out
	}

	state := NewState()
	rxState := NewState()
	bmdt := uint64(0)
	for i := 0; i < 5; i++ {
		samples := mkSamples(3, 1000+uint32(i)*100)
		moof := makeFragment(t, uint32(i+1), bmdt, samples)
		encoded, _, err := Compress(moof, state, moov)
		require.NoError(t, err)

		// Append synthetic mdat so Decompress can derive the last
		// sample's size per §15.1 sample-size-derivation.
		full := appendSyntheticMdat(encoded, samples)

		moofOut, _, err := Decompress(full, rxState, moov)
		require.NoError(t, err)
		require.NotNil(t, moofOut)
		require.Equal(t, bmdt, moofOut.Traf.Tfdt.BaseMediaDecodeTime(),
			"chunk %d BMDT", i)
		require.Len(t, moofOut.Traf.Trun.Samples, 3, "chunk %d sample count", i)
		bmdt += uint64(dur) * 3
	}
}

// appendSyntheticMdat returns encoded with a zero-filled mdat tail of
// length sum(samples[i].Size) appended. The decoder only inspects the
// tail's length to derive the last sample's size; the bytes themselves
// don't matter.
func appendSyntheticMdat(encoded []byte, samples []mp4.FullSample) []byte {
	var total int
	for _, s := range samples {
		total += int(s.Size)
	}
	out := make([]byte, 0, len(encoded)+total)
	out = append(out, encoded...)
	out = append(out, make([]byte, total)...)
	return out
}

// TestListLengthChangesGrowAndShrink: vary sample_count across deltas
// and verify the receiver pads/truncates per draft §16.1.
func TestListLengthChangesGrowAndShrink(t *testing.T) {
	moov := buildSyntheticMoov(t)
	dur := uint32(3000)
	mkSamples := func(n int) []mp4.FullSample {
		out := make([]mp4.FullSample, n)
		for i := range out {
			out[i] = mp4.FullSample{
				Sample: mp4.Sample{Dur: dur, Size: 1000 + uint32(i)*7,
					Flags: 0x01010000},
				Data: bytes.Repeat([]byte{0x33}, 1000+i*7),
			}
		}
		return out
	}

	state := NewState()
	rxState := NewState()
	var bmdt uint64
	counts := []int{2, 4, 1, 3}
	for i, n := range counts {
		samples := mkSamples(n)
		moof := makeFragment(t, uint32(i+1), bmdt, samples)
		encoded, _, err := Compress(moof, state, moov)
		require.NoError(t, err)
		// Append synthetic mdat so the decoder can derive the
		// single-sample size (n == 1) or the trailing sample size
		// of a variable-sized list (n > 1) per §15.1.
		full := appendSyntheticMdat(encoded, samples)

		moofOut, _, err := Decompress(full, rxState, moov)
		require.NoError(t, err)
		require.NotNil(t, moofOut)
		require.Len(t, moofOut.Traf.Trun.Samples, n, "chunk %d sample count", i)
		for j, want := range samples {
			got := moofOut.Traf.Trun.Samples[j]
			require.Equal(t, want.Size, got.Size, "chunk %d sample %d size", i, j)
			require.Equal(t, want.Flags, got.Flags, "chunk %d sample %d flags", i, j)
			require.Equal(t, want.Dur, got.Dur, "chunk %d sample %d dur", i, j)
		}
		bmdt += uint64(dur) * uint64(n)
	}
}

// TestFirstSampleFlagsDeletion: a SAP-1 chunk emits firstSampleFlags;
// the next chunk must explicitly delete it.
func TestFirstSampleFlagsDeletion(t *testing.T) {
	moov := buildSyntheticMoov(t)
	dur := uint32(3000)
	idrSample := mp4.FullSample{
		Sample: mp4.Sample{Dur: dur, Size: 2000, Flags: 0x02000000},
		Data:   bytes.Repeat([]byte{0xAA}, 2000),
	}
	pSample := mp4.FullSample{
		Sample: mp4.Sample{Dur: dur, Size: 1000, Flags: 0x01010000},
		Data:   bytes.Repeat([]byte{0xBB}, 1000),
	}

	// Full chunk: IDR + P with explicit first-sample-flags. We
	// drive the trun directly so the encoder sees a non-empty
	// FirstSampleFlags regardless of mp4ff's OptimizeTrun
	// heuristic.
	f, err := mp4.CreateFragment(1, 1)
	require.NoError(t, err)
	f.AddFullSample(idrSample)
	f.AddFullSample(pSample)
	f.AddFullSample(pSample)
	f.Moof.Traf.Tfdt.SetBaseMediaDecodeTime(0)
	f.Moof.Traf.Trun.SetFirstSampleFlags(idrSample.Flags)
	// Promote the P-frame flags into a tfhd default and drop the
	// per-sample flags so first_sample_flags becomes the only way
	// to signal "this first sample differs".
	f.Moof.Traf.Tfhd.DefaultSampleFlags = pSample.Flags
	f.Moof.Traf.Tfhd.Flags |= mp4.TfhdDefaultSampleFlagsPresentFlag
	f.Moof.Traf.Trun.Flags &^= mp4.TrunSampleFlagsPresentFlag

	state := NewState()
	encoded1, _, err := Compress(f.Moof, state, moov)
	require.NoError(t, err)

	// Sanity: confirm firstSampleFlags was emitted on the full.
	headerID, n, err := quicvarint.Parse(encoded1)
	require.NoError(t, err)
	require.Equal(t, LocmafFullHeaderID, headerID)
	propsLen, n2, err := quicvarint.Parse(encoded1[n:])
	require.NoError(t, err)
	propsStart := n + n2
	props := encoded1[propsStart : propsStart+int(propsLen)]
	raw, err := rawProperties(props)
	require.NoError(t, err)
	_, sawFirstFlags := raw[idTrunFirstSampleFlags]
	require.True(t, sawFirstFlags, "full chunk should carry firstSampleFlags")

	// Next chunk: all P samples, no firstSampleFlags.
	f2, err := mp4.CreateFragment(2, 1)
	require.NoError(t, err)
	f2.AddFullSample(pSample)
	f2.AddFullSample(pSample)
	f2.AddFullSample(pSample)
	f2.Moof.Traf.Tfdt.SetBaseMediaDecodeTime(uint64(dur) * 3)
	f2.Moof.Traf.Tfhd.DefaultSampleFlags = pSample.Flags
	f2.Moof.Traf.Tfhd.Flags |= mp4.TfhdDefaultSampleFlagsPresentFlag
	f2.Moof.Traf.Trun.Flags &^= mp4.TrunSampleFlagsPresentFlag

	encoded2, _, err := Compress(f2.Moof, state, moov)
	require.NoError(t, err)

	// Delta must carry the deletion marker for firstSampleFlags.
	_, n, err = quicvarint.Parse(encoded2)
	require.NoError(t, err)
	pl, n2, err := quicvarint.Parse(encoded2[n:])
	require.NoError(t, err)
	deltaProps := encoded2[n+n2 : n+n2+int(pl)]
	raw, err = rawProperties(deltaProps)
	require.NoError(t, err)
	delBytes, ok := raw[idDeltaDeletedLocmafIDs]
	require.True(t, ok, "delta should emit deletion marker")
	deleted, err := parseVarintList(delBytes)
	require.NoError(t, err)
	require.Contains(t, deleted, uint64(idTrunFirstSampleFlags))

	// Decode and verify the reconstructed P samples don't carry
	// a sticky firstSampleFlags. Append synthetic mdat tails (the
	// variable-sized chunk 1 needs them for §15.1 derivation; the
	// uniform-size chunk 2 doesn't, but the helper is harmless).
	rxState := NewState()
	full1 := appendSyntheticMdat(encoded1, []mp4.FullSample{idrSample, pSample, pSample})
	full2 := appendSyntheticMdat(encoded2, []mp4.FullSample{pSample, pSample, pSample})
	_, _, err = Decompress(full1, rxState, moov)
	require.NoError(t, err)
	moof2, _, err := Decompress(full2, rxState, moov)
	require.NoError(t, err)
	require.NotNil(t, moof2)
	flags, present := moof2.Traf.Trun.FirstSampleFlags()
	require.False(t, present, "decoded delta moof should not carry first_sample_flags (got 0x%08x)", flags)
}

// TestUnknownHeaderIsSkipped: future LOCMAF revisions may add new
// top-level objects; existing receivers must skip them.
func TestUnknownHeaderIsSkipped(t *testing.T) {
	moov := buildSyntheticMoov(t)
	// Craft an object with header_id = 99, empty properties.
	obj := appendVarint(nil, 99)
	obj = appendVarint(obj, 0)
	state := NewState()
	moof, _, err := Decompress(obj, state, moov)
	require.NoError(t, err)
	require.Nil(t, moof)
}

// TestDeltaWithoutFullErrors: a delta header with no prior full
// header in the same group is a protocol violation.
func TestDeltaWithoutFullErrors(t *testing.T) {
	moov := buildSyntheticMoov(t)
	obj := appendVarint(nil, LocmafDeltaHeaderID)
	obj = appendVarint(obj, 0)
	state := NewState()
	_, _, err := Decompress(obj, state, moov)
	require.Error(t, err)
}
