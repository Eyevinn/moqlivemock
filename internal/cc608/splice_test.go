package cc608

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/Eyevinn/go-608/carriage"
	"github.com/Eyevinn/mp4ff/avc"
)

// AVC bare-NALU first bytes (nal_unit_type = byte & 0x1f).
var (
	avcSPS = []byte{0x67, 0x01, 0x02} // type 7
	avcPPS = []byte{0x68, 0x03}       // type 8
	avcSEI = []byte{0x06, 0x04}       // type 6
	avcIDR = []byte{0x65, 0x05, 0x06} // type 5 (VCL)
	avcNon = []byte{0x01, 0x07}       // type 1 (VCL, non-IDR)
)

// HEVC bare-NALU first bytes (nal_unit_type = (byte0 >> 1) & 0x3f, 2-byte header).
var (
	hevcVPS = []byte{0x40, 0x01, 0x0a} // type 32
	hevcSPS = []byte{0x42, 0x01, 0x0b} // type 33
	hevcPPS = []byte{0x44, 0x01, 0x0c} // type 34
	hevcSEI = []byte{0x4e, 0x01, 0x0d} // type 39 (prefix SEI)
	hevcIDR = []byte{0x26, 0x01, 0x0e} // type 19 (IDR_W_RADL, VCL)
)

// marker is the bare SEI NAL that SpliceSEIBeforeVCL inserts; its distinctive
// bytes let the test find it after re-parsing.
var marker = []byte{0xde, 0xad, 0xbe, 0xef}

// buildSample serializes bare NALUs into a 4-byte-length-prefixed AVCC stream.
func buildSample(nalus ...[]byte) []byte {
	var out []byte
	var lb [4]byte
	for _, n := range nalus {
		binary.BigEndian.PutUint32(lb[:], uint32(len(n)))
		out = append(out, lb[:]...)
		out = append(out, n...)
	}
	return out
}

func TestSpliceSEIBeforeVCL(t *testing.T) {
	cases := []struct {
		name      string
		codec     carriage.Codec
		in        [][]byte
		wantIdx   int      // expected index of the spliced marker in the output
		wantOrder [][]byte // full expected NALU order in the output
	}{
		{
			name:      "avc SPS/PPS/SEI/IDR",
			codec:     carriage.CodecAVC,
			in:        [][]byte{avcSPS, avcPPS, avcSEI, avcIDR},
			wantIdx:   3,
			wantOrder: [][]byte{avcSPS, avcPPS, avcSEI, marker, avcIDR},
		},
		{
			name:      "avc IDR first",
			codec:     carriage.CodecAVC,
			in:        [][]byte{avcIDR, avcNon},
			wantIdx:   0,
			wantOrder: [][]byte{marker, avcIDR, avcNon},
		},
		{
			name:      "avc no VCL appends",
			codec:     carriage.CodecAVC,
			in:        [][]byte{avcSPS, avcPPS},
			wantIdx:   2,
			wantOrder: [][]byte{avcSPS, avcPPS, marker},
		},
		{
			name:      "hevc VPS/SPS/PPS/SEI/IDR",
			codec:     carriage.CodecHEVC,
			in:        [][]byte{hevcVPS, hevcSPS, hevcPPS, hevcSEI, hevcIDR},
			wantIdx:   4,
			wantOrder: [][]byte{hevcVPS, hevcSPS, hevcPPS, hevcSEI, marker, hevcIDR},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := SpliceSEIBeforeVCL(buildSample(c.in...), marker, c.codec)
			if err != nil {
				t.Fatalf("SpliceSEIBeforeVCL: %v", err)
			}
			got, err := avc.GetNalusFromSample(out) // pure 4-byte-length split
			if err != nil {
				t.Fatalf("GetNalusFromSample: %v", err)
			}
			if len(got) != len(c.wantOrder) {
				t.Fatalf("got %d NALUs, want %d", len(got), len(c.wantOrder))
			}
			for i := range c.wantOrder {
				if !bytes.Equal(got[i], c.wantOrder[i]) {
					t.Errorf("NALU %d = %x, want %x", i, got[i], c.wantOrder[i])
				}
			}
			if !bytes.Equal(got[c.wantIdx], marker) {
				t.Errorf("marker not at index %d: got %x", c.wantIdx, got[c.wantIdx])
			}
			// The marker must sit immediately before the first VCL NALU (or last if none).
			firstVCL := len(got)
			for i, n := range got {
				if isVCLNalu(n, c.codec) {
					firstVCL = i
					break
				}
			}
			if firstVCL < len(got) && c.wantIdx != firstVCL-1 {
				t.Errorf("marker at %d, first VCL at %d: not immediately before", c.wantIdx, firstVCL)
			}
		})
	}
}

func TestIsVCLNalu(t *testing.T) {
	cases := []struct {
		name  string
		nalu  []byte
		codec carriage.Codec
		want  bool
	}{
		{"avc IDR", avcIDR, carriage.CodecAVC, true},
		{"avc non-IDR", avcNon, carriage.CodecAVC, true},
		{"avc SPS", avcSPS, carriage.CodecAVC, false},
		{"avc PPS", avcPPS, carriage.CodecAVC, false},
		{"avc SEI", avcSEI, carriage.CodecAVC, false},
		{"hevc IDR", hevcIDR, carriage.CodecHEVC, true},
		{"hevc VPS", hevcVPS, carriage.CodecHEVC, false},
		{"hevc SPS", hevcSPS, carriage.CodecHEVC, false},
		{"hevc prefix SEI", hevcSEI, carriage.CodecHEVC, false},
	}
	for _, c := range cases {
		if got := isVCLNalu(c.nalu, c.codec); got != c.want {
			t.Errorf("%s: isVCLNalu = %v, want %v", c.name, got, c.want)
		}
	}
}
