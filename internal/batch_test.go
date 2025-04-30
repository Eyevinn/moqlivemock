package internal

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCalcCmafBitrate(t *testing.T) {
	testCases := []struct {
		desc          string
		sampleBitrate uint32
		frameRate     float64
		sampleBatch   int
		expectedRate  int
	}{
		{
			desc:          "video_batch_1",
			sampleBitrate: 400000,
			frameRate:     25.0,
			sampleBatch:   1,
			expectedRate:  402800, // 400000 + 8*112*25
		},
		{
			desc:          "video_batch_2",
			sampleBitrate: 400000,
			frameRate:     25.0,
			sampleBatch:   2,
			expectedRate:  401500, // 400000 + 8*(112+8)*25/2
		},
		{
			desc:          "audio_batch_1",
			sampleBitrate: 128000,
			frameRate:     46.875, // 48000/1024
			sampleBatch:   1,
			expectedRate:  130100, // 128000 + 8*112*46.875
		},
		{
			desc:          "audio_batch_4",
			sampleBitrate: 128000,
			frameRate:     46.875,
			sampleBatch:   4,
			expectedRate:  128759, // 128000 + 8*(112+3*8)*46.875/4
		},
	}

	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			rate := calcCmafBitrate(tc.sampleBitrate, tc.frameRate, tc.sampleBatch)
			require.Equal(t, tc.expectedRate, rate)
		})
	}
}

func TestInitContentTrackWithBatch(t *testing.T) {
	testCases := []struct {
		desc             string
		filePath         string
		audioSampleBatch int
		videoSampleBatch int
		expectedBatch    int
	}{
		{
			desc:             "video_400kbps_batch_1",
			filePath:         "../content/video_400kbps.mp4",
			audioSampleBatch: 2,
			videoSampleBatch: 1,
			expectedBatch:    1,
		},
		{
			desc:             "video_400kbps_batch_3",
			filePath:         "../content/video_400kbps.mp4",
			audioSampleBatch: 2,
			videoSampleBatch: 3,
			expectedBatch:    3,
		},
		{
			desc:             "audio_128kbps_batch_2",
			filePath:         "../content/audio_monotonic_128kbps.mp4",
			audioSampleBatch: 2,
			videoSampleBatch: 3,
			expectedBatch:    2,
		},
		{
			desc:             "audio_128kbps_batch_4",
			filePath:         "../content/audio_monotonic_128kbps.mp4",
			audioSampleBatch: 4,
			videoSampleBatch: 1,
			expectedBatch:    4,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			fh, err := os.Open(tc.filePath)
			require.NoError(t, err)
			defer fh.Close()
			
			ct, err := InitContentTrack(fh, tc.desc, tc.audioSampleBatch, tc.videoSampleBatch)
			require.NoError(t, err)
			require.Equal(t, tc.expectedBatch, ct.SampleBatch, "SampleBatch")
		})
	}
}

func TestLoadAssetWithBatch(t *testing.T) {
	testCases := []struct {
		desc             string
		audioSampleBatch int
		videoSampleBatch int
	}{
		{
			desc:             "default_batch_1_1",
			audioSampleBatch: 1,
			videoSampleBatch: 1,
		},
		{
			desc:             "audio_batch_2_video_batch_1",
			audioSampleBatch: 2,
			videoSampleBatch: 1,
		},
		{
			desc:             "audio_batch_4_video_batch_2",
			audioSampleBatch: 4,
			videoSampleBatch: 2,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			asset, err := LoadAsset("../content", tc.audioSampleBatch, tc.videoSampleBatch)
			require.NoError(t, err)
			require.NotNil(t, asset)

			// Check that all tracks have the correct batch size
			for _, group := range asset.groups {
				for _, track := range group.tracks {
					switch track.contentType {
					case "audio":
						require.Equal(t, tc.audioSampleBatch, track.SampleBatch, 
							"Audio track %s should have batch size %d", track.name, tc.audioSampleBatch)
					case "video":
						require.Equal(t, tc.videoSampleBatch, track.SampleBatch, 
							"Video track %s should have batch size %d", track.name, tc.videoSampleBatch)
					}
				}
			}

			// Test that the catalog bitrates are calculated correctly based on batch size
			catalog, err := asset.GenCMAFCatalogEntry()
			require.NoError(t, err)
			require.NotNil(t, catalog)

			// Verify that tracks exist in the catalog
			require.Equal(t, 5, len(catalog.Tracks))

			// Check that the bitrates in the catalog reflect the batch sizes
			for _, track := range catalog.Tracks {
				// Find the corresponding ContentTrack
				var contentTrack *ContentTrack
				for _, group := range asset.groups {
					for i := range group.tracks {
						if group.tracks[i].name == track.Name {
							contentTrack = &group.tracks[i]
							break
						}
					}
					if contentTrack != nil {
						break
					}
				}
				require.NotNil(t, contentTrack, "Track %s should exist in asset", track.Name)

				// Calculate expected bitrate
				frameRate := float64(contentTrack.timeScale) / float64(contentTrack.sampleDur)
				expectedBitrate := calcCmafBitrate(contentTrack.sampleBitrate, frameRate, contentTrack.SampleBatch)
				
				require.Equal(t, expectedBitrate, *track.Bitrate, 
					"Track %s should have bitrate calculated with batch size %d", 
					track.Name, contentTrack.SampleBatch)
			}
		})
	}
}

func TestGenCMAFChunkWithBatch(t *testing.T) {
	// Test different batch sizes for chunk generation
	testCases := []struct {
		desc             string
		audioSampleBatch int
		videoSampleBatch int
	}{
		{
			desc:             "batch_1_1",
			audioSampleBatch: 1,
			videoSampleBatch: 1,
		},
		{
			desc:             "batch_2_2",
			audioSampleBatch: 2,
			videoSampleBatch: 2,
		},
		{
			desc:             "batch_4_3",
			audioSampleBatch: 4,
			videoSampleBatch: 3,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			asset, err := LoadAsset("../content", tc.audioSampleBatch, tc.videoSampleBatch)
			require.NoError(t, err)
			require.NotNil(t, asset)

			// Test chunk generation for each track
			for _, group := range asset.groups {
				for _, track := range group.tracks {
					// Test with different batch sizes
					batchSize := track.SampleBatch
					
					// Generate a chunk with the configured batch size
					chunk, err := track.GenCMAFChunk(0, 0, uint64(batchSize))
					require.NoError(t, err)
					require.NotNil(t, chunk)
					
					// For video tracks with batch > 1, the chunk should be larger than a single sample chunk
					if track.contentType == "video" && batchSize > 1 {
						// Generate a single sample chunk for comparison
						singleChunk, err := track.GenCMAFChunk(0, 0, 1)
						require.NoError(t, err)
						
						// The multi-sample chunk should be larger than the single sample chunk
						// but not proportionally larger due to overhead sharing
						require.Greater(t, len(chunk), len(singleChunk), 
							"Multi-sample chunk should be larger than single sample chunk")
						
						// The chunk should be smaller than batchSize * single sample chunks
						// due to shared overhead
						require.Less(t, len(chunk), batchSize*len(singleChunk), 
							"Multi-sample chunk should be smaller than batchSize * single sample chunks")
					}
				}
			}
		})
	}
}
