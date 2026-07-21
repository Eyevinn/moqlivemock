package internal

import (
	"encoding/binary"
	"testing"

	"github.com/Eyevinn/mp4ff/av1"
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

	assert.Equal(t, "draft-01", cat.Version)
	require.NotNil(t, cat.GeneratedAt)
	assert.Equal(t, genAt, *cat.GeneratedAt)
	require.NotEmpty(t, cat.Tracks)

	// LOC supports AVC/HEVC/AV1 video + AAC/Opus audio. AC-3, EC-3 are excluded.
	// test10s has 3 AVC, 3 HEVC, 3 AV1, 2 AAC, 2 Opus, 2 AC-3 => 3+3+3+2+2 = 13
	// LOC tracks.
	require.Len(t, cat.Tracks, 13, "LOC catalog should contain AVC + HEVC + AV1 + AAC + Opus tracks")

	var sawAVC3, sawHEV1, sawAV01, sawLowerOpus, sawLOCPackaging bool
	for _, tr := range cat.Tracks {
		assert.Equal(t, "loc", tr.Packaging)
		assert.True(t, tr.IsLive)
		// Init data must NOT be emitted — LOC carries config in-band.
		assert.Empty(t, tr.InitRef, "LOC tracks must not include initRef (%s)", tr.Name)
		sawLOCPackaging = true

		if tr.Role == "video" {
			// AVC/HEVC codec strings are rewritten to in-payload variants
			// (avc3/hev1); AV1 keeps its av01 codec string.
			require.GreaterOrEqual(t, len(tr.Codec), 4)
			prefix := tr.Codec[:4]
			assert.Contains(t, []string{"avc3", "hev1", "av01"}, prefix,
				"video codec should start with avc3, hev1 or av01, got %q", tr.Codec)
			switch prefix {
			case "avc3":
				sawAVC3 = true
			case "hev1":
				sawHEV1 = true
			case "av01":
				sawAV01 = true
			}
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
	assert.True(t, sawHEV1, "at least one hev1 track expected")
	assert.True(t, sawAV01, "at least one av01 track expected")
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

func TestHEVCGenLOCVideoConfig(t *testing.T) {
	asset, err := LoadAsset("../assets/test10s", 1, 1)
	require.NoError(t, err)

	vt := asset.GetTrackByName("video_400kbps_hevc")
	require.NotNil(t, vt)
	hevcData, ok := vt.SpecData.(*HEVCData)
	require.True(t, ok)

	cfg := hevcData.GenLOCVideoConfig()
	require.NotEmpty(t, cfg)

	// Parse the length-prefixed concatenation and verify it reproduces the
	// original VPS+SPS+PPS NALUs in order.
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

	require.Equal(t, len(hevcData.Vpss)+len(hevcData.Spss)+len(hevcData.Ppss), len(extracted))
	off := 0
	for i, vps := range hevcData.Vpss {
		assert.Equal(t, vps, extracted[off+i], "VPS[%d] mismatch", i)
	}
	off += len(hevcData.Vpss)
	for i, sps := range hevcData.Spss {
		assert.Equal(t, sps, extracted[off+i], "SPS[%d] mismatch", i)
	}
	off += len(hevcData.Spss)
	for i, pps := range hevcData.Ppss {
		assert.Equal(t, pps, extracted[off+i], "PPS[%d] mismatch", i)
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

// TestAV1LOCSelfContained verifies the premise that lets AV1 ride the LOC path:
// the generated keyframes already embed the sequence header OBU, so LOC needs no
// in-band config prepend, and every per-object payload a subscriber receives is
// independently parseable (sync samples start with a sequence header, delta
// samples are bare frame OBUs).
func TestAV1LOCSelfContained(t *testing.T) {
	asset, err := LoadAsset("../assets/test10s", 1, 1)
	require.NoError(t, err)

	vt := asset.GetTrackByName("video_400kbps_av1")
	require.NotNil(t, vt)
	av1Data, ok := vt.SpecData.(*AV1Data)
	require.True(t, ok)

	require.Nil(t, av1Data.GenLOCVideoConfig(),
		"AV1 keyframes already carry the sequence header; LOC config should be nil")

	sawSync, sawDelta := false, false
	for i := range vt.Samples {
		obus, err := av1.SplitOBUs(vt.Samples[i].Data)
		require.NoErrorf(t, err, "sample %d must be valid OBUs", i)
		require.NotEmpty(t, obus)
		if vt.Samples[i].IsSync() {
			sawSync = true
			require.Equalf(t, av1.OBUSequenceHeader, obus[0].Header.Type,
				"sync sample %d must start with a sequence header OBU", i)
		} else {
			sawDelta = true
			for _, o := range obus {
				require.NotEqualf(t, av1.OBUSequenceHeader, o.Header.Type,
					"delta sample %d should not carry a sequence header OBU", i)
			}
		}
	}
	require.True(t, sawSync, "expected at least one sync sample")
	require.True(t, sawDelta, "expected at least one delta sample")
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
