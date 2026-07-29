package pub

import (
	"log/slog"

	"github.com/Eyevinn/moqlivemock/internal"
	"github.com/Eyevinn/moqlivemock/internal/cc608"
)

// videoCaptioner injects auto-generated CTA-608 captions into the raw-frame
// serve paths (LOC and moq-mi), where the publisher hands one coded frame per
// MoQ object. It computes a per-group caption schedule and splices the matching
// envelope into each frame's coded sample — an SEI NAL unit before the first
// VCL NALU for AVC/HEVC, a metadata OBU before the first frame OBU for AV1.
//
// The zero value — and a captioner built from a track with captions off, a
// non-video role, or a codec with no CTA-608 carriage — is disabled: schedule
// returns nil and spliceFrame returns the frame unchanged, so caption injection
// is a complete no-op. (The CMAF/LOCMAF path does its own, equivalent splicing
// inside internal.createFragment; this helper only serves the LOC and moq-mi
// loops.)
type videoCaptioner struct {
	gen   *cc608.Generator
	codec cc608.Codec
	fps   float64
	on    bool
}

// newVideoCaptioner builds a captioner for ct. It is enabled only when ct has an
// enabled generator, is a video track with valid timing, and uses a codec with
// CTA-608 carriage (AVC, HEVC or AV1); anything else yields a disabled
// captioner.
func newVideoCaptioner(ct *internal.ContentTrack) videoCaptioner {
	gen := ct.CC608Generator()
	if !gen.Enabled() || ct.ContentType != "video" || ct.SampleDur == 0 || ct.TimeScale == 0 {
		return videoCaptioner{}
	}
	codec, ok := cc608.CodecFor(ct.SpecData.Codec())
	if !ok {
		return videoCaptioner{}
	}
	return videoCaptioner{
		gen:   gen,
		codec: codec,
		fps:   float64(ct.TimeScale) / float64(ct.SampleDur),
		on:    true,
	}
}

// schedule returns one caption envelope per frame for the group covering samples
// [startNr,endNr), whose captions are anchored at anchorSec (whole UNIX seconds,
// matching cc608.DefaultContent's clock). It returns nil when captions are off,
// the range is empty, or go-608 cannot build the group — in which case the
// caller simply skips splicing.
func (c videoCaptioner) schedule(anchorSec int64, startNr, endNr uint64) [][]byte {
	if !c.on || endNr <= startNr {
		return nil
	}
	return c.gen.Schedule(anchorSec, c.fps, int(endNr-startNr), c.codec)
}

// spliceFrame returns data with the group's caption envelope for the frame at
// sampleNr spliced in. sched is the whole-group schedule from schedule and
// groupStartNr is the group's first sample number, so the frame's envelope is
// sched[sampleNr-groupStartNr]. It returns data unchanged when sched is nil
// (captions off) or the frame index is out of range; on a splice error it logs
// and returns the original data so captioning is strictly best-effort and never
// drops a frame.
func (c videoCaptioner) spliceFrame(data []byte, sched [][]byte, sampleNr, groupStartNr uint64) []byte {
	if sched == nil {
		return data
	}
	idx := int(sampleNr - groupStartNr)
	if idx < 0 || idx >= len(sched) || len(sched[idx]) == 0 {
		return data
	}
	spliced, err := cc608.Splice(data, sched[idx], c.codec)
	if err != nil {
		slog.Warn("cc608: caption splice failed, sending frame without captions",
			"error", err, "sample", sampleNr, "codec", c.codec)
		return data
	}
	return spliced
}
