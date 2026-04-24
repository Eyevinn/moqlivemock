package pub

import (
	"testing"

	"github.com/Eyevinn/moqlivemock/internal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveMoqMITrack(t *testing.T) {
	m := MoqMITrackMap{
		"video0": "video_400kbps_avc",
		"audio0": "audio_monotonic_128kbps_aac",
	}
	assert.Equal(t, "video_400kbps_avc", ResolveMoqMITrack(m, "video0"))
	assert.Equal(t, "audio_monotonic_128kbps_aac", ResolveMoqMITrack(m, "audio0"))
	assert.Equal(t, "", ResolveMoqMITrack(m, "video1"))
	assert.Equal(t, "", ResolveMoqMITrack(nil, "video0"))
}

func TestBuildMoqMITrackMap_PrefersAAC(t *testing.T) {
	asset, err := internal.LoadAsset("../../assets/test10s", 1, 1)
	require.NoError(t, err)

	m, err := BuildMoqMITrackMap(asset)
	require.NoError(t, err)

	// Default build picks first AVC video and first AAC-LC audio (not Opus).
	video0 := m["video0"]
	require.NotEmpty(t, video0)
	vt := asset.GetTrackByName(video0)
	require.NotNil(t, vt)
	_, isAVC := vt.SpecData.(*internal.AVCData)
	assert.True(t, isAVC, "video0 should map to an AVC track, got %q", video0)

	audio0 := m["audio0"]
	require.NotEmpty(t, audio0)
	at := asset.GetTrackByName(audio0)
	require.NotNil(t, at)
	_, isAAC := at.SpecData.(*internal.AACData)
	assert.True(t, isAAC, "audio0 should prefer AAC over Opus, got %q", audio0)
}

func TestBuildMoqMITrackMap_FallsBackToOpus(t *testing.T) {
	// Craft an asset that has AVC video but only Opus audio.
	asset, err := internal.LoadAsset("../../assets/test10s", 1, 1)
	require.NoError(t, err)

	// Filter out everything that isn't AVC video or Opus audio.
	var filtered []internal.TrackGroup
	for _, g := range asset.Groups {
		var keep []internal.ContentTrack
		for _, ct := range g.Tracks {
			switch ct.SpecData.(type) {
			case *internal.AVCData, *internal.OpusData:
				keep = append(keep, ct)
			}
		}
		if len(keep) > 0 {
			filtered = append(filtered, internal.TrackGroup{AltGroupID: g.AltGroupID, Tracks: keep})
		}
	}
	asset.Groups = filtered

	m, err := BuildMoqMITrackMap(asset)
	require.NoError(t, err)

	at := asset.GetTrackByName(m["audio0"])
	require.NotNil(t, at)
	_, isOpus := at.SpecData.(*internal.OpusData)
	assert.True(t, isOpus, "audio0 should fall back to Opus when no AAC is present")
}

func TestBuildMoqMITrackMap_ErrorWhenNoVideo(t *testing.T) {
	asset := &internal.Asset{
		Groups: []internal.TrackGroup{},
	}
	_, err := BuildMoqMITrackMap(asset)
	require.Error(t, err)
}

func TestAudioChannels(t *testing.T) {
	asset, err := internal.LoadAsset("../../assets/test10s", 1, 1)
	require.NoError(t, err)

	aac := asset.GetTrackByName("audio_monotonic_128kbps_aac")
	require.NotNil(t, aac)
	assert.Equal(t, uint64(2), audioChannels(aac.SpecData))

	opus := asset.GetTrackByName("audio_monotonic_128kbps_opus")
	require.NotNil(t, opus)
	assert.Equal(t, uint64(2), audioChannels(opus.SpecData))

	// Non-audio track (AVC) returns 0.
	avc := asset.GetTrackByName("video_400kbps_avc")
	require.NotNil(t, avc)
	assert.Equal(t, uint64(0), audioChannels(avc.SpecData))
}
