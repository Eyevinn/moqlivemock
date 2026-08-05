package cc608

import "fmt"

// Mode selects how a group's caption reaches the screen. Both modes produce one
// self-contained cue per MoQ group and differ only in what the decoder does with
// the pairs as they arrive.
//
// The distinction matters because a MoQ group is one wall-clock second and
// carries exactly one cue, so a caption's display interval is decided entirely
// by where inside its group the visible transition falls:
//
//   - ModePaintOn writes onto the displayed screen, two characters per frame, so
//     the caption starts appearing on the group's first frame and is complete for
//     the group's tail. Display therefore coincides with the second the caption
//     names.
//   - ModePopOn builds in non-displayed memory and flips the whole caption on
//     with an EOC. The build drains one pair per frame, so the flip lands around
//     frame 18 of 25 and the caption is displayed from three-quarters through the
//     group it names until the same point in the next one. Aligning it would mean
//     spending the previous group's frames on this group's build (go-608's
//     WithFlipAtCueStart), which is exactly the cross-group dependency the
//     independent-group model avoids.
//
// Both keep every group independently decodable: a subscriber joining at an
// arbitrary group gets that group's whole caption from that group's samples.
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
)

// modeNames pairs each Mode with its flag spelling. The slice, not a map, so
// ModeNames' output order is fixed and puts the default first.
var modeNames = []struct {
	mode Mode
	name string
}{
	{ModePaintOn, "paint-on"},
	{ModePopOn, "pop-on"},
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
// (ModePaintOn) so that a caller can pass an unset flag straight through.
func ParseMode(s string) (Mode, error) {
	if s == "" {
		return ModePaintOn, nil
	}
	for _, mn := range modeNames {
		if mn.name == s {
			return mn.mode, nil
		}
	}
	return 0, fmt.Errorf("unknown CTA-608 caption mode %q, expected one of %v", s, ModeNames())
}

// ModeNames lists the accepted flag values, default first, for flag help and
// error messages.
func ModeNames() []string {
	names := make([]string, 0, len(modeNames))
	for _, mn := range modeNames {
		names = append(names, mn.name)
	}
	return names
}
