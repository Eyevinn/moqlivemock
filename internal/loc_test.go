package internal

import (
	"encoding/binary"
	"testing"

	"github.com/Eyevinn/mp4ff/avc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenLOCCatalogEntry(t *testing.T) {
	asset, err := LoadAsset("../assets/test10s", 1, 1)
	require.NoError(t, err)

	const genAt = int64(1700000000000)
	cat, err := asset.GenLOCCatalogEntry(genAt)
	require.NoError(t, err)
	require.NotNil(t, cat)

	assert.Equal(t, 1, cat.Version)
	require.NotNil(t, cat.GeneratedAt)
	assert.Equal(t, genAt, *cat.GeneratedAt)
	require.NotEmpty(t, cat.Tracks)

	// LOC only supports AVC video + AAC/Opus audio. HEVC, AC-3, EC-3 are excluded.
	// test10s has 3 AVC, 3 HEVC, 2 AAC, 2 Opus, 2 AC-3 => 3+2+2 = 7 LOC tracks.
	require.Len(t, cat.Tracks, 7, "LOC catalog should contain only AVC + AAC + Opus tracks")

	var sawAVC3, sawLowerOpus, sawLOCPackaging bool
	for _, tr := range cat.Tracks {
		assert.Equal(t, "loc", tr.Packaging)
		assert.True(t, tr.IsLive)
		// Init data must NOT be emitted — LOC carries config in-band.
		assert.Empty(t, tr.InitData, "LOC tracks must not include initData (%s)", tr.Name)
		sawLOCPackaging = true

		if tr.Role == "video" {
			// AVC codec string must be rewritten to avc3.* for LOC.
			assert.True(t, len(tr.Codec) >= 4 && tr.Codec[:4] == "avc3",
				"video codec should start with avc3, got %q", tr.Codec)
			sawAVC3 = true
			require.NotNil(t, tr.Framerate)
			assert.Greater(t, *tr.Framerate, 0.0)
		}
		if tr.Codec == "opus" {
			sawLowerOpus = true
			require.NotNil(t, tr.SampleRate)
			assert.NotEmpty(t, tr.ChannelConfig)
		}
	}
	assert.True(t, sawLOCPackaging)
	assert.True(t, sawAVC3, "at least one avc3 track expected")
	assert.True(t, sawLowerOpus, "Opus codec string should be lowercase 'opus'")

	// Protected tracks should not leak into the LOC catalog.
	for _, tr := range cat.Tracks {
		assert.Empty(t, tr.ContentProtectionRefIDs,
			"LOC catalog should never carry ContentProtectionRefIDs")
	}
}

func TestAVCGenLOCVideoConfig(t *testing.T) {
	asset, err := LoadAsset("../assets/test10s", 1, 1)
	require.NoError(t, err)

	vt := asset.GetTrackByName("video_400kbps_avc")
	require.NotNil(t, vt)
	avcData, ok := vt.SpecData.(*AVCData)
	require.True(t, ok)

	cfg := avcData.GenLOCVideoConfig()
	require.NotEmpty(t, cfg)

	// Parse the length-prefixed concatenation and verify it reproduces the
	// original SPS+PPS NALUs in order.
	var extracted [][]byte
	data := cfg
	for len(data) >= 4 {
		n := binary.BigEndian.Uint32(data[:4])
		data = data[4:]
		require.LessOrEqual(t, int(n), len(data))
		extracted = append(extracted, append([]byte{}, data[:n]...))
		data = data[n:]
	}
	assert.Empty(t, data, "no trailing bytes")

	require.Equal(t, len(avcData.Spss)+len(avcData.Ppss), len(extracted))
	for i, sps := range avcData.Spss {
		assert.Equal(t, sps, extracted[i], "SPS[%d] mismatch", i)
	}
	for i, pps := range avcData.Ppss {
		assert.Equal(t, pps, extracted[len(avcData.Spss)+i], "PPS[%d] mismatch", i)
	}
}

func TestAVCGenAVCDecoderConfigurationRecord(t *testing.T) {
	asset, err := LoadAsset("../assets/test10s", 1, 1)
	require.NoError(t, err)

	vt := asset.GetTrackByName("video_400kbps_avc")
	require.NotNil(t, vt)
	avcData, ok := vt.SpecData.(*AVCData)
	require.True(t, ok)

	dcr, err := avcData.GenAVCDecoderConfigurationRecord()
	require.NoError(t, err)
	require.NotEmpty(t, dcr)

	// First byte is configurationVersion (1); fields 2-4 are profile/compat/level
	// which should match the SPS. Round-trip via mp4ff/avc to make sure the
	// record is actually decodable.
	parsed, err := avc.DecodeAVCDecConfRec(dcr)
	require.NoError(t, err)
	require.NotNil(t, parsed)
	require.Equal(t, 1, len(parsed.SPSnalus))
	require.Equal(t, 1, len(parsed.PPSnalus))
	assert.Equal(t, avcData.Spss[0], parsed.SPSnalus[0])
	assert.Equal(t, avcData.Ppss[0], parsed.PPSnalus[0])
}

func TestAACAndOpusAccessors(t *testing.T) {
	asset, err := LoadAsset("../assets/test10s", 1, 1)
	require.NoError(t, err)

	aac := asset.GetTrackByName("audio_monotonic_128kbps_aac")
	require.NotNil(t, aac)
	aacData, ok := aac.SpecData.(*AACData)
	require.True(t, ok)
	assert.Equal(t, uint32(48000), aacData.SampleRate())
	assert.Equal(t, "2", aacData.ChannelConfig())

	opus := asset.GetTrackByName("audio_monotonic_128kbps_opus")
	require.NotNil(t, opus)
	opusData, ok := opus.SpecData.(*OpusData)
	require.True(t, ok)
	assert.Greater(t, opusData.SampleRate(), uint32(0))
	assert.Equal(t, "2", opusData.ChannelConfig())
}
