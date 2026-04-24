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
	vg, err := GenMoQGroup(videoTrack, groupNr, 1, groupDurMS)
	require.NoError(t, err)
	require.NotNil(t, vg)
	// startTime and endTime should be aligned to sample duration
	require.Equal(t, uint64(0), vg.startTime%uint64(videoTrack.SampleDur), "video startTime not aligned")
	require.Equal(t, uint64(0), vg.endTime%uint64(videoTrack.SampleDur), "video endTime not aligned")
	// startNr and endNr should be integers
	require.True(t, vg.startNr <= vg.endNr, "video startNr > endNr")
	// The number of objects should match endNr-startNr
	require.Equal(t, int(vg.endNr-vg.startNr), len(vg.MoQObjects), "video MoQObjects count")

	// Audio
	ag, err := GenMoQGroup(audioTrack, groupNr, 1, groupDurMS)
	require.NoError(t, err)
	require.NotNil(t, ag)
	require.Equal(t, uint64(0), ag.startTime%uint64(audioTrack.SampleDur), "audio startTime not aligned")
	require.Equal(t, uint64(0), ag.endTime%uint64(audioTrack.SampleDur), "audio endTime not aligned")
	require.True(t, ag.startNr <= ag.endNr, "audio startNr > endNr")
	require.Equal(t, int(ag.endNr-ag.startNr), len(ag.MoQObjects), "audio MoQObjects count")
}

func TestGenMoQStreams(t *testing.T) {
	// StartNr corresponding to 2025-04-21T17:07:48Z
	startNr := uint64(1745255189)
	endNr := startNr + 15                              // 15 MoQGroups à 1s per MoQGroup
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
				moq, err := GenMoQGroup(ct, nr, 1, 1000)
				if err != nil {
					t.Fatalf("failed to generate MoQ group: %v", err)
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

func TestCalcLOCGroupRange(t *testing.T) {
	asset, err := LoadAsset("../assets/test10s", 1, 1)
	require.NoError(t, err)
	require.NotNil(t, asset)

	video := asset.GetTrackByName("video_400kbps_avc")
	require.NotNil(t, video)
	// video_400kbps_avc: 25 fps => 25 samples per 1s group.
	start, end := CalcLOCGroupRange(video, 0, 1000)
	require.Equal(t, uint64(0), start)
	require.Equal(t, uint64(25), end)

	// Group 10 starts 10s in.
	start10, end10 := CalcLOCGroupRange(video, 10, 1000)
	require.Equal(t, uint64(250), start10)
	require.Equal(t, uint64(275), end10)
	require.Equal(t, end-start, end10-start10, "group size should be constant")

	// AAC audio at 48000/1024 samples/sec => ~46.875 frames per 1s group.
	// Two consecutive groups must together cover >= 2s of audio.
	audio := asset.GetTrackByName("audio_monotonic_128kbps_aac")
	require.NotNil(t, audio)
	aStart0, aEnd0 := CalcLOCGroupRange(audio, 0, 1000)
	aStart1, aEnd1 := CalcLOCGroupRange(audio, 1, 1000)
	require.Equal(t, aEnd0, aStart1, "LOC audio groups should be contiguous")
	require.GreaterOrEqual(t, aEnd1-aStart0, uint64(93), "2s of AAC ≈ 93.75 frames")
}

func TestWriteMoQGroupLive(t *testing.T) {
	asset, err := LoadAsset("../assets/test10s", 1, 1) // adjust path if needed
	require.NoError(t, err)
	require.NotNil(t, asset)
	name := "video_400kbps_avc"
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
		mg, err := GenMoQGroup(ct, groupNr, 1, MoqGroupDurMS)
		if err != nil {
			t.Fatalf("failed to generate MoQ group: %v", err)
		}
		err = WriteMoQGroup(context.Background(), ct, mg, cb)
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
