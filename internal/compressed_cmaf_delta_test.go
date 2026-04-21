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
	moof0 := decodeMoof(t, mustChunk(t, track, 0, 0, 1))
	moof1 := decodeMoof(t, mustChunk(t, track, 1, 1, 2))

	var compressor MoofDeltaCompressor
	headerID0, payload0, err := compressor.CompressMoof(moof0, moov)
	require.NoError(t, err)
	require.EqualValues(t, MoofHeader, headerID0)

	headerID1, payload1, err := compressor.CompressMoof(moof1, moov)
	require.NoError(t, err)
	require.EqualValues(t, MoofDeltaHeader, headerID1)

	var decoder MoofDeltaDecompressor
	rebuilt0, err := decoder.DecompressMoof(headerID0, payload0, 0, moov)
	require.NoError(t, err)
	rebuilt1, err := decoder.DecompressMoof(headerID1, payload1, 1, moov)
	require.NoError(t, err)

	requireCompressedMoofEqual(t, moof0, rebuilt0, moov)
	requireCompressedMoofEqual(t, moof1, rebuilt1, moov)
}

func TestMoofDeltaConverterKeepsState(t *testing.T) {
	track, moov := loadVideoTrack(t)
	moof0 := decodeMoof(t, mustChunk(t, track, 0, 0, 1))
	moof1 := decodeMoof(t, mustChunk(t, track, 1, 1, 2))

	var compressor MoofDeltaCompressor
	property0, err := compressor.CreateMoofProperty(moof0, moov)
	require.NoError(t, err)
	property1, err := compressor.CreateMoofProperty(moof1, moov)
	require.NoError(t, err)

	var converter MoofDeltaDecompressor
	frag0, err := converter.ConvertCompressedCMAFPropertyToCMAF(property0, 0, moov)
	require.NoError(t, err)
	frag1, err := converter.ConvertCompressedCMAFPropertyToCMAF(property1, 1, moov)
	require.NoError(t, err)

	requireCompressedMoofEqual(t, moof0, frag0.Moof, moov)
	requireCompressedMoofEqual(t, moof1, frag1.Moof, moov)
}

func TestMoofDeltaRequiresPreviousMoof(t *testing.T) {
	track, moov := loadVideoTrack(t)
	moof0 := decodeMoof(t, mustChunk(t, track, 0, 0, 1))
	moof1 := decodeMoof(t, mustChunk(t, track, 1, 1, 2))

	var compressor MoofDeltaCompressor
	_, _, err := compressor.CompressMoof(moof0, moov)
	require.NoError(t, err)
	headerID, payload, err := compressor.CompressMoof(moof1, moov)
	require.NoError(t, err)
	require.EqualValues(t, MoofDeltaHeader, headerID)

	var decoder MoofDeltaDecompressor
	_, err = decoder.DecompressMoof(headerID, payload, 1, moov)
	require.ErrorContains(t, err, "missing previous moof state")
}

func TestMoofDeltaAllowsEmptyDeltaPayload(t *testing.T) {
	track, moov := loadVideoTrack(t)
	moof := decodeMoof(t, mustChunk(t, track, 0, 0, 1))

	var compressor MoofDeltaCompressor
	headerID0, payload0, err := compressor.CompressMoof(moof, moov)
	require.NoError(t, err)
	headerID1, payload1, err := compressor.CompressMoof(moof, moov)
	require.NoError(t, err)
	require.EqualValues(t, MoofDeltaHeader, headerID1)
	require.Empty(t, payload1)

	var decoder MoofDeltaDecompressor
	_, err = decoder.DecompressMoof(headerID0, payload0, 0, moov)
	require.NoError(t, err)
	rebuilt, err := decoder.DecompressMoof(headerID1, payload1, 1, moov)
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

func decodeMoof(t *testing.T, chunk []byte) *mp4.MoofBox {
	t.Helper()

	box, err := mp4.DecodeBox(0, bytes.NewReader(chunk))
	require.NoError(t, err)
	moof, ok := box.(*mp4.MoofBox)
	require.True(t, ok)
	return moof
}

func requireCompressedMoofEqual(t *testing.T, expected, actual *mp4.MoofBox, moov *mp4.MoovBox) {
	t.Helper()

	expectedPayload, err := CompressMoof(expected, moov)
	require.NoError(t, err)
	actualPayload, err := CompressMoof(actual, moov)
	require.NoError(t, err)
	require.Equal(t, expectedPayload, actualPayload)
}
