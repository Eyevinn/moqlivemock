package internal

import (
	"testing"
)

// benchProtections loads the test asset with ClearKey tracks in each scheme. The clear tracks come from the first
// asset, and the protected ones, with the "_eccp" suffix, from the asset of their scheme.
func benchProtections(b *testing.B) map[string]*Asset {
	b.Helper()
	assets := make(map[string]*Asset)
	for _, scheme := range []string{"cbcs", "cenc"} {
		eccp, err := ParseCENCflags(scheme, "39112233445566778899aabbccddeeff",
			"40112233445566778899aabbccddeeff", "41112233445566778899aabbccddeeff",
			"http://localhost:8081/clearkey")
		if err != nil {
			b.Fatal(err)
		}
		asset, err := LoadAssetWithProtection("../assets/test10s", 1, 1, nil, eccp)
		if err != nil {
			b.Fatal(err)
		}
		assets[scheme] = asset
	}
	return assets
}

// BenchmarkGenMoQGroup generates one MoQ group, one second of media, per iteration, as PublishTrack does for each
// subscription, for clear and encrypted tracks packaged as CMAF and LOCMAF.
func BenchmarkGenMoQGroup(b *testing.B) {
	assets := benchProtections(b)
	tracks := []string{"video_400kbps_avc", "video_400kbps_av1", "audio_monotonic_128kbps_aac"}
	protections := []struct {
		name, scheme, suffix string
	}{
		{"clear", "cbcs", ""},
		{"cbcs", "cbcs", "_eccp"},
		{"cenc", "cenc", "_eccp"},
	}
	for _, track := range tracks {
		for _, prot := range protections {
			for _, packaging := range []string{"cmaf", "locmaf"} {
				b.Run(track+"/"+prot.name+"/"+packaging, func(b *testing.B) {
					ct := assets[prot.scheme].GetTrackByName(track + prot.suffix)
					if ct == nil {
						b.Fatalf("no track %s%s", track, prot.suffix)
					}
					b.ReportAllocs()
					var groupNr uint64
					for b.Loop() {
						if _, err := GenMoQGroup(ct, groupNr, ct.SampleBatch, MoqGroupDurMS, packaging); err != nil {
							b.Fatal(err)
						}
						groupNr++
					}
				})
			}
		}
	}
}
