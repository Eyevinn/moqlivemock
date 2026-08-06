package cc608

import "fmt"

// Mode selects how a group's caption reaches the screen. Every mode produces one
// self-contained cue per MoQ group and they differ only in what the decoder does
// with the pairs as they arrive.
//
// The distinction matters because a MoQ group is one wall-clock second and
// carries exactly one cue, so a caption's display interval is decided entirely
// by where inside its group the visible transition falls:
//
//   - ModePaintOn writes onto the displayed screen, two characters per frame, so
//     the caption starts appearing on the group's first frame and is complete for
//     the group's tail. Display therefore coincides with the second the caption
//     names.
//   - ModeRollUp2/3/4 type onto the base row the same way, but scroll the window
//     up before each line instead of clearing the whole screen. Same timing as
//     paint-on; what differs is the RU2/RU3/RU4 mode code on the wire and the
//     window it asks the decoder to keep.
//   - ModePopOn builds in non-displayed memory and flips the whole caption on
//     with an EOC. The build drains one pair per frame, so the flip lands around
//     frame 18 of 25 and the caption is displayed from three-quarters through the
//     group it names until the same point in the next one. Aligning it would mean
//     spending the previous group's frames on this group's build (go-608's
//     WithFlipAtCueStart), which is exactly the cross-group dependency the
//     independent-group model avoids.
//
// All of them keep every group independently decodable: a subscriber joining at
// an arbitrary group gets that group's whole caption from that group's samples.
// For roll-up that takes go-608's default window reset — the unit clears the
// window on its own first frame — rather than WithRollUpCarry, which would make a
// group's picture depend on its predecessors.
type Mode int

const (
	// ModePaintOn paints the caption on progressively, two characters per frame,
	// after clearing the screen on the group's first frame. The default: display
	// lines up with the group, and the visible typing doubles as a liveness tell.
	ModePaintOn Mode = iota
	// ModePopOn builds the caption in non-displayed memory and flips it on whole,
	// the classic broadcast style. Displayed late relative to its own group; see
	// the Mode doc.
	ModePopOn
	// ModeRollUp2 types the caption into a 2-row roll-up window (RU2).
	ModeRollUp2
	// ModeRollUp3 is ModeRollUp2 with a 3-row window (RU3).
	ModeRollUp3
	// ModeRollUp4 is ModeRollUp2 with a 4-row window (RU4).
	ModeRollUp4
)

// modeNames pairs each Mode with its canonical flag spelling. The slice, not a
// map, so ModeNames' output order is fixed and puts the default first.
var modeNames = []struct {
	mode Mode
	name string
}{
	{ModePaintOn, "paint-on"},
	{ModePopOn, "pop-on"},
	{ModeRollUp2, "roll-up2"},
	{ModeRollUp3, "roll-up3"},
	{ModeRollUp4, "roll-up4"},
}

// rollUpRows returns the roll-up window size for a roll-up Mode, and ok=false for
// the pop-on and paint-on modes, which have no window.
func (m Mode) rollUpRows() (rows int, ok bool) {
	switch m {
	case ModeRollUp2:
		return 2, true
	case ModeRollUp3:
		return 3, true
	case ModeRollUp4:
		return 4, true
	default:
		return 0, false
	}
}

func (m Mode) String() string {
	for _, mn := range modeNames {
		if mn.mode == m {
			return mn.name
		}
	}
	return fmt.Sprintf("Mode(%d)", int(m))
}

// ParseMode maps a flag value onto a Mode. An empty string selects the default
// (ModePaintOn) so that a caller can pass an unset flag straight through. Bare
// "roll-up" means "roll-up2", matching go-608's own go608-clock, whose -mode takes
// roll-up[2-4] with 2 as the zero value.
func ParseMode(s string) (Mode, error) {
	if s == "" {
		return ModePaintOn, nil
	}
	if s == "roll-up" {
		return ModeRollUp2, nil
	}
	for _, mn := range modeNames {
		if mn.name == s {
			return mn.mode, nil
		}
	}
	return 0, fmt.Errorf("unknown CTA-608 caption mode %q, expected one of %v (or \"roll-up\" for roll-up2)",
		s, ModeNames())
}

// ModeNames lists the canonical flag values, default first, for flag help and
// error messages. The "roll-up" alias is deliberately left out: it is accepted,
// but listing it would suggest a fourth distinct mode.
func ModeNames() []string {
	names := make([]string, 0, len(modeNames))
	for _, mn := range modeNames {
		names = append(names, mn.name)
	}
	return names
}
