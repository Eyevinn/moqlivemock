package internal

import (
	"strings"
	"testing"

	"github.com/Eyevinn/mp4ff/bits"
	"github.com/Eyevinn/mp4ff/mp4"
)

func TestNewSubtitleTrack(t *testing.T) {
	tests := []struct {
		name     string
		format   SubtitleFormat
		lang     string
		wantName string
	}{
		{
			name:     "subs_wvtt_en",
			format:   SubtitleFormatWVTT,
			lang:     "en",
			wantName: "subs_wvtt_en",
		},
		{
			name:     "subs_stpp_sv",
			format:   SubtitleFormatSTPP,
			lang:     "sv",
			wantName: "subs_stpp_sv",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st, err := NewSubtitleTrack(tc.name, tc.format, tc.lang)
			if err != nil {
				t.Fatalf("NewSubtitleTrack failed: %v", err)
			}
			if st.Name != tc.wantName {
				t.Errorf("Name = %q, want %q", st.Name, tc.wantName)
			}
			if st.Format != tc.format {
				t.Errorf("Format = %q, want %q", st.Format, tc.format)
			}
			if st.Language != tc.lang {
				t.Errorf("Language = %q, want %q", st.Language, tc.lang)
			}
			if st.TimeScale != SubsTimeTimescale {
				t.Errorf("TimeScale = %d, want %d", st.TimeScale, SubsTimeTimescale)
			}
			if st.CueDurMS != DefaultCueDurMS {
				t.Errorf("CueDurMS = %d, want %d", st.CueDurMS, DefaultCueDurMS)
			}
		})
	}
}

func TestSubtitleDataCodec(t *testing.T) {
	tests := []struct {
		format    SubtitleFormat
		wantCodec string
	}{
		{SubtitleFormatWVTT, "wvtt"},
		{SubtitleFormatSTPP, "stpp.ttml.im1t"},
	}

	for _, tc := range tests {
		t.Run(string(tc.format), func(t *testing.T) {
			sd := &SubtitleData{format: tc.format, language: "en"}
			if got := sd.Codec(); got != tc.wantCodec {
				t.Errorf("Codec() = %q, want %q", got, tc.wantCodec)
			}
		})
	}
}

func TestWvttInitSegment(t *testing.T) {
	st, err := NewSubtitleTrack("test_wvtt", SubtitleFormatWVTT, "en")
	if err != nil {
		t.Fatalf("NewSubtitleTrack failed: %v", err)
	}

	data, err := st.SpecData.GenCMAFInitData()
	if err != nil {
		t.Fatalf("GenCMAFInitData failed: %v", err)
	}

	if len(data) == 0 {
		t.Fatal("Init segment data is empty")
	}

	// Verify it's valid MP4
	sr := bits.NewFixedSliceReader(data)
	mp4d, err := mp4.DecodeFileSR(sr)
	if err != nil {
		t.Fatalf("Failed to decode init segment: %v", err)
	}

	// Check track properties
	if mp4d.Moov == nil || mp4d.Moov.Trak == nil {
		t.Fatal("Init segment missing moov/trak")
	}

	ts := mp4d.Moov.Trak.Mdia.Mdhd.Timescale
	if ts != SubsTimeTimescale {
		t.Errorf("Timescale = %d, want %d", ts, SubsTimeTimescale)
	}

	lang := mp4d.Moov.Trak.Mdia.Elng.Language
	if lang != "en" {
		t.Errorf("Language = %q, want %q", lang, "en")
	}
}

func TestStppInitSegment(t *testing.T) {
	st, err := NewSubtitleTrack("test_stpp", SubtitleFormatSTPP, "sv")
	if err != nil {
		t.Fatalf("NewSubtitleTrack failed: %v", err)
	}

	data, err := st.SpecData.GenCMAFInitData()
	if err != nil {
		t.Fatalf("GenCMAFInitData failed: %v", err)
	}

	if len(data) == 0 {
		t.Fatal("Init segment data is empty")
	}

	// Verify it's valid MP4
	sr := bits.NewFixedSliceReader(data)
	mp4d, err := mp4.DecodeFileSR(sr)
	if err != nil {
		t.Fatalf("Failed to decode init segment: %v", err)
	}

	// Check track properties
	if mp4d.Moov == nil || mp4d.Moov.Trak == nil {
		t.Fatal("Init segment missing moov/trak")
	}

	ts := mp4d.Moov.Trak.Mdia.Mdhd.Timescale
	if ts != SubsTimeTimescale {
		t.Errorf("Timescale = %d, want %d", ts, SubsTimeTimescale)
	}

	lang := mp4d.Moov.Trak.Mdia.Elng.Language
	if lang != "sv" {
		t.Errorf("Language = %q, want %q", lang, "sv")
	}
}

func TestCalcCueItvls(t *testing.T) {
	tests := []struct {
		desc     string
		startMS  int
		dur      int
		utcMS    int
		cueDurMS int
		wanted   []cueItvl
	}{
		{
			desc:     "long cue",
			startMS:  0,
			dur:      2000,
			utcMS:    0,
			cueDurMS: 1800,
			wanted: []cueItvl{
				{startMS: 0, endMS: 1800, utcS: 0},
			},
		},
		{
			desc:     "simple case w 2 cues",
			startMS:  0,
			dur:      2000,
			utcMS:    0,
			cueDurMS: 900,
			wanted: []cueItvl{
				{startMS: 0, endMS: 900, utcS: 0},
				{startMS: 1000, endMS: 1900, utcS: 1},
			},
		},
		{
			desc:     "simple case w 1 cue",
			startMS:  0,
			dur:      1000,
			utcMS:    0,
			cueDurMS: 900,
			wanted: []cueItvl{
				{startMS: 0, endMS: 900, utcS: 0},
			},
		},
		{
			desc:     "utc shifted, starting 100ms into second",
			startMS:  12000,
			dur:      800,
			utcMS:    12100,
			cueDurMS: 900,
			wanted: []cueItvl{
				{startMS: 12000, endMS: 12800, utcS: 12},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			got := calcCueItvls(tc.startMS, tc.dur, tc.utcMS, tc.cueDurMS)
			if len(got) != len(tc.wanted) {
				t.Fatalf("calcCueItvls returned %d intervals, want %d", len(got), len(tc.wanted))
			}
			for i, want := range tc.wanted {
				if got[i] != want {
					t.Errorf("interval[%d] = %+v, want %+v", i, got[i], want)
				}
			}
		})
	}
}

func TestMsToTTMLTime(t *testing.T) {
	tests := []struct {
		ms     int
		wanted string
	}{
		{0, "00:00:00.000"},
		{1000, "00:00:01.000"},
		{60000, "00:01:00.000"},
		{3600000, "01:00:00.000"},
		{36605230, "10:10:05.230"},
	}

	for _, tc := range tests {
		got := msToTTMLTime(tc.ms)
		if got != tc.wanted {
			t.Errorf("msToTTMLTime(%d) = %q, want %q", tc.ms, got, tc.wanted)
		}
	}
}

func TestMakeStppMessage(t *testing.T) {
	tests := []struct {
		lang    string
		utcMS   int
		groupNr int
		want    string
	}{
		{"en", 0, 0, "1970-01-01T00:00:00Z<br/>en # 0"},
		{"sv", 1000000, 1000, "1970-01-01T00:16:40Z<br/>sv # 1000"},
	}

	for _, tc := range tests {
		got := makeStppMessage(tc.lang, tc.utcMS, tc.groupNr)
		if got != tc.want {
			t.Errorf("makeStppMessage(%q, %d, %d) = %q, want %q", tc.lang, tc.utcMS, tc.groupNr, got, tc.want)
		}
	}
}

func TestGenSubtitleGroupWvtt(t *testing.T) {
	st, err := NewSubtitleTrack("test_wvtt", SubtitleFormatWVTT, "en")
	if err != nil {
		t.Fatalf("NewSubtitleTrack failed: %v", err)
	}

	groupNr := uint64(1000) // Group number corresponding to 1000 seconds
	groupDurMS := uint32(1000)

	mg, err := GenSubtitleGroup(st, groupNr, groupDurMS)
	if err != nil {
		t.Fatalf("GenSubtitleGroup failed: %v", err)
	}

	if mg == nil {
		t.Fatal("GenSubtitleGroup returned nil")
	}

	if len(mg.MoQObjects) != 1 {
		t.Errorf("MoQObjects count = %d, want 1", len(mg.MoQObjects))
	}

	// Verify it's valid MP4
	data := mg.MoQObjects[0]
	sr := bits.NewFixedSliceReader(data)
	mp4d, err := mp4.DecodeFileSR(sr)
	if err != nil {
		t.Fatalf("Failed to decode media segment: %v", err)
	}

	if len(mp4d.Segments) != 1 {
		t.Fatalf("Expected 1 segment, got %d", len(mp4d.Segments))
	}

	seg := mp4d.Segments[0]
	if len(seg.Fragments) != 1 {
		t.Fatalf("Expected 1 fragment, got %d", len(seg.Fragments))
	}

	frag := seg.Fragments[0]
	expectedTime := groupNr * uint64(groupDurMS)
	if frag.Moof.Traf.Tfdt.BaseMediaDecodeTime() != expectedTime {
		t.Errorf("BaseMediaDecodeTime = %d, want %d", frag.Moof.Traf.Tfdt.BaseMediaDecodeTime(), expectedTime)
	}
}

func TestGenSubtitleGroupStpp(t *testing.T) {
	st, err := NewSubtitleTrack("test_stpp", SubtitleFormatSTPP, "en")
	if err != nil {
		t.Fatalf("NewSubtitleTrack failed: %v", err)
	}

	groupNr := uint64(1000)
	groupDurMS := uint32(1000)

	mg, err := GenSubtitleGroup(st, groupNr, groupDurMS)
	if err != nil {
		t.Fatalf("GenSubtitleGroup failed: %v", err)
	}

	if mg == nil {
		t.Fatal("GenSubtitleGroup returned nil")
	}

	if len(mg.MoQObjects) != 1 {
		t.Errorf("MoQObjects count = %d, want 1", len(mg.MoQObjects))
	}

	// Verify it's valid MP4
	data := mg.MoQObjects[0]
	sr := bits.NewFixedSliceReader(data)
	mp4d, err := mp4.DecodeFileSR(sr)
	if err != nil {
		t.Fatalf("Failed to decode media segment: %v", err)
	}

	if len(mp4d.Segments) != 1 {
		t.Fatalf("Expected 1 segment, got %d", len(mp4d.Segments))
	}

	seg := mp4d.Segments[0]
	if len(seg.Fragments) != 1 {
		t.Fatalf("Expected 1 fragment, got %d", len(seg.Fragments))
	}

	frag := seg.Fragments[0]
	fss, err := frag.GetFullSamples(nil)
	if err != nil {
		t.Fatalf("GetFullSamples failed: %v", err)
	}

	// STPP should have exactly 1 sample with TTML content
	if len(fss) != 1 {
		t.Errorf("Expected 1 sample, got %d", len(fss))
	}

	// Verify TTML content
	payload := string(fss[0].Data)
	if !strings.Contains(payload, "<?xml") {
		t.Error("STPP payload doesn't contain XML declaration")
	}
	if !strings.Contains(payload, "http://www.w3.org/ns/ttml") {
		t.Error("STPP payload doesn't contain TTML namespace")
	}
	if !strings.Contains(payload, "xml:lang=\"en\"") {
		t.Error("STPP payload doesn't contain correct language")
	}
}

func TestCurrSubtitleGroupNr(t *testing.T) {
	tests := []struct {
		nowMS      uint64
		groupDurMS uint32
		want       uint64
	}{
		{0, 1000, 0},
		{999, 1000, 0},
		{1000, 1000, 1},
		{1001, 1000, 1},
		{5000, 1000, 5},
	}

	for _, tc := range tests {
		got := CurrSubtitleGroupNr(tc.nowMS, tc.groupDurMS)
		if got != tc.want {
			t.Errorf("CurrSubtitleGroupNr(%d, %d) = %d, want %d", tc.nowMS, tc.groupDurMS, got, tc.want)
		}
	}
}
