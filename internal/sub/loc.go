package sub

import (
	"encoding/binary"
	"fmt"
	"io"
	"strconv"
	"strings"
)

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
type LOCAACWriter struct {
	W             io.Writer
	SampleRate    int // from catalog samplerate field
	ChannelConfig int // from catalog channelConfig field
	ObjectType    int // from codec string "mp4a.40.N" -> N
}

// NewLOCAACWriter creates a LOCAACWriter from catalog track fields.
// codec is e.g. "mp4a.40.2", sampleRate e.g. 44100, channelConfig e.g. "2".
func NewLOCAACWriter(w io.Writer, codec string, sampleRate int, channelConfig string) (*LOCAACWriter, error) {
	// Parse object type from codec string "mp4a.40.N"
	objectType := 2 // Default to AAC-LC
	parts := strings.Split(codec, ".")
	if len(parts) >= 3 {
		if ot, err := strconv.Atoi(parts[2]); err == nil {
			objectType = ot
		}
	}

	chCfg := 2 // Default stereo
	if cc, err := strconv.Atoi(channelConfig); err == nil {
		chCfg = cc
	}

	return &LOCAACWriter{
		W:             w,
		SampleRate:    sampleRate,
		ChannelConfig: chCfg,
		ObjectType:    objectType,
	}, nil
}

// sampleRateIndex returns the ADTS sample rate index for a given sample rate.
func sampleRateIndex(rate int) int {
	rates := []int{96000, 88200, 64000, 48000, 44100, 32000, 24000, 22050, 16000, 12000, 11025, 8000, 7350}
	for i, r := range rates {
		if rate == r {
			return i
		}
	}
	return 4 // Default to 44100 index
}

// Write prepends a 7-byte ADTS header to the raw AAC frame and writes it.
func (lw *LOCAACWriter) Write(payload []byte) error {
	frameLen := len(payload) + 7 // ADTS header is 7 bytes
	srIdx := sampleRateIndex(lw.SampleRate)

	// Build 7-byte ADTS header
	// See ISO 14496-3 for ADTS header format
	header := [7]byte{}
	// Syncword (12 bits) = 0xFFF
	header[0] = 0xFF
	header[1] = 0xF1 // syncword + ID=0 (MPEG-4) + layer=0 + protection_absent=1
	// Profile (2 bits) + sampling_freq_index (4 bits) + private_bit (1) + channel_config high (1)
	header[2] = byte(((lw.ObjectType-1)&0x03)<<6) | byte((srIdx&0x0F)<<2) | byte((lw.ChannelConfig>>2)&0x01)
	// channel_config low (2) + orig/copy (1) + home (1) + copyright (2) + frame_length high (2)
	header[3] = byte((lw.ChannelConfig&0x03)<<6) | byte((frameLen>>11)&0x03)
	// frame_length mid (8 bits)
	header[4] = byte((frameLen >> 3) & 0xFF)
	// frame_length low (3 bits) + buffer_fullness high (5 bits)
	header[5] = byte((frameLen&0x07)<<5) | 0x1F // 0x1F = buffer fullness VBR
	// buffer_fullness low (6 bits) + number_of_raw_data_blocks (2 bits)
	header[6] = 0xFC // buffer fullness VBR + 0 raw data blocks

	_, err := lw.W.Write(header[:])
	if err != nil {
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
