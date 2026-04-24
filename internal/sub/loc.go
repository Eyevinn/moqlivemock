package sub

import (
	"encoding/binary"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/Eyevinn/moqtransport"
	"github.com/Eyevinn/mp4ff/aac"
)

// LOC property IDs from draft-ietf-moq-loc-02 §2.3.1.
const (
	locPropTimestamp = 0x06
)

// locTimestampMicros returns the LOC Timestamp (ID 0x06) from the object's
// extension headers, interpreted as microseconds since the Unix epoch
// (the default when no Timescale property is present, per LOC-02 §2.3.1.1).
func locTimestampMicros(headers moqtransport.KVPList) (uint64, bool) {
	for _, kv := range headers {
		if kv.Type == locPropTimestamp {
			return kv.ValueVarInt, true
		}
	}
	return 0, false
}

// annexBStartCode is the 4-byte AnnexB start code used in H.264 bitstreams.
var annexBStartCode = []byte{0x00, 0x00, 0x00, 0x01}

// LOCVideoWriter converts LOC AVC video objects (length-prefixed NALUs) to
// AnnexB H.264 format by replacing 4-byte length prefixes with start codes.
// The resulting output is playable with: ffplay -f h264 file.h264
type LOCVideoWriter struct {
	W io.Writer
}

// Write converts a LOC AVC payload to AnnexB format and writes it.
func (lw *LOCVideoWriter) Write(payload []byte) error {
	data := payload
	for len(data) >= 4 {
		naluLen := binary.BigEndian.Uint32(data[:4])
		data = data[4:]
		if int(naluLen) > len(data) {
			return fmt.Errorf("LOC AVC: NALU length %d exceeds remaining data %d", naluLen, len(data))
		}
		_, err := lw.W.Write(annexBStartCode)
		if err != nil {
			return err
		}
		_, err = lw.W.Write(data[:naluLen])
		if err != nil {
			return err
		}
		data = data[naluLen:]
	}
	return nil
}

// LOCAACWriter converts LOC AAC objects (raw AAC frames) to ADTS-framed AAC.
// The resulting output is playable with: ffplay file.aac
//
// Only AAC-LC (mp4a.40.2) is supported — ADTS header construction via
// mp4ff/aac rejects other object types.
type LOCAACWriter struct {
	W             io.Writer
	SampleRate    int  // from catalog samplerate field
	ChannelConfig byte // from catalog channelConfig field
	ObjectType    byte // from codec string "mp4a.40.N" -> N (must be 2 / AAC-LC)
}

// NewLOCAACWriter creates a LOCAACWriter from catalog track fields.
// codec is e.g. "mp4a.40.2", sampleRate e.g. 44100, channelConfig e.g. "2".
// Returns an error if the codec is not AAC-LC.
func NewLOCAACWriter(w io.Writer, codec string, sampleRate int, channelConfig string) (*LOCAACWriter, error) {
	objectType := byte(aac.AAClc)
	parts := strings.Split(codec, ".")
	if len(parts) >= 3 {
		if ot, err := strconv.Atoi(parts[2]); err == nil {
			objectType = byte(ot)
		}
	}
	if objectType != aac.AAClc {
		return nil, fmt.Errorf("LOC AAC: only AAC-LC (mp4a.40.2) is supported, got %q", codec)
	}

	chCfg := byte(2) // Default stereo
	if cc, err := strconv.Atoi(channelConfig); err == nil {
		chCfg = byte(cc)
	}

	return &LOCAACWriter{
		W:             w,
		SampleRate:    sampleRate,
		ChannelConfig: chCfg,
		ObjectType:    objectType,
	}, nil
}

// Write prepends a 7-byte ADTS header to the raw AAC frame and writes it.
func (lw *LOCAACWriter) Write(payload []byte) error {
	hdr, err := aac.NewADTSHeader(lw.SampleRate, lw.ChannelConfig, lw.ObjectType, uint16(len(payload)))
	if err != nil {
		return fmt.Errorf("LOC AAC: ADTS header: %w", err)
	}
	if _, err := lw.W.Write(hdr.Encode()); err != nil {
		return err
	}
	_, err = lw.W.Write(payload)
	return err
}

// LOCOpusWriter writes raw LOC Opus packets directly.
// Raw Opus packets without a container are not directly playable but useful for debugging.
type LOCOpusWriter struct {
	W io.Writer
}

// Write writes the raw Opus payload.
func (lw *LOCOpusWriter) Write(payload []byte) error {
	_, err := lw.W.Write(payload)
	return err
}
