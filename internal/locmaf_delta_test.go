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
	var compressor MoofDeltaCompressor
	object0, moof0 := mustCompressedObject(t, track, 0, 0, 2, &compressor)
	object1, moof1 := mustCompressedObject(t, track, 1, 2, 4, &compressor)

	var decoder MoofDeltaDecompressor
	rebuilt0, err := decoder.DecompressMoof(object0, 0, moov)
	require.NoError(t, err)
	rebuilt1, err := decoder.DecompressMoof(object1, 1, moov)
	require.NoError(t, err)

	requireCompressedMoofEqual(t, moof0, rebuilt0, moov)
	requireCompressedMoofEqual(t, moof1, rebuilt1, moov)
}

func TestMoofDeltaConverterKeepsState(t *testing.T) {
	track, moov := loadVideoTrack(t)
	var compressor MoofDeltaCompressor
	object0, moof0 := mustCompressedObject(t, track, 0, 0, 2, &compressor)
	object1, moof1 := mustCompressedObject(t, track, 1, 2, 4, &compressor)

	var converter MoofDeltaDecompressor
	frag0, err := convertLocmafObjectToCMAF(t, object0, 0, moov, &converter)
	require.NoError(t, err)
	frag1, err := convertLocmafObjectToCMAF(t, object1, 1, moov, &converter)
	require.NoError(t, err)

	requireCompressedMoofEqual(t, moof0, frag0.Moof, moov)
	requireCompressedMoofEqual(t, moof1, frag1.Moof, moov)
}

func TestMoofDeltaRequiresPreviousMoof(t *testing.T) {
	track, moov := loadVideoTrack(t)
	var compressor MoofDeltaCompressor
	_, _ = mustCompressedObject(t, track, 0, 0, 2, &compressor)
	object1, _ := mustCompressedObject(t, track, 1, 2, 4, &compressor)

	var decoder MoofDeltaDecompressor
	_, err := decoder.DecompressMoof(object1, 1, moov)
	require.ErrorContains(t, err, "missing previous moof state")
}

func TestMoofDeltaAllowsEmptyDeltaPayload(t *testing.T) {
	track, moov := loadVideoTrack(t)
	var compressor MoofDeltaCompressor
	object0, moof := mustCompressedObject(t, track, 0, 0, 2, &compressor)
	object1, _ := mustCompressedObject(t, track, 0, 0, 2, &compressor)

	parsedObject1, err := parseLocmafObject(object1)
	require.NoError(t, err)
	require.EqualValues(t, MoofDeltaHeader, parsedObject1.headerID)
	require.Empty(t, parsedObject1.properties)

	var decoder MoofDeltaDecompressor
	_, err = decoder.DecompressMoof(object0, 0, moov)
	require.NoError(t, err)
	rebuilt, err := decoder.DecompressMoof(object1, 1, moov)
	require.NoError(t, err)

	requireCompressedMoofEqual(t, moof, rebuilt, moov)
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

	object, err := parseLocmafObject(locmaf)
	if err != nil {
		return nil, err
	}
	moof, err := decompressor.DecompressMoof(locmaf, seqnum, moov)
	if err != nil {
		return nil, err
	}

	frag := mp4.NewFragment()
	frag.Moof = moof
	frag.AddChild(&mp4.MdatBox{Data: object.mdatPayload})
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
	object, moof := mustCompressedObject(t, track, 0, 0, 1, &MoofDeltaCompressor{})
	require.Len(t, moof.Traf.Trun.Samples, 1)

	parsedObject, err := parseLocmafObject(object)
	require.NoError(t, err)

	fields, err := separateFields(parsedObject.properties)
	require.NoError(t, err)

	sampleCountValue, ok := readVarint(moofLocmafIDs.SampleCount, fields)
	require.True(t, ok)
	require.EqualValues(t, 1, sampleCountValue)
	_, hasSampleSizes := fields[moofLocmafIDs.SampleSizes]
	require.False(t, hasSampleSizes)
	_, hasDefaultSampleSize := fields[moofLocmafIDs.DefaultSampleSize]
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

	sampleCountValue, ok := readVarint(moofLocmafIDs.SampleCount, fields)
	require.True(t, ok)
	require.EqualValues(t, 2, sampleCountValue)
	_, hasSampleSizes := fields[moofLocmafIDs.SampleSizes]
	require.True(t, hasSampleSizes)
}

func TestDecompressMoofDefaultsMissingCompositionOffsetsToZero(t *testing.T) {
	track, moov := loadVideoTrack(t)
	object, _ := mustCompressedObject(t, track, 0, 0, 2, &MoofDeltaCompressor{})

	parsedObject, err := parseLocmafObject(object)
	require.NoError(t, err)

	fields, err := separateFields(parsedObject.properties)
	require.NoError(t, err)
	delete(fields, moofLocmafIDs.SampleCompositionTimeOffsets)

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
