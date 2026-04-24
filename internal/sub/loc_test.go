package sub

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/Eyevinn/moqtransport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLocTimestampMicros(t *testing.T) {
	t.Run("present", func(t *testing.T) {
		headers := moqtransport.KVPList{
			{Type: 0x02, ValueVarInt: 99},
			{Type: locPropTimestamp, ValueVarInt: 1234567},
		}
		ts, ok := locTimestampMicros(headers)
		require.True(t, ok)
		assert.Equal(t, uint64(1234567), ts)
	})
	t.Run("absent", func(t *testing.T) {
		headers := moqtransport.KVPList{
			{Type: 0x02, ValueVarInt: 99},
		}
		_, ok := locTimestampMicros(headers)
		assert.False(t, ok)
	})
	t.Run("empty", func(t *testing.T) {
		_, ok := locTimestampMicros(nil)
		assert.False(t, ok)
	})
}

func TestLOCVideoWriter(t *testing.T) {
	t.Run("two NALUs", func(t *testing.T) {
		nalu1 := []byte{0x67, 0x01, 0x02}       // fake SPS
		nalu2 := []byte{0x65, 0x03, 0x04, 0x05} // fake IDR
		payload := make([]byte, 0, 4+len(nalu1)+4+len(nalu2))
		work := make([]byte, 4)
		binary.BigEndian.PutUint32(work, uint32(len(nalu1)))
		payload = append(payload, work...)
		payload = append(payload, nalu1...)
		binary.BigEndian.PutUint32(work, uint32(len(nalu2)))
		payload = append(payload, work...)
		payload = append(payload, nalu2...)

		var buf bytes.Buffer
		lw := &LOCVideoWriter{W: &buf}
		require.NoError(t, lw.Write(payload))

		expected := append([]byte{}, annexBStartCode...)
		expected = append(expected, nalu1...)
		expected = append(expected, annexBStartCode...)
		expected = append(expected, nalu2...)
		assert.Equal(t, expected, buf.Bytes())
	})

	t.Run("empty payload is noop", func(t *testing.T) {
		var buf bytes.Buffer
		lw := &LOCVideoWriter{W: &buf}
		require.NoError(t, lw.Write(nil))
		assert.Equal(t, 0, buf.Len())
	})

	t.Run("truncated NALU is error", func(t *testing.T) {
		// Length prefix claims 10 bytes but only 2 follow.
		payload := []byte{0, 0, 0, 10, 0x01, 0x02}
		lw := &LOCVideoWriter{W: &bytes.Buffer{}}
		err := lw.Write(payload)
		require.Error(t, err)
	})
}

func TestNewLOCAACWriter(t *testing.T) {
	t.Run("AAC-LC ok", func(t *testing.T) {
		w, err := NewLOCAACWriter(&bytes.Buffer{}, "mp4a.40.2", 48000, "2")
		require.NoError(t, err)
		assert.Equal(t, 48000, w.SampleRate)
		assert.Equal(t, byte(2), w.ChannelConfig)
		assert.Equal(t, byte(2), w.ObjectType)
	})

	t.Run("HE-AAC rejected", func(t *testing.T) {
		_, err := NewLOCAACWriter(&bytes.Buffer{}, "mp4a.40.5", 48000, "2")
		require.Error(t, err)
	})

	t.Run("short codec defaults to AAC-LC", func(t *testing.T) {
		// Fewer than 3 dotted parts: object type remains the AAC-LC default.
		w, err := NewLOCAACWriter(&bytes.Buffer{}, "mp4a", 44100, "1")
		require.NoError(t, err)
		assert.Equal(t, byte(2), w.ObjectType)
		assert.Equal(t, byte(1), w.ChannelConfig)
	})

	t.Run("invalid channel falls back to default stereo", func(t *testing.T) {
		w, err := NewLOCAACWriter(&bytes.Buffer{}, "mp4a.40.2", 44100, "notanumber")
		require.NoError(t, err)
		assert.Equal(t, byte(2), w.ChannelConfig)
	})
}

func TestLOCAACWriter_Write(t *testing.T) {
	var buf bytes.Buffer
	w, err := NewLOCAACWriter(&buf, "mp4a.40.2", 48000, "2")
	require.NoError(t, err)

	payload := []byte{0xDE, 0xAD, 0xBE, 0xEF}
	require.NoError(t, w.Write(payload))

	out := buf.Bytes()
	// ADTS header is 7 bytes, then the raw frame.
	require.Len(t, out, 7+len(payload))
	// Byte 0 is the high 8 bits of the 0xFFF sync word.
	assert.Equal(t, byte(0xFF), out[0])
	// Byte 1: high nibble is remaining 4 sync bits (1111); low nibble is
	// ID=0 (MPEG-4) | layer=00 | protection_absent=1 => 0x01.
	assert.Equal(t, byte(0xF1), out[1])
	assert.Equal(t, payload, out[7:])
}

func TestLOCOpusWriter(t *testing.T) {
	var buf bytes.Buffer
	w := &LOCOpusWriter{W: &buf}
	payload := []byte{1, 2, 3, 4, 5}
	require.NoError(t, w.Write(payload))
	assert.Equal(t, payload, buf.Bytes())
}
