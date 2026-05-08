package internal

import (
	"bytes"
	"testing"

	"github.com/Eyevinn/mp4ff/bits"
	"github.com/Eyevinn/mp4ff/mp4"
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
	_, hasBaseMediaDecodeTime := deltaFields[moofLocmafIDs.baseMediaDecodeTime]
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
		moofLocmafIDs.defaultSampleDuration: appendVarint(nil, 3),
		moofLocmafIDs.sampleSizes: append(
			appendVarint(nil, 10),
			appendVarint(nil, 4)...,
		),
	}
	previous := map[locmafID][]byte{
		moofLocmafIDs.defaultSampleDuration: appendVarint(nil, 5),
		moofLocmafIDs.sampleSizes: append(
			appendVarint(nil, 7),
			appendVarint(nil, 6)...,
		),
	}

	deltaFields, err := diffMoofFields(current, previous)
	require.NoError(t, err)

	durationDelta, ok, err := readSignedVarintList(
		moofLocmafIDs.defaultSampleDuration,
		deltaFields,
	)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, []int64{-2}, durationDelta)

	sizeDeltas, ok, err := readSignedVarintList(moofLocmafIDs.sampleSizes, deltaFields)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, []int64{3, -2}, sizeDeltas)
}

func TestMoofDeltaInitializationVectorUsesRawBytes(t *testing.T) {
	current := []byte{0x00, 0x40, 0x80, 0xff}
	previous := []byte{0x01, 0x02, 0x03, 0x04}

	delta, err := diffMoofFieldValue(moofLocmafIDs.initializationVector, current, previous)
	require.NoError(t, err)
	require.Equal(t, current, delta)

	applied, err := applyMoofFieldDelta(moofLocmafIDs.initializationVector, delta, previous)
	require.NoError(t, err)
	require.Equal(t, current, applied)
}

func TestMoofDeltaDeletedFieldsUseUnsignedVarints(t *testing.T) {
	deltaFields, err := diffMoofFields(
		map[locmafID][]byte{},
		map[locmafID][]byte{
			moofLocmafIDs.defaultSampleDuration: appendVarint(nil, 5),
		},
	)
	require.NoError(t, err)

	deletedFields, ok, err := readVarintList(moofDeltaDeletedLocmafIDs, deltaFields)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, []uint64{uint64(moofLocmafIDs.defaultSampleDuration)}, deletedFields)
}

func TestDeriveNextBaseMediaDecodeTimeUsesDefaultSampleDuration(t *testing.T) {
	previous := map[locmafID][]byte{
		moofLocmafIDs.baseMediaDecodeTime: appendVarint(nil, 100),
		moofLocmafIDs.sampleCount:         appendVarint(nil, 3),
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
		Senc:     senc,
		Children: []mp4.Box{senc},
	}
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
	require.Equal(t, append(append([]byte(nil), iv0...), iv1...), fields[moofLocmafIDs.initializationVector])
}

func TestReconstructSencReadsRawInitializationVectorBytes(t *testing.T) {
	iv0 := []byte{0x00, 0x40, 0x80, 0xff, 0x01, 0x41, 0x81, 0xfe, 0x02, 0x42, 0x82, 0xfd, 0x03, 0x43, 0x83, 0xfc}
	iv1 := []byte{0x10, 0x50, 0x90, 0xef, 0x11, 0x51, 0x91, 0xee, 0x12, 0x52, 0x92, 0xed, 0x13, 0x53, 0x93, 0xec}
	fields := map[locmafID][]byte{
		moofLocmafIDs.initializationVector: append(append([]byte(nil), iv0...), iv1...),
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

	sampleCountValue, ok := readVarint(moofLocmafIDs.sampleCount, fields)
	require.True(t, ok)
	require.EqualValues(t, 1, sampleCountValue)
	_, hasSampleSizes := fields[moofLocmafIDs.sampleSizes]
	require.False(t, hasSampleSizes)
	_, hasDefaultSampleSize := fields[moofLocmafIDs.defaultSampleSize]
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

	sampleCountValue, ok := readVarint(moofLocmafIDs.sampleCount, fields)
	require.True(t, ok)
	require.EqualValues(t, 2, sampleCountValue)
	sampleSizes, hasSampleSizes, err := readVarintList(moofLocmafIDs.sampleSizes, fields)
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
	delete(fields, moofLocmafIDs.sampleCompositionTimeOffsets)

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
