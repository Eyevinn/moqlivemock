// Package moqmi provides Object Property builders and readers for the MoQ
// Media Interop (moq-mi) wire format defined in
// draft-cenzano-moq-media-interop-03.
//
// It lived in moqtransport until the draft-18 rewrite, which is the wrong
// place for it: moq-mi is one application's packaging, and a transport should
// not know about H.264 extradata. The properties it builds are ordinary MOQT
// Key-Value-Pairs, so nothing here needs the transport beyond that type.
//
// The varints inside a property's value are moq-mi's own packing, not MOQT's,
// so they stay RFC 9000 varints rather than following draft-17's move to
// vi64. Whether draft-cenzano intends to follow is a question for that draft.
package moqmi

import (
	"fmt"

	"github.com/Eyevinn/moqtransport"
	"github.com/quic-go/quic-go/quicvarint"
)

// Media type values for extension header 0x0A.
const (
	MediaTypeVideoH264AVCC uint64 = 0x0
	MediaTypeAudioOpus     uint64 = 0x1
	MediaTypeUTF8Text      uint64 = 0x2
	MediaTypeAudioAACLC    uint64 = 0x3
)

// Extension header type IDs.
const (
	ExtMediaType      uint64 = 0x0A // even — varint value
	ExtVideoH264Extra uint64 = 0x0D // odd  — length-prefixed bytes (AVCDecoderConfigurationRecord)
	ExtAudioOpus      uint64 = 0x0F // odd  — length-prefixed bytes
	ExtUTF8Text       uint64 = 0x11 // odd  — length-prefixed bytes
	ExtAudioAACLC     uint64 = 0x13 // odd  — length-prefixed bytes
	ExtVideoH264Meta  uint64 = 0x15 // odd  — length-prefixed bytes
)

// VideoMetadata holds the fields of the Video H264 AVCC metadata header (0x15).
type VideoMetadata struct {
	SeqID       uint64
	PTS         uint64
	DTS         uint64
	Timebase    uint64
	Duration    uint64
	WallclockMS uint64
}

// AudioMetadata holds the fields of the Audio Opus (0x0F) or Audio AAC-LC (0x13) headers.
type AudioMetadata struct {
	SeqID       uint64
	PTS         uint64
	Timebase    uint64
	SampleFreq  uint64
	NumChannels uint64
	Duration    uint64
	WallclockMS uint64
}

// TextMetadata holds the fields of the UTF-8 Text header (0x11).
type TextMetadata struct {
	SeqID uint64
}

// encodeVarints encodes a slice of uint64 values as consecutive QUIC varints.
func encodeVarints(vals ...uint64) []byte {
	buf := make([]byte, 0, len(vals)*4)
	for _, v := range vals {
		buf = quicvarint.Append(buf, v)
	}
	return buf
}

// VideoHeaders builds extension headers for a video H264 AVCC object.
// If extradata is non-nil, the AVCDecoderConfigurationRecord header (0x0D) is included.
func VideoHeaders(meta VideoMetadata, extradata []byte) moqtransport.KVPList {
	headers := moqtransport.KVPList{
		{Type: ExtMediaType, ValueVarInt: MediaTypeVideoH264AVCC},
		{Type: ExtVideoH264Extra},
		{
			Type: ExtVideoH264Meta,
			ValueBytes: encodeVarints(
				meta.SeqID,
				meta.PTS,
				meta.DTS,
				meta.Timebase,
				meta.Duration,
				meta.WallclockMS,
			),
		},
	}
	if extradata != nil {
		headers[1].ValueBytes = extradata
	} else {
		// Remove the extradata entry when not needed.
		headers = append(headers[:1], headers[2:]...)
	}
	return headers
}

// AudioOpusHeaders builds extension headers for an Audio Opus object.
func AudioOpusHeaders(meta AudioMetadata) moqtransport.KVPList {
	return moqtransport.KVPList{
		{Type: ExtMediaType, ValueVarInt: MediaTypeAudioOpus},
		{
			Type: ExtAudioOpus,
			ValueBytes: encodeVarints(
				meta.SeqID,
				meta.PTS,
				meta.Timebase,
				meta.SampleFreq,
				meta.NumChannels,
				meta.Duration,
				meta.WallclockMS,
			),
		},
	}
}

// AudioAACHeaders builds extension headers for an Audio AAC-LC MPEG4 object.
func AudioAACHeaders(meta AudioMetadata) moqtransport.KVPList {
	return moqtransport.KVPList{
		{Type: ExtMediaType, ValueVarInt: MediaTypeAudioAACLC},
		{
			Type: ExtAudioAACLC,
			ValueBytes: encodeVarints(
				meta.SeqID,
				meta.PTS,
				meta.Timebase,
				meta.SampleFreq,
				meta.NumChannels,
				meta.Duration,
				meta.WallclockMS,
			),
		},
	}
}

// TextHeaders builds extension headers for a UTF-8 text object.
func TextHeaders(meta TextMetadata) moqtransport.KVPList {
	return moqtransport.KVPList{
		{Type: ExtMediaType, ValueVarInt: MediaTypeUTF8Text},
		{
			Type:       ExtUTF8Text,
			ValueBytes: encodeVarints(meta.SeqID),
		},
	}
}

// --- Readers (parse received extension headers) ---

// MediaType extracts the media type value from extension headers.
// Returns false if the media type header (0x0A) is not present.
func MediaType(headers moqtransport.KVPList) (uint64, bool) {
	for _, h := range headers {
		if h.Type == ExtMediaType {
			return h.ValueVarInt, true
		}
	}
	return 0, false
}

// parseVarints parses consecutive QUIC varints from data, returning n values.
func parseVarints(data []byte, n int) ([]uint64, error) {
	vals := make([]uint64, 0, n)
	for i := 0; i < n; i++ {
		if len(data) == 0 {
			return nil, fmt.Errorf("moqmi: unexpected end of data at field %d of %d", i, n)
		}
		v, consumed, err := quicvarint.Parse(data)
		if err != nil {
			return nil, fmt.Errorf("moqmi: parse varint field %d: %w", i, err)
		}
		vals = append(vals, v)
		data = data[consumed:]
	}
	return vals, nil
}

// ReadVideoMetadata extracts the Video H264 AVCC metadata (0x15) from extension headers.
// Returns false if the header is not present.
func ReadVideoMetadata(headers moqtransport.KVPList) (VideoMetadata, bool, error) {
	for _, h := range headers {
		if h.Type == ExtVideoH264Meta {
			vals, err := parseVarints(h.ValueBytes, 6)
			if err != nil {
				return VideoMetadata{}, false, err
			}
			return VideoMetadata{
				SeqID:       vals[0],
				PTS:         vals[1],
				DTS:         vals[2],
				Timebase:    vals[3],
				Duration:    vals[4],
				WallclockMS: vals[5],
			}, true, nil
		}
	}
	return VideoMetadata{}, false, nil
}

// ReadVideoExtradata extracts the AVCDecoderConfigurationRecord (0x0D) from extension headers.
// Returns nil, false if the header is not present.
func ReadVideoExtradata(headers moqtransport.KVPList) ([]byte, bool) {
	for _, h := range headers {
		if h.Type == ExtVideoH264Extra {
			return h.ValueBytes, true
		}
	}
	return nil, false
}

// ReadAudioOpusMetadata extracts the Audio Opus metadata (0x0F) from extension headers.
// Returns false if the header is not present.
func ReadAudioOpusMetadata(headers moqtransport.KVPList) (AudioMetadata, bool, error) {
	return readAudioMetadata(headers, ExtAudioOpus)
}

// ReadAudioAACMetadata extracts the Audio AAC-LC metadata (0x13) from extension headers.
// Returns false if the header is not present.
func ReadAudioAACMetadata(headers moqtransport.KVPList) (AudioMetadata, bool, error) {
	return readAudioMetadata(headers, ExtAudioAACLC)
}

func readAudioMetadata(headers moqtransport.KVPList, extType uint64) (AudioMetadata, bool, error) {
	for _, h := range headers {
		if h.Type == extType {
			vals, err := parseVarints(h.ValueBytes, 7)
			if err != nil {
				return AudioMetadata{}, false, err
			}
			return AudioMetadata{
				SeqID:       vals[0],
				PTS:         vals[1],
				Timebase:    vals[2],
				SampleFreq:  vals[3],
				NumChannels: vals[4],
				Duration:    vals[5],
				WallclockMS: vals[6],
			}, true, nil
		}
	}
	return AudioMetadata{}, false, nil
}

// ReadTextMetadata extracts the UTF-8 Text metadata (0x11) from extension headers.
// Returns false if the header is not present.
func ReadTextMetadata(headers moqtransport.KVPList) (TextMetadata, bool, error) {
	for _, h := range headers {
		if h.Type == ExtUTF8Text {
			vals, err := parseVarints(h.ValueBytes, 1)
			if err != nil {
				return TextMetadata{}, false, err
			}
			return TextMetadata{SeqID: vals[0]}, true, nil
		}
	}
	return TextMetadata{}, false, nil
}
