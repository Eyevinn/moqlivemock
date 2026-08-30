package moqmi

import (
	"testing"

	"github.com/Eyevinn/moqtransport"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVideoHeaders(t *testing.T) {
	meta := VideoMetadata{
		SeqID:       0,
		PTS:         0,
		DTS:         0,
		Timebase:    30,
		Duration:    1,
		WallclockMS: 1717027200000,
	}

	t.Run("with extradata", func(t *testing.T) {
		extradata := []byte{0x01, 0x64, 0x00, 0x1f}
		headers := VideoHeaders(meta, extradata)
		assert.Len(t, headers, 3)
		assert.Equal(t, ExtMediaType, headers[0].Type)
		assert.Equal(t, MediaTypeVideoH264AVCC, headers[0].ValueVarInt)
		assert.Equal(t, ExtVideoH264Extra, headers[1].Type)
		assert.Equal(t, extradata, headers[1].ValueBytes)
		assert.Equal(t, ExtVideoH264Meta, headers[2].Type)
	})

	t.Run("without extradata", func(t *testing.T) {
		headers := VideoHeaders(meta, nil)
		assert.Len(t, headers, 2)
		assert.Equal(t, ExtMediaType, headers[0].Type)
		assert.Equal(t, ExtVideoH264Meta, headers[1].Type)
	})
}

func TestAudioOpusHeaders(t *testing.T) {
	meta := AudioMetadata{
		SeqID:       5,
		PTS:         480,
		Timebase:    48000,
		SampleFreq:  48000,
		NumChannels: 2,
		Duration:    960,
		WallclockMS: 1717027200000,
	}
	headers := AudioOpusHeaders(meta)
	assert.Len(t, headers, 2)
	assert.Equal(t, ExtMediaType, headers[0].Type)
	assert.Equal(t, MediaTypeAudioOpus, headers[0].ValueVarInt)
	assert.Equal(t, ExtAudioOpus, headers[1].Type)
}

func TestAudioAACHeaders(t *testing.T) {
	meta := AudioMetadata{
		SeqID:       3,
		PTS:         3072,
		Timebase:    48000,
		SampleFreq:  48000,
		NumChannels: 2,
		Duration:    1024,
		WallclockMS: 1717027200000,
	}
	headers := AudioAACHeaders(meta)
	assert.Len(t, headers, 2)
	assert.Equal(t, ExtMediaType, headers[0].Type)
	assert.Equal(t, MediaTypeAudioAACLC, headers[0].ValueVarInt)
	assert.Equal(t, ExtAudioAACLC, headers[1].Type)
}

func TestTextHeaders(t *testing.T) {
	headers := TextHeaders(TextMetadata{SeqID: 42})
	assert.Len(t, headers, 2)
	assert.Equal(t, ExtMediaType, headers[0].Type)
	assert.Equal(t, MediaTypeUTF8Text, headers[0].ValueVarInt)
	assert.Equal(t, ExtUTF8Text, headers[1].Type)
}

func TestMediaType(t *testing.T) {
	t.Run("present", func(t *testing.T) {
		headers := moqtransport.KVPList{
			{Type: ExtMediaType, ValueVarInt: MediaTypeAudioOpus},
		}
		mt, ok := MediaType(headers)
		assert.True(t, ok)
		assert.Equal(t, MediaTypeAudioOpus, mt)
	})
	t.Run("absent", func(t *testing.T) {
		_, ok := MediaType(nil)
		assert.False(t, ok)
	})
}

func TestVideoMetadataRoundTrip(t *testing.T) {
	original := VideoMetadata{
		SeqID:       7,
		PTS:         210,
		DTS:         180,
		Timebase:    30,
		Duration:    1,
		WallclockMS: 1717027200000,
	}
	extradata := []byte{0x01, 0x64, 0x00, 0x1f, 0xff, 0xe1}
	headers := VideoHeaders(original, extradata)

	mt, ok := MediaType(headers)
	require.True(t, ok)
	assert.Equal(t, MediaTypeVideoH264AVCC, mt)

	got, ok, err := ReadVideoMetadata(headers)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, original, got)

	gotExtra, ok := ReadVideoExtradata(headers)
	require.True(t, ok)
	assert.Equal(t, extradata, gotExtra)
}

func TestAudioOpusMetadataRoundTrip(t *testing.T) {
	original := AudioMetadata{
		SeqID:       5,
		PTS:         480,
		Timebase:    48000,
		SampleFreq:  48000,
		NumChannels: 2,
		Duration:    960,
		WallclockMS: 1717027200000,
	}
	headers := AudioOpusHeaders(original)

	got, ok, err := ReadAudioOpusMetadata(headers)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, original, got)
}

func TestAudioAACMetadataRoundTrip(t *testing.T) {
	original := AudioMetadata{
		SeqID:       3,
		PTS:         3072,
		Timebase:    48000,
		SampleFreq:  48000,
		NumChannels: 2,
		Duration:    1024,
		WallclockMS: 1717027200000,
	}
	headers := AudioAACHeaders(original)

	got, ok, err := ReadAudioAACMetadata(headers)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, original, got)
}

func TestTextMetadataRoundTrip(t *testing.T) {
	original := TextMetadata{SeqID: 42}
	headers := TextHeaders(original)

	got, ok, err := ReadTextMetadata(headers)
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, original, got)
}

func TestReadVideoMetadataAbsent(t *testing.T) {
	headers := AudioOpusHeaders(AudioMetadata{})
	_, ok, err := ReadVideoMetadata(headers)
	require.NoError(t, err)
	assert.False(t, ok)
}

func TestReadVideoExtradataAbsent(t *testing.T) {
	headers := VideoHeaders(VideoMetadata{Timebase: 30}, nil)
	_, ok := ReadVideoExtradata(headers)
	assert.False(t, ok)
}
