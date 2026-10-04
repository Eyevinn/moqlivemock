package internal

import (
	"testing"

	"github.com/Eyevinn/mp4ff/bits"
	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/stretchr/testify/require"
)

// TestAV1ConfigFromKeyframe checks that av1C holds the sequence header of the
// keyframes. The av1C of the test assets, from ffmpeg and libsvtav1, holds one
// with intra edge filter, interintra compound, warped motion, CDEF and loop
// restoration turned off, which the frames use.
func TestAV1ConfigFromKeyframe(t *testing.T) {
	asset, err := LoadAsset("../assets/test10s", 1, 1)
	require.NoError(t, err)
	ct := asset.GetTrackByName("video_400kbps_av1")
	require.NotNil(t, ct)
	ad, ok := ct.SpecData.(*AV1Data)
	require.True(t, ok)

	seqOBU, ok := keyframeAV1SeqHeader(ct.Samples)
	require.True(t, ok, "the keyframes carry a sequence header")
	av1C := ad.GetInit().Moov.Trak.Mdia.Minf.Stbl.Stsd.Av01.Av1C
	require.Equal(t, seqOBU.Encode(), av1C.ConfigOBUs)
	sh, err := av1C.SequenceHeader()
	require.NoError(t, err)
	require.True(t, sh.EnableCdef && sh.EnableRestoration && sh.EnableWarpedMotion,
		"av1C enables the tools that the frames use")

	srcAv1C := ad.inInit.Moov.Trak.Mdia.Minf.Stbl.Stsd.Av01.Av1C
	require.NotEqual(t, srcAv1C.ConfigOBUs, av1C.ConfigOBUs,
		"the source av1C differs, which is what this guards against")

	same, err := av1CWithSequenceHeader(av1C, seqOBU)
	require.NoError(t, err)
	require.Same(t, av1C, same, "an av1C that has the keyframe sequence header is kept")
}

// TestAV1EncryptionClearsHeaders checks that encrypting an AV1 track leaves
// exactly the OBU and frame headers in the clear. wantClear holds the offsets
// of the tile data of the first 25 frames of video_400kbps_av1, read with
// ffmpeg's trace_headers bitstream filter. Getting them right takes both the
// keyframes' sequence header in av1C and one encryptor per MoQ group, since
// inter frame headers depend on the reference frames before them.
func TestAV1EncryptionClearsHeaders(t *testing.T) {
	wantClear := []uint16{27, 18, 20, 19, 22, 19, 20, 19, 22, 19, 20, 19, 22, 19, 22, 19, 22, 19, 20, 20, 22, 19,
		22, 19, 22}
	for _, scheme := range []string{"cbcs", "cenc"} {
		t.Run(scheme, func(t *testing.T) {
			eccp, err := ParseCENCflags(scheme, "39112233445566778899aabbccddeeff",
				"40112233445566778899aabbccddeeff", "41112233445566778899aabbccddeeff",
				"http://localhost:8081/clearkey")
			require.NoError(t, err)
			asset, err := LoadAssetWithProtection("../assets/test10s", 1, 1, nil, eccp)
			require.NoError(t, err)
			ct := asset.GetTrackByName("video_400kbps_av1_eccp")
			require.NotNil(t, ct)

			mg, err := GenMoQGroup(ct, 0, 1, MoqGroupDurMS, "cmaf")
			require.NoError(t, err)
			require.Len(t, mg.MoQObjects, len(wantClear))
			for i, obj := range mg.MoQObjects {
				f, err := mp4.DecodeFileSR(bits.NewFixedSliceReader(obj))
				require.NoError(t, err)
				frag := f.Segments[0].Fragments[0]
				require.NoError(t, frag.ParseSenc(ct.SpecData.GetInit()))
				got := frag.Moof.Traf.Senc.SubSamples[0][0].BytesOfClearData
				require.Equalf(t, wantClear[i], got, "frame %d clear bytes", i)
			}
		})
	}
}
