package internal

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/stretchr/testify/require"
)

func TestPrepareTrack(t *testing.T) {
	testCases := []struct {
		desc          string
		filePath      string
		contentType   string
		language      string
		timeScale     int
		duration      int
		sampleDur     int
		nrSamples     int
		gopLength     int
		sampleBitrate int
	}{
		{
			desc:          "video_400kbps",
			filePath:      "../content/video_400kbps.mp4",
			contentType:   "video",
			timeScale:     12800,
			duration:      128000,
			sampleDur:     512,
			nrSamples:     250,
			gopLength:     25,
			sampleBitrate: 373200,
		},
		{
			desc:          "audio_128kbps",
			filePath:      "../content/audio_monotonic_128kbps.mp4",
			contentType:   "audio",
			timeScale:     48000,
			duration:      469 * 1024,
			sampleDur:     1024,
			nrSamples:     469,
			gopLength:     1,
			sampleBitrate: 127691,
			language:      "und",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			fh, err := os.Open(tc.filePath)
			require.NoError(t, err)
			ct, err := InitContentTrack(fh, tc.desc)
			require.NoError(t, err)
			require.Equal(t, tc.contentType, ct.contentType, "contentType")
			require.Equal(t, tc.timeScale, int(ct.timeScale), "timeScale")
			require.Equal(t, tc.duration, int(ct.duration), "duration")
			require.Equal(t, tc.sampleDur, int(ct.sampleDur), "sampleDur")
			require.Equal(t, tc.nrSamples, int(ct.nrSamples), "nrSamples")
			require.Equal(t, tc.gopLength, int(ct.gopLength), "gopLength")
			require.Equal(t, tc.sampleBitrate, int(ct.sampleBitrate), "sampleBitrate")
		})
	}
}

func TestLoadAsset(t *testing.T) {
	asset, err := LoadAsset("../content")
	require.NoError(t, err)
	require.NotNil(t, asset)

	// Check asset name
	require.Equal(t, "content", asset.name)

	// Collect all tracks by contentType
	trackCounts := map[string]int{}
	for _, group := range asset.groups {
		for _, track := range group.tracks {
			trackCounts[track.contentType]++
		}
	}
	// Expect 2 audio and 3 video tracks
	require.Equal(t, 2, trackCounts["audio"], "should have 2 audio tracks")
	require.Equal(t, 3, trackCounts["video"], "should have 3 video tracks")

	// Check that track names match the files
	var expectedNames = map[string]bool{
		"audio_monotonic_128kbps": true,
		"audio_scale_128kbps":     true,
		"video_400kbps":           true,
		"video_600kbps":           true,
		"video_900kbps":           true,
	}
	for _, group := range asset.groups {
		for _, track := range group.tracks {
			_, ok := expectedNames[track.name]
			require.True(t, ok, "unexpected track name: %s", track.name)
		}
	}

	// Check that video tracks are in bitrate order (ascending)
	var videoBitrates []int
	var videoNames []string
	for _, group := range asset.groups {
		if len(group.tracks) > 0 && group.tracks[0].contentType == "video" {
			for _, track := range group.tracks {
				videoBitrates = append(videoBitrates, int(track.sampleBitrate))
				videoNames = append(videoNames, track.name)
			}
		}
	}
	for i := 1; i < len(videoBitrates); i++ {
		require.LessOrEqual(t, videoBitrates[i-1], videoBitrates[i],
			"video tracks not in bitrate order: got %v (%v)", videoBitrates, videoNames)
	}

	// Check that video group has a lower altGroupID than audio group
	var videoGroupID, audioGroupID uint32
	for _, group := range asset.groups {
		if len(group.tracks) > 0 {
			switch group.tracks[0].contentType {
			case "video":
				videoGroupID = group.altGroupID
			case "audio":
				audioGroupID = group.altGroupID
			}
		}
	}
	if videoGroupID != 0 && audioGroupID != 0 {
		require.Less(t, videoGroupID, audioGroupID,
			"video group altGroupID should be less than audio group altGroupID")
	}
	require.Equal(t, 10000, int(asset.loopDurMS), "loop duration should be 10000ms")
	for _, group := range asset.groups {
		for _, track := range group.tracks {
			require.Equal(t, int(10*track.timeScale), int(track.loopDur),
				"loop duration should be 10s in timescale")
		}
	}
	cat, err := asset.GenCMAFCatalogEntry()
	require.NoError(t, err)
	require.NotNil(t, cat)
	require.Equal(t, 5, len(cat.Tracks))
	names := []string{"video_400kbps", "video_600kbps", "video_900kbps", "audio_monotonic_128kbps", "audio_scale_128kbps"}
	for i, track := range cat.Tracks {
		require.Equal(t, Namespace, track.Namespace)
		require.Equal(t, names[i], track.Name)
	}
}

func TestGen20sCMAFStreams(t *testing.T) {
	asset, err := LoadAsset("../content")
	require.NoError(t, err)
	require.NotNil(t, asset)

	tmpDir := t.TempDir()
	cases := []struct {
		name     string
		groupIdx int
		trackNr  int
	}{
		{"video_400kbps", 0, 0},
		{"video_600kbps", 0, 1},
		{"video_900kbps", 0, 2},
		{"audio_128kbps", 1, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := asset.groups[tc.groupIdx].tracks[tc.trackNr]
			outFile := filepath.Join(tmpDir, tc.name+".mp4")
			ofh, err := os.Create(outFile)
			require.NoError(t, err)
			spc := tr.specData
			data, err := spc.GenCMAFInitData()
			require.NoError(t, err)
			_, err = ofh.Write(data)
			require.NoError(t, err)
			nrSamples := int(20 * tr.timeScale / tr.sampleDur)
			for nr := 0; nr < nrSamples; nr++ {
				chunk, err := tr.GetCMAFChunk(uint64(nr))
				require.NoError(t, err)
				_, err = ofh.Write(chunk)
				require.NoError(t, err)
			}
			ofh.Close()
			fh, err := os.Open(outFile)
			require.NoError(t, err)
			defer fh.Close()
			mp4f, err := mp4.DecodeFile(fh)
			require.NoError(t, err)
			require.Equal(t, 1, len(mp4f.Segments))
			require.Equal(t, nrSamples, len(mp4f.Segments[0].Fragments))
			for _, frag := range mp4f.Segments[0].Fragments {
				// Tfdt version will be 0 at start, but 1 as needed
				// for big enough timestamps (64-bit need)
				require.Equal(t, 0, int(frag.Moof.Traf.Tfdt.Version))
				// Size of fragment should be 100 bytes for tfdt version 0
				// and exactly one sample without compositionTimeOffset.
				require.Equal(t, 100, int(frag.Moof.Size()))
			}
		})
	}
}
