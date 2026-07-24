package cc608

import (
	"encoding/binary"

	"github.com/Eyevinn/go-608/carriage"
	"github.com/Eyevinn/mp4ff/avc"
	"github.com/Eyevinn/mp4ff/hevc"
)

// SpliceSEIBeforeVCL returns sampleData (a 4-byte-big-endian-length-prefixed AVCC
// NALU byte stream) with seiNAL inserted — with its own 4-byte length prefix —
// immediately before the first VCL (coded-slice) NALU. If the sample has no VCL
// NALU, the SEI is appended at the end. seiNAL is a bare NAL unit as returned by
// carriage.FrameSEINALU (no length prefix). codec selects the VCL-detection rules.
//
// Ported from livesim2 (cmd/livesim2/app/cc608_inject.go: spliceSEIBeforeVCL).
func SpliceSEIBeforeVCL(sampleData, seiNAL []byte, codec carriage.Codec) ([]byte, error) {
	nalus, err := avc.GetNalusFromSample(sampleData) // pure 4-byte-length split, codec-agnostic
	if err != nil {
		return nil, err
	}
	insertAt := len(nalus)
	for i, n := range nalus {
		if len(n) > 0 && isVCLNalu(n, codec) {
			insertAt = i
			break
		}
	}
	ordered := make([][]byte, 0, len(nalus)+1)
	ordered = append(ordered, nalus[:insertAt]...)
	ordered = append(ordered, seiNAL)
	ordered = append(ordered, nalus[insertAt:]...)

	total := 0
	for _, n := range ordered {
		total += 4 + len(n)
	}
	out := make([]byte, 0, total)
	var lenBuf [4]byte
	for _, n := range ordered {
		binary.BigEndian.PutUint32(lenBuf[:], uint32(len(n)))
		out = append(out, lenBuf[:]...)
		out = append(out, n...)
	}
	return out, nil
}

// isVCLNalu reports whether a bare NALU (no length prefix) is a VCL (coded-slice)
// unit for the codec. Ported from livesim2 (cc608_inject.go: isVCLNalu).
func isVCLNalu(nalu []byte, codec carriage.Codec) bool {
	if codec == carriage.CodecHEVC {
		return hevc.IsVideoNaluType(hevc.GetNaluType(nalu[0]))
	}
	return avc.IsVideoNaluType(avc.GetNaluType(nalu[0]))
}
