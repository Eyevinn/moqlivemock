package main

// import (
// 	"testing"

// 	"github.com/Eyevinn/moqlivemock/internal"
// )

// func TestMeasureDeltaHeaderFieldUsageVideo(t *testing.T) {
// 	videoTrack, _ := loadDeltaHeaderFieldUsageTestTracks(t)

// 	results, err := measureDeltaHeaderFieldUsage(videoTrack, 1, 25, "none")
// 	if err != nil {
// 		t.Fatalf("measure video delta header field usage: %v", err)
// 	}

// 	assertDeltaHeaderFieldUsage(t, results, []deltaHeaderFieldUsage{
// 		{
// 			AssetName:    "video_400kbps_avc",
// 			Protection:   "none",
// 			DeltaHeaders: 24,
// 			FieldID:      "7",
// 			FieldName:    "Sample flags",
// 			Appearances:  1,
// 		},
// 	})
// }

// func TestMeasureDeltaHeaderFieldUsageAudio(t *testing.T) {
// 	_, audioTrack := loadDeltaHeaderFieldUsageTestTracks(t)

// 	results, err := measureDeltaHeaderFieldUsage(audioTrack, 1, samplesForSeconds(audioTrack, 1), "none")
// 	if err != nil {
// 		t.Fatalf("measure audio delta header field usage: %v", err)
// 	}

// 	assertDeltaHeaderFieldUsage(t, results, []deltaHeaderFieldUsage{
// 		{
// 			AssetName:    "audio_monotonic_128kbps_aac",
// 			Protection:   "none",
// 			DeltaHeaders: 46,
// 			FieldID:      "-",
// 			FieldName:    "(none)",
// 			Appearances:  0,
// 		},
// 	})
// }

// func TestRunDeltaHeaderFieldUsageExperimentIncludesCENCAndCBCS(t *testing.T) {
// 	videoTrack, audioTrack := loadDeltaHeaderFieldUsageTestTracks(t)

// 	results, err := runDeltaHeaderFieldUsageExperiment("../../assets/test10s",
// 		videoTrack, audioTrack, 25, samplesForSeconds(audioTrack, 1))
// 	if err != nil {
// 		t.Fatalf("run delta header field usage experiment: %v", err)
// 	}

// 	assertDeltaHeaderFieldUsage(t, results, []deltaHeaderFieldUsage{
// 		{
// 			AssetName:    "video_400kbps_avc",
// 			Protection:   "none",
// 			DeltaHeaders: 24,
// 			FieldID:      "7",
// 			FieldName:    "Sample flags",
// 			Appearances:  1,
// 		},
// 		{
// 			AssetName:    "audio_monotonic_128kbps_aac",
// 			Protection:   "none",
// 			DeltaHeaders: 46,
// 			FieldID:      "-",
// 			FieldName:    "(none)",
// 			Appearances:  0,
// 		},
// 		{
// 			AssetName:    "video_400kbps_avc",
// 			Protection:   "cenc",
// 			DeltaHeaders: 24,
// 			FieldID:      "7",
// 			FieldName:    "Sample flags",
// 			Appearances:  1,
// 		},
// 		{
// 			AssetName:    "video_400kbps_avc",
// 			Protection:   "cenc",
// 			DeltaHeaders: 24,
// 			FieldID:      "13",
// 			FieldName:    "Bytes of clear data",
// 			Appearances:  22,
// 		},
// 		{
// 			AssetName:    "video_400kbps_avc",
// 			Protection:   "cenc",
// 			DeltaHeaders: 24,
// 			FieldID:      "15",
// 			FieldName:    "Bytes of protected data",
// 			Appearances:  22,
// 		},
// 		{
// 			AssetName:    "audio_monotonic_128kbps_aac",
// 			Protection:   "cenc",
// 			DeltaHeaders: 46,
// 			FieldID:      "-",
// 			FieldName:    "(none)",
// 			Appearances:  0,
// 		},
// 		{
// 			AssetName:    "video_400kbps_avc",
// 			Protection:   "cbcs",
// 			DeltaHeaders: 24,
// 			FieldID:      "7",
// 			FieldName:    "Sample flags",
// 			Appearances:  1,
// 		},
// 		{
// 			AssetName:    "video_400kbps_avc",
// 			Protection:   "cbcs",
// 			DeltaHeaders: 24,
// 			FieldID:      "13",
// 			FieldName:    "Bytes of clear data",
// 			Appearances:  5,
// 		},
// 		{
// 			AssetName:    "video_400kbps_avc",
// 			Protection:   "cbcs",
// 			DeltaHeaders: 24,
// 			FieldID:      "15",
// 			FieldName:    "Bytes of protected data",
// 			Appearances:  24,
// 		},
// 		{
// 			AssetName:    "audio_monotonic_128kbps_aac",
// 			Protection:   "cbcs",
// 			DeltaHeaders: 46,
// 			FieldID:      "-",
// 			FieldName:    "(none)",
// 			Appearances:  0,
// 		},
// 	})
// }

// func loadDeltaHeaderFieldUsageTestTracks(t *testing.T) (*internal.ContentTrack, *internal.ContentTrack) {
// 	t.Helper()

// 	asset, err := internal.LoadAsset("../../assets/test10s", 1, 1)
// 	if err != nil {
// 		t.Fatalf("load test asset: %v", err)
// 	}

// 	videoTrack := asset.GetTrackByName("video_400kbps_avc")
// 	if videoTrack == nil {
// 		t.Fatal("video_400kbps_avc track not found")
// 	}
// 	audioTrack := asset.GetTrackByName("audio_monotonic_128kbps_aac")
// 	if audioTrack == nil {
// 		t.Fatal("audio_monotonic_128kbps_aac track not found")
// 	}
// 	return videoTrack, audioTrack
// }

// func assertDeltaHeaderFieldUsage(t *testing.T, got, want []deltaHeaderFieldUsage) {
// 	t.Helper()

// 	if len(got) != len(want) {
// 		t.Fatalf("got %d rows, want %d: %#v", len(got), len(want), got)
// 	}
// 	for i := range want {
// 		if got[i] != want[i] {
// 			t.Fatalf("row %d = %#v, want %#v", i, got[i], want[i])
// 		}
// 	}
// }
