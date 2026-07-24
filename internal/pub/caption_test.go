package pub

import (
	"testing"

	"github.com/Eyevinn/go-608/carriage"
	"github.com/Eyevinn/go-608/cta608"
	"github.com/Eyevinn/mp4ff/avc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Eyevinn/moqlivemock/internal"
	"github.com/Eyevinn/moqlivemock/internal/cc608"
)

// groupNrClock is a group number whose wall-clock second is 12:34:56 UTC. The
// test assets are 25 fps with a 1 s GOP, so for every raw-frame path the caption
// clock anchor equals the group number, giving a deterministic caption to
// assert: row 13 = "12:34:56.000", row 14 = "GRP 45296".
const groupNrClock = 45296

// rowText concatenates the text of the decoded screen row with the given index.
func rowText(s cta608.Screen, idx int) string {
	for _, r := range s.Rows {
		if r.Index != idx {
			continue
		}
		var text string
		for _, run := range r.Runs {
			text += run.Text
		}
		return text
	}
	return ""
}

// captionedVideoTrack loads test10s, enables captions, and returns the named
// video track (a copy that carries the installed generator).
func captionedVideoTrack(t *testing.T, name string) *internal.ContentTrack {
	t.Helper()
	asset, err := internal.LoadAsset("../../assets/test10s", 1, 1)
	require.NoError(t, err)
	asset.SetCC608Generator(cc608.New(cc608.Config{Enabled: true}))
	ct := asset.GetTrackByName(name)
	require.NotNil(t, ct)
	return ct
}

// decodeFrames feeds a sequence of per-frame AVCC sample datas through the
// cta608 decoder and returns the flip count and the last-flip row 13/14 text.
func decodeFrames(t *testing.T, frames [][]byte, codec carriage.Codec) (flips int, row13, row14 string) {
	t.Helper()
	var dec cta608.Decoder
	for i, data := range frames {
		nalus, err := avc.GetNalusFromSample(data)
		require.NoErrorf(t, err, "frame %d", i)
		f1, _, err := carriage.FieldPairs(nalus, codec)
		require.NoErrorf(t, err, "frame %d FieldPairs", i)
		if len(f1) == 0 {
			continue
		}
		require.NoErrorf(t, dec.Feed(f1), "frame %d decode", i)
		if dec.Changed() {
			flips++
			row13 = rowText(dec.Screen(), 13)
			row14 = rowText(dec.Screen(), 14)
		}
	}
	return flips, row13, row14
}

// TestVideoCaptioner_LOC mirrors PublishLOCTrack's grouping and splicing exactly
// (same CalcLOCGroupRange range, same per-frame spliceFrame indexing) and proves
// the produced frames carry a decodable CC1 caption for both AVC and HEVC.
func TestVideoCaptioner_LOC(t *testing.T) {
	cases := []struct {
		name  string
		codec carriage.Codec
	}{
		{"video_400kbps_avc", carriage.CodecAVC},
		{"video_400kbps_hevc", carriage.CodecHEVC},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ct := captionedVideoTrack(t, c.name)
			cap := newVideoCaptioner(ct)
			require.True(t, cap.on, "captioner should be enabled for AVC/HEVC")
			require.Equal(t, c.codec, cap.codec)

			startNr, endNr := internal.CalcLOCGroupRange(ct, groupNrClock, internal.MoqGroupDurMS)
			require.Equal(t, uint64(25), endNr-startNr)
			sei := cap.schedule(int64(groupNrClock), startNr, endNr)
			require.NotNil(t, sei)
			require.Len(t, sei, 25)

			var frames [][]byte
			for sampleNr := startNr; sampleNr < endNr; sampleNr++ {
				_, origNr := ct.CalcSample(sampleNr)
				frames = append(frames, cap.spliceFrame(ct.Samples[origNr].Data, sei, sampleNr, startNr))
			}
			flips, row13, row14 := decodeFrames(t, frames, c.codec)
			assert.Equal(t, 1, flips, "one pop-on cue per group")
			assert.Equal(t, "12:34:56.000", row13)
			assert.Equal(t, "GRP 45296", row14)
		})
	}
}

// TestVideoCaptioner_MoqMI mirrors publishMoqMIVideo's GOP grouping and anchor
// (anchorSec = startSample*sampleDur/timebase) for the AVC video0 track and
// proves the produced frames carry a decodable CC1 caption.
func TestVideoCaptioner_MoqMI(t *testing.T) {
	ct := captionedVideoTrack(t, "video_400kbps_avc")
	cap := newVideoCaptioner(ct)
	require.True(t, cap.on)

	gopLen := uint64(ct.GopLength)
	require.NotZero(t, gopLen)
	timebase := uint64(ct.TimeScale)
	sampleDur := uint64(ct.SampleDur)

	startSample := uint64(groupNrClock) * gopLen
	endSample := startSample + gopLen
	anchorSec := int64(startSample * sampleDur / timebase)
	require.Equal(t, int64(groupNrClock), anchorSec, "1s GOP => anchor second == group number")

	sei := cap.schedule(anchorSec, startSample, endSample)
	require.NotNil(t, sei)
	require.Len(t, sei, int(gopLen))

	var frames [][]byte
	for sampleNr := startSample; sampleNr < endSample; sampleNr++ {
		_, origNr := ct.CalcSample(sampleNr)
		frames = append(frames, cap.spliceFrame(ct.Samples[origNr].Data, sei, sampleNr, startSample))
	}
	flips, row13, row14 := decodeFrames(t, frames, carriage.CodecAVC)
	assert.Equal(t, 1, flips)
	assert.Equal(t, "12:34:56.000", row13)
	assert.Equal(t, "GRP 45296", row14)
}

// TestVideoCaptioner_Disabled proves the captioner is a complete no-op when
// captions are off, on a non-video track, or on an unsupported codec (AV1):
// schedule returns nil and spliceFrame returns the frame unchanged.
func TestVideoCaptioner_Disabled(t *testing.T) {
	asset, err := internal.LoadAsset("../../assets/test10s", 1, 1)
	require.NoError(t, err)

	// No generator installed at all.
	ct := asset.GetTrackByName("video_400kbps_avc")
	require.NotNil(t, ct)
	cap := newVideoCaptioner(ct)
	assert.False(t, cap.on)
	assert.Nil(t, cap.schedule(groupNrClock, 0, 25))
	orig := ct.Samples[0].Data
	assert.Equal(t, orig, cap.spliceFrame(orig, nil, 0, 0))

	// Generator enabled, but AV1 is an unsupported caption codec.
	asset.SetCC608Generator(cc608.New(cc608.Config{Enabled: true}))
	av1 := asset.GetTrackByName("video_400kbps_av1")
	require.NotNil(t, av1)
	assert.False(t, newVideoCaptioner(av1).on, "AV1 must not be captioned")

	// Generator enabled, but audio is not a video track.
	aud := asset.GetTrackByName("audio_monotonic_128kbps_aac")
	require.NotNil(t, aud)
	assert.False(t, newVideoCaptioner(aud).on, "audio must not be captioned")
}
