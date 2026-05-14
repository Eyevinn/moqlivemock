package internal

import (
	"bytes"
	"testing"

	"github.com/Eyevinn/mp4ff/bits"
	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/quic-go/quic-go/quicvarint"
	"github.com/stretchr/testify/require"
)

func TestMoofDeltaCompressorRoundTrip(t *testing.T) {
	track, moov := loadVideoTrack(t)
	compressor := &MoofDeltaCompressor{}
	object0, moof0 := mustCompressedObject(t, track, 0, 0, 2, compressor)
	object1, moof1 := mustCompressedObject(t, track, 1, 2, 4, compressor)

	parsedObject1, err := parseLocmafObject(object1)
	require.NoError(t, err)
	require.EqualValues(t, MoofDeltaHeader, parsedObject1.headerID)
	deltaFields, err := separateFields(parsedObject1.properties)
	require.NoError(t, err)
	_, hasBaseMediaDecodeTime := deltaFields[moofBaseMediaDecodeTime]
	require.False(t, hasBaseMediaDecodeTime)

	decoder := &MoofDeltaDecompressor{}
	rebuilt0, _, err := decoder.DecompressMoof(object0, 0, moov)
	require.NoError(t, err)
	rebuilt1, _, err := decoder.DecompressMoof(object1, 1, moov)
	require.NoError(t, err)

	requireCompressedMoofEqual(t, moof0, rebuilt0, moov)
	requireCompressedMoofEqual(t, moof1, rebuilt1, moov)
}

func TestMoofDeltaConverterKeepsState(t *testing.T) {
	track, moov := loadVideoTrack(t)
	compressor := &MoofDeltaCompressor{}
	object0, moof0 := mustCompressedObject(t, track, 0, 0, 2, compressor)
	object1, moof1 := mustCompressedObject(t, track, 1, 2, 4, compressor)

	converter := &MoofDeltaDecompressor{}
	frag0, err := convertLocmafObjectToCMAF(t, object0, 0, moov, converter)
	require.NoError(t, err)
	frag1, err := convertLocmafObjectToCMAF(t, object1, 1, moov, converter)
	require.NoError(t, err)

	requireCompressedMoofEqual(t, moof0, frag0.Moof, moov)
	requireCompressedMoofEqual(t, moof1, frag1.Moof, moov)
}

func TestMoofDeltaRequiresPreviousMoof(t *testing.T) {
	track, moov := loadVideoTrack(t)
	compressor := &MoofDeltaCompressor{}
	_, _ = mustCompressedObject(t, track, 0, 0, 2, compressor)
	object1, _ := mustCompressedObject(t, track, 1, 2, 4, compressor)

	decoder := &MoofDeltaDecompressor{}
	_, _, err := decoder.DecompressMoof(object1, 1, moov)
	require.ErrorContains(t, err, "missing previous moof state")
}

// Forward-compat: a future LOCMAF revision may introduce additional
// top-level objects (e.g. prft, sidx). Existing receivers should log and
// skip such objects without erroring, so they can stream alongside the
// known moof/moof-delta objects.
func TestDecompressMoofSkipsUnknownObjects(t *testing.T) {
	_, moov := loadVideoTrack(t)
	const unknownHeaderID locmafPropertyID = 99
	payload := encodeFields(map[locmafID][]byte{1: {0xab, 0xcd}})
	object := createSizedLocmafProperty(unknownHeaderID, payload)

	decoder := &MoofDeltaDecompressor{}
	moof, mdat, err := decoder.DecompressMoof(object, 0, moov)
	require.NoError(t, err)
	require.Nil(t, moof)
	require.Nil(t, mdat)
}

// A delta moof normally omits moofBaseMediaDecodeTime; the receiver derives
// it from the previous moof's state. When the actual BMDT diverges (a
// discontinuity such as a splice or stream-tear), the encoder must emit
// moofBaseMediaDecodeTime as an absolute value in the delta moof and the
// decoder must use that value verbatim instead of the derivation.
func TestMoofDeltaEmitsAbsoluteBMDTOnDiscontinuity(t *testing.T) {
	_, moov := loadVideoTrack(t)

	// Previous moof: 2 samples × 512 ticks → contiguous next BMDT = 1024.
	previous := map[locmafID][]byte{
		moofBaseMediaDecodeTime:   appendVarint(nil, 0),
		moofSampleCount:           appendVarint(nil, 2),
		moofDefaultSampleDuration: appendVarint(nil, 512),
	}

	// Contiguous current (BMDT = 1024) should leave BMDT out of the delta.
	contiguous := cloneFieldValues(previous)
	contiguous[moofBaseMediaDecodeTime] = appendVarint(nil, 1024)
	deltaContiguous, err := diffMoofFields(contiguous, previous, moov)
	require.NoError(t, err)
	require.NotContains(t, deltaContiguous, moofBaseMediaDecodeTime,
		"contiguous BMDT should not appear in delta")

	// Discontinuous current (BMDT = 999999) should be carried as absolute.
	discontinuous := cloneFieldValues(previous)
	discontinuous[moofBaseMediaDecodeTime] = appendVarint(nil, 999999)
	deltaDiscontinuous, err := diffMoofFields(discontinuous, previous, moov)
	require.NoError(t, err)
	require.Contains(t, deltaDiscontinuous, moofBaseMediaDecodeTime,
		"discontinuous BMDT should appear in delta")
	emitted, _, err := quicvarint.Parse(deltaDiscontinuous[moofBaseMediaDecodeTime])
	require.NoError(t, err)
	require.EqualValues(t, 999999, emitted,
		"absolute BMDT must be encoded as an unsigned varint, not a delta")

	// Round-trip via applyMoofDelta restores the absolute value.
	applied, err := applyMoofDelta(previous, deltaDiscontinuous, moov)
	require.NoError(t, err)
	restored, _, err := quicvarint.Parse(applied[moofBaseMediaDecodeTime])
	require.NoError(t, err)
	require.EqualValues(t, 999999, restored)
}

func TestMoofDeltaAllowsEmptyDeltaPayload(t *testing.T) {
	track, moov := loadVideoTrack(t)
	compressor := &MoofDeltaCompressor{}
	object0, moof := mustCompressedObject(t, track, 0, 0, 2, compressor)
	parsedObject0, err := parseLocmafObject(object0)
	require.NoError(t, err)

	object1 := append(
		createSizedLocmafProperty(MoofDeltaHeader, nil),
		parsedObject0.mdatPayload...,
	)

	decoder := &MoofDeltaDecompressor{}
	_, _, err = decoder.DecompressMoof(object0, 0, moov)
	require.NoError(t, err)
	rebuilt, _, err := decoder.DecompressMoof(object1, 1, moov)
	require.NoError(t, err)

	expectedBaseMediaDecodeTime := moof.Traf.Tfdt.BaseMediaDecodeTime()
	for _, sample := range moof.Traf.Trun.Samples {
		expectedBaseMediaDecodeTime += uint64(sample.Dur)
	}
	require.Equal(t, expectedBaseMediaDecodeTime, rebuilt.Traf.Tfdt.BaseMediaDecodeTime())
}

func TestMoofDeltaFieldsUseSignedVarints(t *testing.T) {
	current := map[locmafID][]byte{
		moofDefaultSampleDuration: appendVarint(nil, 3),
		moofSampleSizes: append(
			appendVarint(nil, 10),
			appendVarint(nil, 4)...,
		),
	}
	previous := map[locmafID][]byte{
		moofDefaultSampleDuration: appendVarint(nil, 5),
		moofSampleSizes: append(
			appendVarint(nil, 7),
			appendVarint(nil, 6)...,
		),
	}

	deltaFields, err := diffMoofFields(current, previous, nil)
	require.NoError(t, err)

	durationDelta, ok, err := readSignedVarintList(
		moofDefaultSampleDuration,
		deltaFields,
	)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, []int64{-2}, durationDelta)

	sizeDeltas, ok, err := readSignedVarintList(moofSampleSizes, deltaFields)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, []int64{3, -2}, sizeDeltas)
}

func TestMoofDeltaInitializationVectorUsesRawBytes(t *testing.T) {
	current := []byte{0x00, 0x40, 0x80, 0xff}
	previous := []byte{0x01, 0x02, 0x03, 0x04}

	delta, err := diffMoofFieldValue(moofInitializationVector, current, previous)
	require.NoError(t, err)
	require.Equal(t, current, delta)

	applied, err := applyMoofFieldDelta(moofInitializationVector, delta, previous)
	require.NoError(t, err)
	require.Equal(t, current, applied)
}

func TestMoofDeltaDeletedFieldsUseUnsignedVarints(t *testing.T) {
	deltaFields, err := diffMoofFields(
		map[locmafID][]byte{},
		map[locmafID][]byte{
			moofDefaultSampleDuration: appendVarint(nil, 5),
		},
		nil,
	)
	require.NoError(t, err)

	deletedFields, ok, err := readVarintList(moofDeltaDeletedLocmafIDs, deltaFields)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, []uint64{uint64(moofDefaultSampleDuration)}, deletedFields)
}

func TestDeriveNextBaseMediaDecodeTimeUsesDefaultSampleDuration(t *testing.T) {
	previous := map[locmafID][]byte{
		moofBaseMediaDecodeTime: appendVarint(nil, 100),
		moofSampleCount:         appendVarint(nil, 3),
	}
	moov := &mp4.MoovBox{
		Mvex: &mp4.MvexBox{
			Trex: &mp4.TrexBox{
				DefaultSampleDuration: 40,
			},
		},
	}

	baseMediaDecodeTime, err := deriveNextBaseMediaDecodeTime(previous, moov)
	require.NoError(t, err)
	require.EqualValues(t, 220, baseMediaDecodeTime)
}

func TestCompressMoofEncodesInitializationVectorsAsRawBytes(t *testing.T) {
	iv0 := []byte{0x00, 0x40, 0x80, 0xff, 0x01, 0x41, 0x81, 0xfe, 0x02, 0x42, 0x82, 0xfd, 0x03, 0x43, 0x83, 0xfc}
	iv1 := []byte{0x10, 0x50, 0x90, 0xef, 0x11, 0x51, 0x91, 0xee, 0x12, 0x52, 0x92, 0xed, 0x13, 0x53, 0x93, 0xec}
	senc := mp4.CreateSencBox()
	require.NoError(t, senc.AddSample(mp4.SencSample{IV: iv0}))
	require.NoError(t, senc.AddSample(mp4.SencSample{IV: iv1}))

	traf := &mp4.TrafBox{
		Tfhd:     mp4.CreateTfhd(1),
		Tfdt:     mp4.CreateTfdt(0),
		Trun:     mp4.CreateTrun(0),
		Senc:     senc,
		Children: []mp4.Box{senc},
	}
	traf.Trun.AddSamples([]mp4.Sample{
		mp4.NewSample(0, 0, 0, 0),
		mp4.NewSample(0, 0, 0, 0),
	})
	moof := &mp4.MoofBox{Traf: traf}
	moov := &mp4.MoovBox{
		Mvex: &mp4.MvexBox{
			Trex: &mp4.TrexBox{
				DefaultSampleDescriptionIndex: 1,
			},
		},
	}

	fields, err := extractImportantMoofFields(moof, moov)
	require.NoError(t, err)
	require.Equal(t, append(append([]byte(nil), iv0...), iv1...), fields[moofInitializationVector])
}

func TestReconstructSencReadsRawInitializationVectorBytes(t *testing.T) {
	iv0 := []byte{0x00, 0x40, 0x80, 0xff, 0x01, 0x41, 0x81, 0xfe, 0x02, 0x42, 0x82, 0xfd, 0x03, 0x43, 0x83, 0xfc}
	iv1 := []byte{0x10, 0x50, 0x90, 0xef, 0x11, 0x51, 0x91, 0xee, 0x12, 0x52, 0x92, 0xed, 0x13, 0x53, 0x93, 0xec}
	fields := map[locmafID][]byte{
		moofInitializationVector: append(append([]byte(nil), iv0...), iv1...),
	}

	senc, err := reconstructSencFromFields(fields, 2, 16, false)
	require.NoError(t, err)
	require.NotNil(t, senc)
	require.Equal(t, byte(16), senc.PerSampleIVSize())
	require.Equal(t, []mp4.InitializationVector{mp4.InitializationVector(iv0), mp4.InitializationVector(iv1)}, senc.IVs)
}

func loadVideoTrack(t *testing.T) (*ContentTrack, *mp4.MoovBox) {
	t.Helper()

	asset, err := LoadAsset("../assets/test10s", 1, 1)
	require.NoError(t, err)

	var track *ContentTrack
	for groupIndex := range asset.Groups {
		for trackIndex := range asset.Groups[groupIndex].Tracks {
			candidate := &asset.Groups[groupIndex].Tracks[trackIndex]
			if candidate.ContentType == "video" {
				track = candidate
				break
			}
		}
		if track != nil {
			break
		}
	}
	require.NotNil(t, track)

	initData, err := track.SpecData.GenCMAFInitData()
	require.NoError(t, err)
	initFile, err := mp4.DecodeFileSR(bits.NewFixedSliceReader(initData))
	require.NoError(t, err)
	require.NotNil(t, initFile.Init)

	return track, initFile.Init.Moov
}

func mustChunk(t *testing.T, track *ContentTrack, chunkNr uint32, startNr, endNr uint64) []byte {
	t.Helper()

	chunk, err := track.GenCMAFChunk(chunkNr, startNr, endNr)
	require.NoError(t, err)
	return chunk
}

func mustCompressedObject(t *testing.T, track *ContentTrack, chunkNr uint32, startNr, endNr uint64,
	compressor *MoofDeltaCompressor) ([]byte, *mp4.MoofBox) {
	t.Helper()

	fragment, err := track.createFragment(chunkNr, startNr, endNr)
	require.NoError(t, err)

	if compressor == nil {
		compressor = &MoofDeltaCompressor{}
	}
	headerID, payload, err := compressor.CompressMoof(fragment.Moof, track.SpecData.GetInit().Moov)
	require.NoError(t, err)

	object := append(createSizedLocmafProperty(headerID, payload), fragment.Mdat.Data...)
	return object, fragment.Moof
}

func decodeMoof(t *testing.T, chunk []byte) *mp4.MoofBox {
	t.Helper()

	box, err := mp4.DecodeBox(0, bytes.NewReader(chunk))
	require.NoError(t, err)
	moof, ok := box.(*mp4.MoofBox)
	require.True(t, ok)
	return moof
}

func convertLocmafObjectToCMAF(t *testing.T, locmaf []byte, seqnum uint32, moov *mp4.MoovBox,
	decompressor *MoofDeltaDecompressor) (*mp4.Fragment, error) {
	t.Helper()

	moof, mdatPayload, err := decompressor.DecompressMoof(locmaf, seqnum, moov)
	if err != nil {
		return nil, err
	}

	frag := mp4.NewFragment()
	frag.Moof = moof
	frag.AddChild(&mp4.MdatBox{Data: mdatPayload})
	return frag, nil
}

func requireCompressedMoofEqual(t *testing.T, expected, actual *mp4.MoofBox, moov *mp4.MoovBox) {
	t.Helper()

	expectedPayload, err := CompressMoof(expected, moov)
	require.NoError(t, err)
	actualPayload, err := CompressMoof(actual, moov)
	require.NoError(t, err)
	require.Equal(t, expectedPayload, actualPayload)
}

func TestCompressMoofOmitsSampleSizesForSingleSampleFragment(t *testing.T) {
	track, moov := loadVideoTrack(t)
	compressor := &MoofDeltaCompressor{}
	object, moof := mustCompressedObject(t, track, 0, 0, 1, compressor)
	require.Len(t, moof.Traf.Trun.Samples, 1)

	parsedObject, err := parseLocmafObject(object)
	require.NoError(t, err)

	fields, err := separateFields(parsedObject.properties)
	require.NoError(t, err)

	sampleCountValue, ok := readVarint(moofSampleCount, fields)
	require.True(t, ok)
	require.EqualValues(t, 1, sampleCountValue)
	_, hasSampleSizes := fields[moofSampleSizes]
	require.False(t, hasSampleSizes)
	_, hasDefaultSampleSize := fields[moofDefaultSampleSize]
	require.False(t, hasDefaultSampleSize)

	rebuilt, err := DecompressMoof(object, 1, moov)
	require.NoError(t, err)
	requireCompressedMoofEqual(t, moof, rebuilt, moov)
}

func TestCompressMoofKeepsSampleSizesForMultiSampleFragment(t *testing.T) {
	track, moov := loadVideoTrack(t)
	moof := decodeMoof(t, mustChunk(t, track, 0, 0, 2))
	require.Len(t, moof.Traf.Trun.Samples, 2)

	payload, err := CompressMoof(moof, moov)
	require.NoError(t, err)

	fields, err := separateFields(payload)
	require.NoError(t, err)

	sampleCountValue, ok := readVarint(moofSampleCount, fields)
	require.True(t, ok)
	require.EqualValues(t, 2, sampleCountValue)
	sampleSizes, hasSampleSizes, err := readVarintList(moofSampleSizes, fields)
	require.NoError(t, err)
	require.True(t, hasSampleSizes)
	require.True(t, len(sampleSizes) == 2)
}

func TestDecompressMoofDefaultsMissingCompositionOffsetsToZero(t *testing.T) {
	track, moov := loadVideoTrack(t)
	compressor := &MoofDeltaCompressor{}
	object, _ := mustCompressedObject(t, track, 0, 0, 2, compressor)

	parsedObject, err := parseLocmafObject(object)
	require.NoError(t, err)

	fields, err := separateFields(parsedObject.properties)
	require.NoError(t, err)
	delete(fields, moofSampleCompositionTimeOffsets)

	modifiedObject := append(
		createSizedLocmafProperty(parsedObject.headerID, encodeFields(fields)),
		parsedObject.mdatPayload...,
	)

	rebuilt, err := DecompressMoof(modifiedObject, 1, moov)
	require.NoError(t, err)
	require.False(t, rebuilt.Traf.Trun.HasSampleCompositionTimeOffset())
	require.Len(t, rebuilt.Traf.Trun.Samples, 2)
	for _, sample := range rebuilt.Traf.Trun.Samples {
		require.Zero(t, sample.CompositionTimeOffset)
	}
}
