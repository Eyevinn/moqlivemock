package cc608

import (
	"fmt"

	"github.com/Eyevinn/go-608/carriage"
)

// Codec selects how a frame's CTA-608 cc_data() is wrapped and where it is
// spliced into the coded sample. The cc_data() itself is identical for all
// three; only the envelope and the splice anchor differ.
//
// This is the three-value discriminator go-608 leaves to its consumers:
// carriage.Codec names NAL framing and so stays two-valued, with the AV1
// metadata-OBU functions sitting beside the SEI ones rather than inside them.
// Keeping the third value here means a switch that forgets AV1 fails to
// compile instead of silently captioning nothing.
type Codec int

const (
	CodecAVC  Codec = iota // H.264: caption SEI NAL unit before the first VCL NALU.
	CodecHEVC              // H.265: prefix-SEI NAL unit before the first VCL NALU.
	CodecAV1               // AV1: metadata_itu_t_t35 OBU before the first frame OBU.
)

func (c Codec) String() string {
	switch c {
	case CodecAVC:
		return "AVC"
	case CodecHEVC:
		return "HEVC"
	case CodecAV1:
		return "AV1"
	default:
		return fmt.Sprintf("Codec(%d)", int(c))
	}
}

// nalCodec maps the NAL-framed codecs onto go-608's carriage.Codec. ok is false
// for CodecAV1, which has no NAL units and takes the parallel OBU path.
func (c Codec) nalCodec() (carriage.Codec, bool) {
	switch c {
	case CodecAVC:
		return carriage.CodecAVC, true
	case CodecHEVC:
		return carriage.CodecHEVC, true
	default:
		return 0, false
	}
}

// Splice inserts one frame's caption envelope — an element of the schedule
// returned by Generator.Schedule — into that frame's coded sample, returning
// the rewritten sample. The original is never modified.
//
// For AVC/HEVC the sample is a length-prefixed NAL stream and the SEI goes
// before the first VCL NALU; for AV1 it is a bare OBU sequence (one temporal
// unit) and the metadata OBU goes before the first frame OBU, after any
// sequence header. Both keep the caption ahead of the picture data of the same
// access unit, which is what puts it in scope for that frame.
func Splice(sample, envelope []byte, codec Codec) ([]byte, error) {
	if nalCodec, ok := codec.nalCodec(); ok {
		return carriage.SpliceSEIBeforeVCL(sample, envelope, nalCodec)
	}
	return carriage.SpliceOBUBeforeFrame(sample, envelope)
}

// FieldPairs is the decode counterpart of Splice: it reads the CTA-608 field-1
// and field-2 byte-pair streams back out of a coded sample, ready to feed a
// cta608.Decoder. Both fields are nil when the sample carries no captions.
//
// It exists so that verifying "the captions really are in the elementary
// stream" is one call regardless of codec — the same dispatch as Splice, in the
// opposite direction.
func FieldPairs(sample []byte, codec Codec) (field1, field2 []byte, err error) {
	nalCodec, ok := codec.nalCodec()
	if !ok {
		return carriage.OBUFieldPairs(sample)
	}
	nalus, err := carriage.SampleNALUs(sample)
	if err != nil {
		return nil, nil, err
	}
	return carriage.FieldPairs(nalus, nalCodec)
}
