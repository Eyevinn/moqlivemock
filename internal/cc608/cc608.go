// Package cc608 generates in-band CTA-608 closed captions for moqlivemock video.
//
// It is a thin, serve-path-agnostic wrapper over the Eyevinn/go-608 library. For
// one MoQ group (== one wall-clock second) it builds a self-contained pop-on
// caption via go-608's per-unit cue mechanism and returns one caption envelope
// per video frame. The caller splices each envelope into the frame's coded
// sample with Splice. Wiring this into the publisher/catalog is a separate
// concern; this package only produces the caption bytes.
//
// The three supported codecs share one cc_data() payload but not one envelope:
// AVC and HEVC carry it in an SEI NAL unit, AV1 in a metadata_itu_t_t35 OBU.
// go-608 exposes those as two parallel APIs and deliberately keeps its own
// carriage.Codec two-valued (it names NAL framing, which AV1 has none of), so
// this package owns the three-value Codec discriminator and dispatches on it.
package cc608

import (
	"fmt"
	"strings"
	"time"

	"github.com/Eyevinn/go-608/carriage"
	"github.com/Eyevinn/go-608/cta608"
	"github.com/Eyevinn/go-608/generate"
)

// targetPeriodMS is the nominal caption update period handed to go-608. A MoQ
// group is one second (MoqGroupDurMS), so with a 1000 ms period NumCues resolves
// to exactly one cue per group: one self-contained pop-on caption per group.
const targetPeriodMS = 1000

// Caption rows (1..15, 15 = bottom). The two lines sit near the bottom but leave
// row 15 free. Row 13 carries the white UTC clock, row 14 the yellow group tag.
const (
	captionRowTime  = 13 // white: UTC wall-clock HH:MM:SS.mmm
	captionRowGroup = 14 // yellow: "GRP <n>"
)

// defaultChannel is CC1 (the primary field-1 caption service). go-608's
// BuildUnitCues always serializes CC1 on field 1, so this is currently a
// descriptive setting (used by the catalog/consumer, not by Schedule).
const defaultChannel = 1

// defaultLang is the BCP-47-ish language tag advertised for the caption service.
const defaultLang = "eng"

// Config configures a caption Generator. The zero value (Enabled false) yields a
// disabled generator; Channel and Lang default to CC1/"eng" when left zero/empty,
// and Content defaults to DefaultContent when nil.
type Config struct {
	// Enabled turns caption generation on. A disabled Generator produces no
	// caption envelopes.
	Enabled bool
	// Channel is the CEA-608 caption channel (1 == CC1). Informational: go-608
	// emits CC1 on field 1 regardless.
	Channel int
	// Lang is the caption language tag advertised in the catalog (e.g. "eng").
	Lang string
	// Content formats each cue's lines. Nil selects DefaultContent.
	Content generate.CueContentFunc
}

// Generator produces per-frame CTA-608 caption envelopes for MoQ groups. A nil
// *Generator is a valid, disabled generator: Schedule returns nil for it. This
// lets a consumer hold a possibly-nil *Generator to mean "captions off" without
// a branch.
type Generator struct {
	enabled bool
	channel int
	lang    string
	content generate.CueContentFunc
}

// New returns a Generator from cfg, filling in the CC1/"eng"/DefaultContent
// defaults for any zero-valued setting.
func New(cfg Config) *Generator {
	content := cfg.Content
	if content == nil {
		content = DefaultContent
	}
	channel := cfg.Channel
	if channel == 0 {
		channel = defaultChannel
	}
	lang := cfg.Lang
	if lang == "" {
		lang = defaultLang
	}
	return &Generator{
		enabled: cfg.Enabled,
		channel: channel,
		lang:    lang,
		content: content,
	}
}

// Enabled reports whether the generator emits captions. It is false for a nil or
// disabled Generator.
func (g *Generator) Enabled() bool { return g != nil && g.enabled }

// Channel returns the CEA-608 caption channel (1 == CC1).
func (g *Generator) Channel() int { return g.channel }

// Lang returns the advertised caption language tag.
func (g *Generator) Lang() string { return g.lang }

// Schedule builds one group's captions and returns one caption envelope per
// video frame (len == nFrames), ready to splice into the frame's coded sample
// with Splice: a bare SEI NAL unit for AVC/HEVC, a metadata_itu_t_t35 OBU for
// AV1. groupNr is the MoQ group number (== unix seconds); the group's captions
// start at wall-clock groupNr*1000 ms. fps is the video frame rate and nFrames
// the number of frames in the group.
//
// It returns nil when captions are off (nil or disabled Generator), when
// nFrames <= 0, when codec is not one of the three supported values, or when
// go-608 cannot build the group (an out-of-range fps or a caption that does not
// fit the group — the Overran case). A nil return means "no captions this
// group"; the caller simply skips splicing.
func (g *Generator) Schedule(groupNr int64, fps float64, nFrames int, codec Codec) [][]byte {
	if !g.Enabled() || nFrames <= 0 {
		return nil
	}
	unit := generate.Unit{Nr: groupNr, StartMS: groupNr * 1000, Frames: nFrames}
	frames, err := generate.BuildUnitCues(fps, unit, targetPeriodMS, g.content)
	if err != nil {
		return nil
	}
	nalCodec, isNAL, err := codec.nalCodec()
	if err != nil {
		return nil
	}
	out := make([][]byte, len(frames))
	for i, f := range frames {
		if isNAL {
			out[i] = carriage.FrameSEINALU(f.Field1, f.Field2, f.CCCount, nalCodec)
		} else {
			out[i] = carriage.FrameMetadataOBU(f.Field1, f.Field2, f.CCCount)
		}
	}
	return out
}

// DefaultContent is the built-in CueContentFunc: two centered pop-on lines per
// cue. Row 13 (white) is the cue's UTC wall-clock time HH:MM:SS.mmm; row 14
// (yellow) is "GRP <n>" where n is u.Nr, the MoQ group number (== unix seconds),
// so the caption is a self-describing clock. cueIdx is unused: with one cue per
// group the content is a pure function of u.Nr and cueStartMS.
func DefaultContent(u generate.Unit, _ int, cueStartMS int64) generate.UnitCue {
	t := time.UnixMilli(cueStartMS).UTC()
	h, m, s := t.Clock()
	ts := fmt.Sprintf("%02d:%02d:%02d.%03d", h, m, s, t.Nanosecond()/1_000_000)
	grp := fmt.Sprintf("GRP %d", u.Nr)
	return generate.UnitCue{Lines: []cta608.Line{
		{Row: captionRowTime, Align: cta608.AlignCenter, Runs: []cta608.Run{
			{Text: ts, Pen: cta608.Pen{Color: cta608.White}},
		}},
		{Row: captionRowGroup, Align: cta608.AlignCenter, Runs: []cta608.Run{
			{Text: grp, Pen: cta608.Pen{Color: cta608.Yellow}},
		}},
	}}
}

// CodecFor maps a representation codec string to the caption codec: "avc*" ->
// CodecAVC, "hev*"/"hvc*" -> CodecHEVC, "av01*" -> CodecAV1. It returns
// ok=false for anything else (audio, subtitles, unknown video), which is the
// gate both the injection and the catalog use. The returned Codec is
// meaningless when ok is false; always check ok.
func CodecFor(codecStr string) (Codec, bool) {
	switch {
	case strings.HasPrefix(codecStr, "avc"):
		return CodecAVC, true
	case strings.HasPrefix(codecStr, "hev"), strings.HasPrefix(codecStr, "hvc"):
		return CodecHEVC, true
	case strings.HasPrefix(codecStr, "av01"):
		return CodecAV1, true
	default:
		return 0, false
	}
}
