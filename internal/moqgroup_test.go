package internal

import (
	"context"
	"fmt"
	"log"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestGenMoQGroup_VideoAudio(t *testing.T) {
	// Use similar setup as in asset_test.go
	asset, err := LoadAsset("../assets/test10s", 1, 1) // adjust path if needed
	require.NoError(t, err)
	require.NotNil(t, asset)

	var videoTrack, audioTrack *ContentTrack
	for _, group := range asset.Groups {
		for i := range group.Tracks {
			ct := &group.Tracks[i]
			if ct.ContentType == "video" && videoTrack == nil {
				videoTrack = ct
			}
			if ct.ContentType == "audio" && audioTrack == nil {
				audioTrack = ct
			}
		}
	}
	require.NotNil(t, videoTrack, "video track not found")
	require.NotNil(t, audioTrack, "audio track not found")

	const groupNr = 0
	const groupDurMS = 1000 // 1 second per MoQGroup

	// Video
	vg := GenMoQGroup(videoTrack, groupNr, 1, groupDurMS)
	require.NotNil(t, vg)
	// startTime and endTime should be aligned to sample duration
	require.Equal(t, uint64(0), vg.startTime%uint64(videoTrack.SampleDur), "video startTime not aligned")
	require.Equal(t, uint64(0), vg.endTime%uint64(videoTrack.SampleDur), "video endTime not aligned")
	// startNr and endNr should be integers
	require.True(t, vg.startNr <= vg.endNr, "video startNr > endNr")
	// The number of objects should match endNr-startNr
	require.Equal(t, int(vg.endNr-vg.startNr), len(vg.MoQObjects), "video MoQObjects count")

	// Audio
	ag := GenMoQGroup(audioTrack, groupNr, 1, groupDurMS)
	require.NotNil(t, ag)
	require.Equal(t, uint64(0), ag.startTime%uint64(audioTrack.SampleDur), "audio startTime not aligned")
	require.Equal(t, uint64(0), ag.endTime%uint64(audioTrack.SampleDur), "audio endTime not aligned")
	require.True(t, ag.startNr <= ag.endNr, "audio startNr > endNr")
	require.Equal(t, int(ag.endNr-ag.startNr), len(ag.MoQObjects), "audio MoQObjects count")
}

func TestGenMoQStreams(t *testing.T) {
	// StartNr corresponding to 2025-04-21T17:07:48Z
	startNr := uint64(1745255189)
	endNr := startNr + 15                       // 15 MoQGroups à 1s per MoQGroup
	asset, err := LoadAsset("../assets/test10s", 1, 1) // adjust path if needed
	require.NoError(t, err)
	require.NotNil(t, asset)
	for _, group := range asset.Groups {
		for i := range group.Tracks {
			ct := &group.Tracks[i]
			ofh, err := os.Create(fmt.Sprintf("%s.mp4", ct.Name))
			if err != nil {
				t.Fatalf("failed to create output file: %v", err)
			}
			defer ofh.Close()
			init, err := ct.SpecData.GenCMAFInitData()
			if err != nil {
				t.Fatalf("failed to generate init data: %v", err)
			}
			_, err = ofh.Write(init)
			if err != nil {
				t.Fatalf("failed to write init data: %v", err)
			}
			for nr := startNr; nr < endNr; nr++ {
				moq := GenMoQGroup(ct, nr, 1, 1000)
				if moq == nil {
					t.Fatalf("failed to generate MoQ group")
				}
				for _, obj := range moq.MoQObjects {
					_, err := ofh.Write(obj)
					if err != nil {
						t.Fatalf("failed to write object: %v", err)
					}
				}
			}
		}
	}
}

func TestWriteMoQGroupLive(t *testing.T) {
	asset, err := LoadAsset("../assets/test10s", 1, 1) // adjust path if needed
	require.NoError(t, err)
	require.NotNil(t, asset)
	name := "video_400kbps"
	ct := asset.GetTrackByName(name)
	require.NotNil(t, ct)
	ofh, err := os.Create(name + "_live.mp4")
	if err != nil {
		t.Fatalf("failed to create output file: %v", err)
	}
	defer ofh.Close()
	init, err := ct.SpecData.GenCMAFInitData()
	if err != nil {
		t.Fatalf("failed to generate init data: %v", err)
	}
	_, err = ofh.Write(init)
	if err != nil {
		t.Fatalf("failed to write init data: %v", err)
	}
	cb := func(objectID uint64, data []byte) (int, error) {
		return ofh.Write(data)
	}
	now := time.Now()
	nowMS := now.UnixMilli()
	currGroupNr := CurrMoQGroupNr(ct, uint64(nowMS), MoqGroupDurMS)
	groupNr := currGroupNr + 1 // Start stream on next group
	endNr := groupNr + 1       // 1 MoQGroup à 1s per MoQGroup
	for {
		mg := GenMoQGroup(ct, groupNr, 1, MoqGroupDurMS)
		err := WriteMoQGroup(context.Background(), ct, mg, cb)
		if err != nil {
			log.Printf("failed to write MoQ group: %v", err)
			return
		}
		log.Printf("published MoQ group %d, %d objects", groupNr, len(mg.MoQObjects))
		groupNr++
		if groupNr > endNr {
			break
		}
	}
	timePassed := time.Since(now)
	if timePassed < time.Duration(1*time.Second) {
		t.Fatalf("live MoQ group generation took less than 1 second: %v", timePassed)
	}
}

func TestGetLargestObject(t *testing.T) {
	tests := []struct {
		name         string
		timeScale    uint32
		sampleDur    uint32
		sampleBatch  int
		nowMS        uint64
		constantDurMS uint32
		expected     Location
		description  string
	}{
		{
			name:         "video_start_of_first_group",
			timeScale:    90000,
			sampleDur:    3750, // 24fps
			sampleBatch:  1,
			nowMS:        0,
			constantDurMS: 1000,
			expected:     Location{Group: 0, Object: 0},
			description:  "At time 0, should be at start of first group",
		},
		{
			name:         "video_middle_of_first_group",
			timeScale:    90000,
			sampleDur:    3750,
			sampleBatch:  1,
			nowMS:        500, // 0.5 seconds into first group
			constantDurMS: 1000,
			expected:     Location{Group: 0, Object: 12}, // ~12 frames at 24fps
			description:  "0.5s into first group should have ~12 objects",
		},
		{
			name:         "video_end_of_first_group",
			timeScale:    90000,
			sampleDur:    3750,
			sampleBatch:  1,
			nowMS:        999, // Near end of first group
			constantDurMS: 1000,
			expected:     Location{Group: 0, Object: 23}, // ~24 frames at 24fps
			description:  "Near end of first group should have ~23 objects (0-based)",
		},
		{
			name:         "video_start_of_second_group",
			timeScale:    90000,
			sampleDur:    3750,
			sampleBatch:  1,
			nowMS:        1000, // Start of second group
			constantDurMS: 1000,
			expected:     Location{Group: 1, Object: 0},
			description:  "At start of second group",
		},
		{
			name:         "audio_48khz_first_group",
			timeScale:    48000,
			sampleDur:    1024, // Common audio sample duration
			sampleBatch:  1,
			nowMS:        500, // 0.5 seconds
			constantDurMS: 1000,
			expected:     Location{Group: 0, Object: 23}, // ~24 samples at 48kHz/1024
			description:  "Audio at 48kHz, 0.5s should have ~23 objects",
		},
		{
			name:         "audio_with_batching",
			timeScale:    48000,
			sampleDur:    1024,
			sampleBatch:  5, // Batch 5 samples per object
			nowMS:        500,
			constantDurMS: 1000,
			expected:     Location{Group: 0, Object: 4}, // ~24/5 = 4 objects (0-based)
			description:  "Audio with sample batching should reduce object count",
		},
		{
			name:         "before_first_group",
			timeScale:    90000,
			sampleDur:    3750,
			sampleBatch:  1,
			nowMS:        0, // Before any content
			constantDurMS: 1000,
			expected:     Location{Group: 0, Object: 0},
			description:  "Before any content should return zero location",
		},
		{
			name:         "long_duration_group",
			timeScale:    90000,
			sampleDur:    3750,
			sampleBatch:  1,
			nowMS:        2500, // 2.5 seconds
			constantDurMS: 2000, // 2-second groups
			expected:     Location{Group: 1, Object: 12}, // 0.5s into second group
			description:  "With 2-second groups, 2.5s should be in second group",
		},
		{
			name:         "high_framerate_video",
			timeScale:    90000,
			sampleDur:    1500, // 60fps
			sampleBatch:  1,
			nowMS:        500,
			constantDurMS: 1000,
			expected:     Location{Group: 0, Object: 30}, // ~30 frames at 60fps
			description:  "60fps video should have more objects per second",
		},
		{
			name:         "multiple_groups_later",
			timeScale:    90000,
			sampleDur:    3750,
			sampleBatch:  1,
			nowMS:        5250, // 5.25 seconds
			constantDurMS: 1000,
			expected:     Location{Group: 5, Object: 6}, // 0.25s into 6th group
			description:  "Multiple groups later should calculate correctly",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a mock ContentTrack with the test parameters
			track := &ContentTrack{
				TimeScale:   tt.timeScale,
				SampleDur:   tt.sampleDur,
				SampleBatch: tt.sampleBatch,
			}

			result := GetLargestObject(track, tt.nowMS, tt.constantDurMS)
			
			require.Equal(t, tt.expected.Group, result.Group, 
				"Group mismatch: %s", tt.description)
			require.Equal(t, tt.expected.Object, result.Object, 
				"Object mismatch: %s", tt.description)
			
			t.Logf("Test '%s': nowMS=%d -> Location{Group: %d, Object: %d} (%s)", 
				tt.name, tt.nowMS, result.Group, result.Object, tt.description)
		})
	}
}

func TestGetLargestObjectEdgeCases(t *testing.T) {
	track := &ContentTrack{
		TimeScale:   90000,
		SampleDur:   3750, // 24fps
		SampleBatch: 1,
	}
	constantDurMS := uint32(1000)

	t.Run("exactly_at_group_boundary", func(t *testing.T) {
		result := GetLargestObject(track, 1000, constantDurMS) // Exactly 1 second
		require.Equal(t, uint64(1), result.Group, "Should be in second group")
		require.Equal(t, uint64(0), result.Object, "Should be first object in second group")
	})

	t.Run("just_before_group_boundary", func(t *testing.T) {
		result := GetLargestObject(track, 999, constantDurMS) // Just before 1 second
		require.Equal(t, uint64(0), result.Group, "Should still be in first group")
		require.True(t, result.Object > 0, "Should have some objects in first group")
	})

	t.Run("very_early_time", func(t *testing.T) {
		result := GetLargestObject(track, 1, constantDurMS) // 1ms
		require.Equal(t, uint64(0), result.Group, "Should be in first group")
		require.Equal(t, uint64(0), result.Object, "Should be first object")
	})

	t.Run("large_time_value", func(t *testing.T) {
		largeTime := uint64(3600000) // 1 hour
		result := GetLargestObject(track, largeTime, constantDurMS)
		require.Equal(t, uint64(3600), result.Group, "Should be in group 3600 (1 hour / 1 second)")
		require.Equal(t, uint64(0), result.Object, "Should be first object in that group")
	})
}
