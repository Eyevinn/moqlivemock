package cc608

import (
	"testing"

	"github.com/Eyevinn/go-608/carriage"
	"github.com/Eyevinn/go-608/cta608"
	"github.com/Eyevinn/go-608/generate"
)

// TestDefaultContent checks the two-line cue content: row 13 is the UTC clock
// (HH:MM:SS.mmm, white, centered) and row 14 is "GRP <n>" (yellow, centered),
// where n is the unit number the caller passed. The clock wraps at 24 h but the
// group keeps counting. The last case pins the group to the unit number rather
// than to the timestamp, which go-608 v0.8.0 made independent inputs.
func TestDefaultContent(t *testing.T) {
	cases := []struct {
		name       string
		unitNr     int64
		cueStartMS int64
		wantTime   string
		wantGroup  string
	}{
		{"epoch", 0, 0, "00:00:00.000", "GRP 0"},
		{"whole second", 45296, 45296000, "12:34:56.000", "GRP 45296"},
		{"sub-second millis", 45296, 45296123, "12:34:56.123", "GRP 45296"},
		{"day wrap keeps group", 90061, 90061500, "01:01:01.500", "GRP 90061"},
		{"number independent of time", 7, 45296000, "12:34:56.000", "GRP 7"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			u := generate.Unit{Nr: c.unitNr, StartMS: c.cueStartMS}
			cue := DefaultContent(u, 0, c.cueStartMS)
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

// allModes is every -cc608mode value. Tests that assert a property all modes must
// share iterate this, so a mode added later is covered without being remembered.
var allModes = []Mode{ModePaintOn, ModePopOn, ModeRollUp2, ModeRollUp3, ModeRollUp4}

// decodeSchedule replays a group's envelopes through the full carriage +
// cta608.Decoder path with a fresh decoder, and reports per frame what the
// displayed screen held. Feeding one group only makes every assertion built on
// the result also an assertion that the group is self-contained.
//
// firstText is the frame at which any of the caption's characters first reach the
// screen and completeAt the frame at which both rows are finished; both are -1 if
// that never happens. The rows are those left displayed by the last frame.
func decodeSchedule(t *testing.T, envelopes [][]byte, codec Codec) (
	changes, firstText, completeAt int, gotTime, gotGroup string,
	timeColor, groupColor cta608.Color,
) {
	t.Helper()
	firstText, completeAt = -1, -1
	var dec cta608.Decoder
	for i, env := range envelopes {
		// A bare envelope is not a coded sample, so feed it to the decoder as the
		// only thing in one: an AV1 metadata OBU already is a valid (frameless)
		// OBU sequence, while an SEI NAL unit needs its 4-byte length prefix to
		// form a NAL stream.
		sample := env
		if codec != CodecAV1 {
			sample = carriage.PrefixNALUs(env)
		}
		f1, _, err := FieldPairs(sample, codec)
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
			changes++
		}
		gotTime, timeColor, _ = rowText(dec.Screen(), captionRowTime)
		gotGroup, groupColor, _ = rowText(dec.Screen(), captionRowGroup)
		if firstText < 0 && (gotTime != "" || gotGroup != "") {
			firstText = i
		}
		if completeAt < 0 && gotTime == "12:34:56.000" && gotGroup == "GRP 45296" {
			completeAt = i
		}
	}
	return changes, firstText, completeAt, gotTime, gotGroup, timeColor, groupColor
}

// TestScheduleRoundTrip builds a group's caption schedule at two frame rates in
// every mode and decodes it back through the full carriage + cta608.Decoder path
// for all three codecs, checking that the group reconstructs the expected CC1
// caption (round trip) and that returning nFrames envelopes proves no Overran
// occurred. The AV1 case exercises the metadata-OBU envelope, which carries the
// identical cc_data() and so must decode to the identical caption.
//
// Roll-up matters here beyond the round trip: it is the tightest of the three on
// the pair budget (a mode entry per cue plus a CR per line), so a caption that
// grows would fail this first, and its lines are written in Row order onto a
// scrolling window rather than positioned absolutely — the assertion that rows 13
// and 14 still end up holding the clock and the group tag is what pins that the
// window lays out the way the content declares.
func TestScheduleRoundTrip(t *testing.T) {
	const groupNr = 45296 // 12:34:56 UTC
	cases := []struct {
		name    string
		fps     float64
		nFrames int
		codec   Codec
	}{
		{"25fps avc", 25.0, 25, CodecAVC},
		{"30fps hevc", 30.0, 30, CodecHEVC},
		{"25fps av1", 25.0, 25, CodecAV1},
		{"30fps av1", 30.0, 30, CodecAV1},
	}
	for _, mode := range allModes {
		g := New(Config{Enabled: true, Mode: mode})
		for _, c := range cases {
			t.Run(mode.String()+"/"+c.name, func(t *testing.T) {
				envelopes := g.Schedule(groupNr, c.fps, c.nFrames, c.codec)
				if len(envelopes) != c.nFrames {
					t.Fatalf("got %d envelopes, want %d (nil => build error / Overran)", len(envelopes), c.nFrames)
				}

				changes, _, completeAt, gotTime, gotGroup, timeColor, groupColor :=
					decodeSchedule(t, envelopes, c.codec)
				if changes == 0 {
					t.Fatal("the caption never reached the screen")
				}
				if completeAt < 0 {
					t.Errorf("caption never completed; ended as row13 = %q, row14 = %q", gotTime, gotGroup)
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
}

// TestScheduleModeDisplayTiming pins the difference the mode choice is made for.
// Every mode finishes the caption inside its own group, but the progressive modes
// (paint-on and roll-up) start putting it on screen in the group's first frames,
// while pop-on shows nothing until its EOC flips the finished caption on around
// three-quarters through — so a progressive caption is displayed over the second
// it names, and pop-on's mostly over the following one.
//
// The frame numbers are deliberately loose bounds, not exact indices: the point
// is which half of the group the caption lands in, and an exact index would break
// on any harmless change to the caption text's length.
func TestScheduleModeDisplayTiming(t *testing.T) {
	const (
		groupNr = 45296
		fps     = 25.0
		nFrames = 25
	)
	for _, c := range []struct {
		mode Mode
		// maxFirstText bounds when any character may first appear, and
		// minCompleteAt/maxCompleteAt bracket when the caption must be finished.
		maxFirstText  int
		minCompleteAt int
		maxCompleteAt int
		wantChanges   func(int) bool
		changesDesc   string
	}{
		{
			mode:          ModePaintOn,
			maxFirstText:  nFrames / 4, // visible early in its own group
			minCompleteAt: 1,
			maxCompleteAt: nFrames - 2, // and finished with frames to spare
			wantChanges:   func(n int) bool { return n > 1 },
			changesDesc:   "more than one (the screen grows two characters at a time)",
		},
		{
			mode:          ModePopOn,
			maxFirstText:  nFrames - 1,
			minCompleteAt: nFrames / 2, // nothing on screen until the late flip
			maxCompleteAt: nFrames - 1,
			wantChanges:   func(n int) bool { return n == 1 },
			changesDesc:   "exactly one (the whole caption flips on at once)",
		},
		// Roll-up types onto a scrolling window, so it animates like paint-on. It
		// pays a CR per line on top of paint-on's two control pairs, which is why
		// it may finish later — but still inside its own group. All three window
		// sizes carry the same two lines and so share these bounds; only the RU
		// code on the wire differs.
		{
			mode:          ModeRollUp2,
			maxFirstText:  nFrames / 4,
			minCompleteAt: 1,
			maxCompleteAt: nFrames - 1,
			wantChanges:   func(n int) bool { return n > 1 },
			changesDesc:   "more than one (the base row grows two characters at a time)",
		},
		{
			mode:          ModeRollUp3,
			maxFirstText:  nFrames / 4,
			minCompleteAt: 1,
			maxCompleteAt: nFrames - 1,
			wantChanges:   func(n int) bool { return n > 1 },
			changesDesc:   "more than one (the base row grows two characters at a time)",
		},
		{
			mode:          ModeRollUp4,
			maxFirstText:  nFrames / 4,
			minCompleteAt: 1,
			maxCompleteAt: nFrames - 1,
			wantChanges:   func(n int) bool { return n > 1 },
			changesDesc:   "more than one (the base row grows two characters at a time)",
		},
	} {
		t.Run(c.mode.String(), func(t *testing.T) {
			g := New(Config{Enabled: true, Mode: c.mode})
			envelopes := g.Schedule(groupNr, fps, nFrames, CodecAVC)
			if len(envelopes) != nFrames {
				t.Fatalf("got %d envelopes, want %d", len(envelopes), nFrames)
			}
			changes, firstText, completeAt, _, _, _, _ := decodeSchedule(t, envelopes, CodecAVC)

			if !c.wantChanges(changes) {
				t.Errorf("screen changed %d times, want %s", changes, c.changesDesc)
			}
			if firstText < 0 || firstText > c.maxFirstText {
				t.Errorf("caption first visible at frame %d, want 0..%d of %d",
					firstText, c.maxFirstText, nFrames)
			}
			if completeAt < c.minCompleteAt || completeAt > c.maxCompleteAt {
				t.Errorf("caption complete at frame %d, want %d..%d of %d",
					completeAt, c.minCompleteAt, c.maxCompleteAt, nFrames)
			}
		})
	}
}

// TestScheduleSelfContained is the independent-group guarantee: consecutive
// groups each carry their own whole caption, so a fresh decoder fed one group in
// isolation — a subscriber joining there — reconstructs that group's caption and
// no other. No mode is passed a cross-unit option, so this must hold for every
// one of them.
//
// The two options omitted are the two ways it could break, and each belongs to a
// different mode: WithFlipAtCueStart would put a pop-on group's build in its
// predecessor (the trade #118 proposed), and WithRollUpCarry would leave a
// roll-up group's upper rows holding its predecessor's lines. Running the whole
// mode set through this is what keeps either from being adopted unnoticed.
func TestScheduleSelfContained(t *testing.T) {
	const (
		firstGroup = 45296 // 12:34:56 UTC
		fps        = 25.0
		nFrames    = 25
	)
	for _, mode := range allModes {
		for _, codec := range []Codec{CodecAVC, CodecHEVC, CodecAV1} {
			t.Run(mode.String()+"/"+codec.String(), func(t *testing.T) {
				g := New(Config{Enabled: true, Mode: mode})
				for offset, want := range []struct{ time, group string }{
					{"12:34:56.000", "GRP 45296"},
					{"12:34:57.000", "GRP 45297"},
					{"12:34:58.000", "GRP 45298"},
				} {
					groupNr := int64(firstGroup + offset)
					envelopes := g.Schedule(groupNr, fps, nFrames, codec)
					if len(envelopes) != nFrames {
						t.Fatalf("group %d: got %d envelopes, want %d", groupNr, len(envelopes), nFrames)
					}
					// A fresh decoder per group: no state carried in from the
					// previous one, as for a subscriber joining at this group.
					_, _, _, gotTime, gotGroup, _, _ := decodeSchedule(t, envelopes, codec)
					if gotTime != want.time || gotGroup != want.group {
						t.Errorf("group %d decoded to %q / %q, want %q / %q",
							groupNr, gotTime, gotGroup, want.time, want.group)
					}
				}
			})
		}
	}
}

// TestScheduleDisabled checks that captions-off cases return nil, for every
// codec (the early-outs precede the envelope choice, so AV1 must behave the
// same as the SEI codecs).
func TestScheduleDisabled(t *testing.T) {
	for _, codec := range []Codec{CodecAVC, CodecHEVC, CodecAV1} {
		t.Run(codec.String(), func(t *testing.T) {
			var nilGen *Generator
			if got := nilGen.Schedule(1, 30.0, 30, codec); got != nil {
				t.Errorf("nil Generator: got %d envelopes, want nil", len(got))
			}
			off := New(Config{Enabled: false})
			if got := off.Schedule(1, 30.0, 30, codec); got != nil {
				t.Errorf("disabled Generator: got %d envelopes, want nil", len(got))
			}
			on := New(Config{Enabled: true})
			if got := on.Schedule(1, 30.0, 0, codec); got != nil {
				t.Errorf("zero frames: got %d envelopes, want nil", len(got))
			}
			// An out-of-range fps makes BuildUnitCues error; Schedule degrades to nil.
			if got := on.Schedule(1, 5.0, 30, codec); got != nil {
				t.Errorf("bad fps: got %d envelopes, want nil", len(got))
			}
		})
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
		wantCodec Codec
		wantOK    bool
	}{
		{"avc1.640028", CodecAVC, true},
		{"avc3.42e01e", CodecAVC, true},
		{"hev1.2.4.L120.90", CodecHEVC, true},
		{"hvc1.1.6.L93.90", CodecHEVC, true},
		{"av01.0.05M.08", CodecAV1, true},
		{"mp4a.40.2", 0, false},
		{"stpp.ttml.im1t", 0, false},
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

// TestParseMode covers the -cc608mode flag values, including the empty string a
// caller passes for an unset flag, which must select the default.
func TestParseMode(t *testing.T) {
	for _, c := range []struct {
		in      string
		want    Mode
		wantErr bool
	}{
		{"", ModePaintOn, false},
		{"paint-on", ModePaintOn, false},
		{"pop-on", ModePopOn, false},
		{"roll-up2", ModeRollUp2, false},
		{"roll-up3", ModeRollUp3, false},
		{"roll-up4", ModeRollUp4, false},
		{"roll-up", ModeRollUp2, false}, // go608-clock's spelling; 2 is its zero value
		{"paint_on", 0, true},
		{"PAINT-ON", 0, true},
		{"roll-up5", 0, true}, // outside go-608's 2..4 window range
		{"roll-up1", 0, true},
	} {
		got, err := ParseMode(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("ParseMode(%q) = %v, want an error", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseMode(%q): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParseMode(%q) = %v, want %v", c.in, got, c.want)
		}
	}

	// The zero Mode must be the default, so Config{Enabled: true} means paint-on.
	if New(Config{Enabled: true}).Mode() != ModePaintOn {
		t.Errorf("default Mode = %v, want %v", New(Config{Enabled: true}).Mode(), ModePaintOn)
	}
	if names := ModeNames(); len(names) != 5 || names[0] != "paint-on" {
		t.Errorf("ModeNames() = %v, want 5 canonical names with the default first", names)
	}
	// String must give back the canonical spelling, never the "roll-up" alias.
	for _, c := range []struct {
		mode Mode
		want string
	}{
		{ModePaintOn, "paint-on"}, {ModePopOn, "pop-on"},
		{ModeRollUp2, "roll-up2"}, {ModeRollUp3, "roll-up3"}, {ModeRollUp4, "roll-up4"},
	} {
		if got := c.mode.String(); got != c.want {
			t.Errorf("Mode.String() = %q, want %q", got, c.want)
		}
	}
	// The window size must match the mode's name, since it picks the RU code.
	for _, c := range []struct {
		mode     Mode
		wantRows int
		wantOK   bool
	}{
		{ModeRollUp2, 2, true}, {ModeRollUp3, 3, true}, {ModeRollUp4, 4, true},
		{ModePaintOn, 0, false}, {ModePopOn, 0, false},
	} {
		if rows, ok := c.mode.rollUpRows(); rows != c.wantRows || ok != c.wantOK {
			t.Errorf("%v.rollUpRows() = %d, %v; want %d, %v", c.mode, rows, ok, c.wantRows, c.wantOK)
		}
	}
	if got := Mode(42).String(); got != "Mode(42)" {
		t.Errorf("Mode(42).String() = %q", got)
	}
}
