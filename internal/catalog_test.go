package internal

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCatalogGetTrackByName(t *testing.T) {
	cat := &Catalog{
		Tracks: []Track{
			{Name: "video_400kbps_avc", Codec: "avc1"},
			{Name: "audio_128kbps_aac", Codec: "mp4a"},
		},
	}

	t.Run("found", func(t *testing.T) {
		track := cat.GetTrackByName("video_400kbps_avc")
		require.NotNil(t, track)
		assert.Equal(t, "avc1", track.Codec)
	})

	t.Run("not found", func(t *testing.T) {
		track := cat.GetTrackByName("nonexistent")
		assert.Nil(t, track)
	})

	t.Run("empty catalog", func(t *testing.T) {
		empty := &Catalog{}
		assert.Nil(t, empty.GetTrackByName("anything"))
	})
}

func TestCatalogString(t *testing.T) {
	cat := &Catalog{
		Version: 1,
		Tracks: []Track{
			{Name: "video", InitData: "short"},
			{Name: "audio", InitData: "this_is_a_very_long_init_data_string_that_exceeds_20_chars"},
		},
	}

	s := cat.String()

	// Should be valid JSON
	var parsed map[string]any
	require.NoError(t, json.Unmarshal([]byte(s), &parsed))

	// Short init data should be preserved
	assert.Contains(t, s, `"short"`)

	// Long init data should be truncated
	assert.Contains(t, s, "...")
	assert.Contains(t, s, "len=")

	// Original catalog should not be modified
	assert.Equal(t, "this_is_a_very_long_init_data_string_that_exceeds_20_chars", cat.Tracks[1].InitData)
}

func TestCatalogStringEmpty(t *testing.T) {
	cat := &Catalog{Version: 1}
	s := cat.String()
	assert.True(t, strings.Contains(s, `"version": 1`))
}
