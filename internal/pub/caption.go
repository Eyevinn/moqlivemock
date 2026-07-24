package pub

import (
	"log/slog"

	"github.com/Eyevinn/go-608/carriage"

	"github.com/Eyevinn/moqlivemock/internal"
	"github.com/Eyevinn/moqlivemock/internal/cc608"
)

// videoCaptioner injects auto-generated CTA-608 captions into the raw-frame
// serve paths (LOC and moq-mi), where the publisher hands one coded frame per
// MoQ object. It computes a per-group SEI schedule and splices the matching SEI
// in front of each frame's first VCL NALU.
//
// The zero value — and a captioner built from a track with captions off, a
// non-video role, or a non-AVC/HEVC codec — is disabled: schedule returns nil
// and spliceFrame returns the frame unchanged, so caption injection is a
// complete no-op. (The CMAF/LOCMAF path does its own, equivalent splicing inside
// internal.createFragment; this helper only serves the LOC and moq-mi loops.)
type videoCaptioner struct {
	gen   *cc608.Generator
	codec carriage.Codec
	fps   float64
	on    bool
}

// newVideoCaptioner builds a captioner for ct. It is enabled only when ct has an
// enabled generator, is a video track with valid timing, and uses an AVC or HEVC
// codec (AV1 and everything else yield a disabled captioner).
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

// schedule returns one SEI NAL per frame for the group covering samples
// [startNr,endNr), whose captions are anchored at anchorSec (whole UNIX seconds,
// matching cc608.DefaultContent's clock). It returns nil when captions are off,
// the range is empty, or go-608 cannot build the group — in which case the
// caller simply skips splicing.
func (c videoCaptioner) schedule(anchorSec int64, startNr, endNr uint64) [][]byte {
	if !c.on || endNr <= startNr {
		return nil
	}
	return c.gen.SEISchedule(anchorSec, c.fps, int(endNr-startNr), c.codec)
}

// spliceFrame returns data with the group's SEI for the frame at sampleNr
// spliced in front of its first VCL NALU. sei is the whole-group schedule from
// schedule and groupStartNr is the group's first sample number, so the frame's
// SEI is sei[sampleNr-groupStartNr]. It returns data unchanged when sei is nil
// (captions off) or the frame index is out of range; on a splice error it logs
// and returns the original data so captioning is strictly best-effort and never
// drops a frame.
func (c videoCaptioner) spliceFrame(data []byte, sei [][]byte, sampleNr, groupStartNr uint64) []byte {
	if sei == nil {
		return data
	}
	idx := int(sampleNr - groupStartNr)
	if idx < 0 || idx >= len(sei) || len(sei[idx]) == 0 {
		return data
	}
	spliced, err := cc608.SpliceSEIBeforeVCL(data, sei[idx], c.codec)
	if err != nil {
		slog.Warn("cc608: SEI splice failed, sending frame without captions",
			"error", err, "sample", sampleNr)
		return data
	}
	return spliced
}
