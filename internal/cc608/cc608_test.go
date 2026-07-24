package cc608

import (
	"testing"

	"github.com/Eyevinn/go-608/carriage"
	"github.com/Eyevinn/go-608/cta608"
)

// TestDefaultContent checks the two-line cue content: row 13 is the UTC clock
// (HH:MM:SS.mmm, white, centered) and row 14 is "GRP <n>" (yellow, centered),
// where n = cueStartMS/1000. The clock wraps at 24 h but the group keeps counting.
func TestDefaultContent(t *testing.T) {
	cases := []struct {
		name       string
		cueStartMS int64
		wantTime   string
		wantGroup  string
	}{
		{"epoch", 0, "00:00:00.000", "GRP 0"},
		{"whole second", 45296000, "12:34:56.000", "GRP 45296"},
		{"sub-second millis", 45296123, "12:34:56.123", "GRP 45296"},
		{"day wrap keeps group", 90061500, "01:01:01.500", "GRP 90061"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cue := DefaultContent(0, c.cueStartMS)
			if len(cue.Lines) != 2 {
				t.Fatalf("got %d lines, want 2", len(cue.Lines))
			}
			assertLine(t, "row13", cue.Lines[0], captionRowTime, cta608.White, c.wantTime)
			assertLine(t, "row14", cue.Lines[1], captionRowGroup, cta608.Yellow, c.wantGroup)
		})
	}
}

// assertLine checks a Line's row, center alignment, single run text and color.
func assertLine(t *testing.T, name string, ln cta608.Line, row int, color cta608.Color, text string) {
	t.Helper()
	if ln.Row != row {
		t.Errorf("%s: row = %d, want %d", name, ln.Row, row)
	}
	if ln.Align != cta608.AlignCenter {
		t.Errorf("%s: align = %v, want center", name, ln.Align)
	}
	if len(ln.Runs) != 1 {
		t.Fatalf("%s: got %d runs, want 1", name, len(ln.Runs))
	}
	if ln.Runs[0].Text != text {
		t.Errorf("%s: text = %q, want %q", name, ln.Runs[0].Text, text)
	}
	if ln.Runs[0].Pen.Color != color {
		t.Errorf("%s: color = %v, want %v", name, ln.Runs[0].Pen.Color, color)
	}
}

// TestSEIScheduleRoundTrip builds a group's SEI schedule at two frame rates and
// decodes it back through the full carriage + cta608.Decoder path, checking that
// the group's single pop-on flip reconstructs the expected CC1 caption (round
// trip) and that returning nFrames NALs proves no Overran occurred.
func TestSEIScheduleRoundTrip(t *testing.T) {
	const groupNr = 45296 // 12:34:56 UTC
	cases := []struct {
		name    string
		fps     float64
		nFrames int
		codec   carriage.Codec
	}{
		{"25fps avc", 25.0, 25, carriage.CodecAVC},
		{"30fps hevc", 30.0, 30, carriage.CodecHEVC},
	}
	g := New(Config{Enabled: true})
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			seis := g.SEISchedule(groupNr, c.fps, c.nFrames, c.codec)
			if len(seis) != c.nFrames {
				t.Fatalf("got %d SEI NALs, want %d (nil => build error / Overran)", len(seis), c.nFrames)
			}

			var dec cta608.Decoder
			flips := 0
			var gotTime, gotGroup string
			var timeColor, groupColor cta608.Color
			for i, nalu := range seis {
				f1, _, err := carriage.FieldPairs([][]byte{nalu}, c.codec)
				if err != nil {
					t.Fatalf("frame %d FieldPairs: %v", i, err)
				}
				if len(f1) == 0 {
					continue
				}
				if err := dec.Feed(f1); err != nil {
					t.Fatalf("frame %d decode: %v", i, err)
				}
				if dec.Changed() {
					flips++
					gotTime, timeColor, _ = rowText(dec.Screen(), captionRowTime)
					gotGroup, groupColor, _ = rowText(dec.Screen(), captionRowGroup)
				}
			}
			if flips != 1 {
				t.Fatalf("got %d on-screen flips, want 1 (one cue per group)", flips)
			}
			if gotTime != "12:34:56.000" {
				t.Errorf("row13 = %q, want %q", gotTime, "12:34:56.000")
			}
			if gotGroup != "GRP 45296" {
				t.Errorf("row14 = %q, want %q", gotGroup, "GRP 45296")
			}
			if timeColor != cta608.White {
				t.Errorf("row13 color = %v, want white", timeColor)
			}
			if groupColor != cta608.Yellow {
				t.Errorf("row14 color = %v, want yellow", groupColor)
			}
		})
	}
}

// TestSEIScheduleDisabled checks that captions-off cases return nil.
func TestSEIScheduleDisabled(t *testing.T) {
	var nilGen *Generator
	if got := nilGen.SEISchedule(1, 30.0, 30, carriage.CodecAVC); got != nil {
		t.Errorf("nil Generator: got %d NALs, want nil", len(got))
	}
	off := New(Config{Enabled: false})
	if got := off.SEISchedule(1, 30.0, 30, carriage.CodecAVC); got != nil {
		t.Errorf("disabled Generator: got %d NALs, want nil", len(got))
	}
	on := New(Config{Enabled: true})
	if got := on.SEISchedule(1, 30.0, 0, carriage.CodecAVC); got != nil {
		t.Errorf("zero frames: got %d NALs, want nil", len(got))
	}
	// An out-of-range fps makes BuildUnitCues error; SEISchedule degrades to nil.
	if got := on.SEISchedule(1, 5.0, 30, carriage.CodecAVC); got != nil {
		t.Errorf("bad fps: got %d NALs, want nil", len(got))
	}
}

// TestNewDefaults checks the CC1/"eng"/DefaultContent defaults and accessors.
func TestNewDefaults(t *testing.T) {
	g := New(Config{Enabled: true})
	if !g.Enabled() {
		t.Error("Enabled() = false, want true")
	}
	if g.Channel() != 1 {
		t.Errorf("Channel() = %d, want 1 (CC1)", g.Channel())
	}
	if g.Lang() != "eng" {
		t.Errorf("Lang() = %q, want \"eng\"", g.Lang())
	}
	var nilGen *Generator
	if nilGen.Enabled() {
		t.Error("nil Generator Enabled() = true, want false")
	}
}

func TestCodecFor(t *testing.T) {
	cases := []struct {
		codec     string
		wantCodec carriage.Codec
		wantOK    bool
	}{
		{"avc1.640028", carriage.CodecAVC, true},
		{"avc3.42e01e", carriage.CodecAVC, true},
		{"hev1.2.4.L120.90", carriage.CodecHEVC, true},
		{"hvc1.1.6.L93.90", carriage.CodecHEVC, true},
		{"av01.0.05M.08", 0, false},
		{"mp4a.40.2", 0, false},
		{"", 0, false},
	}
	for _, c := range cases {
		gotCodec, gotOK := CodecFor(c.codec)
		if gotOK != c.wantOK {
			t.Errorf("CodecFor(%q) ok = %v, want %v", c.codec, gotOK, c.wantOK)
		}
		if gotOK && gotCodec != c.wantCodec {
			t.Errorf("CodecFor(%q) codec = %v, want %v", c.codec, gotCodec, c.wantCodec)
		}
	}
}

// rowText returns the concatenated text and color of a decoded screen row.
func rowText(s cta608.Screen, idx int) (text string, color cta608.Color, ok bool) {
	for _, r := range s.Rows {
		if r.Index != idx {
			continue
		}
		color = cta608.ColDefault
		for i, run := range r.Runs {
			if i == 0 {
				color = run.Pen.Color
			}
			text += run.Text
		}
		return text, color, true
	}
	return "", cta608.ColDefault, false
}
