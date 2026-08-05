package pub

import (
	"testing"

	"github.com/Eyevinn/go-608/cta608"
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

// decodeFrames feeds a sequence of per-frame coded samples through a fresh cta608
// decoder and returns how many times the displayed screen changed plus the row
// 13/14 text the last frame leaves on it.
//
// The screen is read at the end rather than at a change because the change count
// is mode-dependent: pop-on flips its caption on once, paint-on grows it two
// characters at a time. Both leave the same finished caption displayed, which is
// what these tests are about.
func decodeFrames(t *testing.T, frames [][]byte, codec cc608.Codec) (changes int, row13, row14 string) {
	t.Helper()
	var dec cta608.Decoder
	for i, data := range frames {
		f1, _, err := cc608.FieldPairs(data, codec)
		require.NoErrorf(t, err, "frame %d FieldPairs", i)
		if len(f1) == 0 {
			continue
		}
		require.NoErrorf(t, dec.Feed(f1), "frame %d decode", i)
		if dec.Changed() {
			changes++
		}
	}
	return changes, rowText(dec.Screen(), 13), rowText(dec.Screen(), 14)
}

// TestVideoCaptioner_LOC mirrors PublishLOCTrack's grouping and splicing exactly
// (same CalcLOCGroupRange range, same per-frame spliceFrame indexing) and proves
// the produced frames carry a decodable CC1 caption for AVC, HEVC and AV1.
func TestVideoCaptioner_LOC(t *testing.T) {
	cases := []struct {
		name  string
		codec cc608.Codec
	}{
		{"video_400kbps_avc", cc608.CodecAVC},
		{"video_400kbps_hevc", cc608.CodecHEVC},
		{"video_400kbps_av1", cc608.CodecAV1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ct := captionedVideoTrack(t, c.name)
			cap := newVideoCaptioner(ct)
			require.True(t, cap.on, "captioner should be enabled for %s", c.codec)
			require.Equal(t, c.codec, cap.codec)

			startNr, endNr := internal.CalcLOCGroupRange(ct, groupNrClock, internal.MoqGroupDurMS)
			require.Equal(t, uint64(25), endNr-startNr)
			sched := cap.schedule(int64(groupNrClock), startNr, endNr)
			require.NotNil(t, sched)
			require.Len(t, sched, 25)

			var frames [][]byte
			for sampleNr := startNr; sampleNr < endNr; sampleNr++ {
				_, origNr := ct.CalcSample(sampleNr)
				frames = append(frames, cap.spliceFrame(ct.Samples[origNr].Data, sched, sampleNr, startNr))
			}
			changes, row13, row14 := decodeFrames(t, frames, c.codec)
			assert.Positive(t, changes, "the caption must reach the screen")
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

	sched := cap.schedule(anchorSec, startSample, endSample)
	require.NotNil(t, sched)
	require.Len(t, sched, int(gopLen))

	var frames [][]byte
	for sampleNr := startSample; sampleNr < endSample; sampleNr++ {
		_, origNr := ct.CalcSample(sampleNr)
		frames = append(frames, cap.spliceFrame(ct.Samples[origNr].Data, sched, sampleNr, startSample))
	}
	changes, row13, row14 := decodeFrames(t, frames, cc608.CodecAVC)
	assert.Positive(t, changes, "the caption must reach the screen")
	assert.Equal(t, "12:34:56.000", row13)
	assert.Equal(t, "GRP 45296", row14)
}

// TestVideoCaptioner_Disabled proves the captioner is a complete no-op when
// captions are off or the track is not captionable: schedule returns nil and
// spliceFrame returns the frame unchanged.
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

	// Generator enabled, but audio is not a video track.
	asset.SetCC608Generator(cc608.New(cc608.Config{Enabled: true}))
	aud := asset.GetTrackByName("audio_monotonic_128kbps_aac")
	require.NotNil(t, aud)
	assert.False(t, newVideoCaptioner(aud).on, "audio must not be captioned")
}
