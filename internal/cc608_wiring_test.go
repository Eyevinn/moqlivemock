package internal

import (
	"bytes"
	"testing"

	"github.com/Eyevinn/go-608/carriage"
	"github.com/Eyevinn/go-608/cta608"
	"github.com/Eyevinn/mp4ff/avc"
	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Eyevinn/moqlivemock/internal/cc608"
)

// groupNrClock is a MoQ group number chosen so its wall-clock second is
// 12:34:56 UTC (45296 s after midnight), giving a deterministic caption to
// assert against: row 13 = "12:34:56.000", row 14 = "GRP 45296".
const groupNrClock = 45296

// mdatData decodes a raw CMAF chunk (moof+mdat) and returns the mdat payload,
// which for a single-sample chunk is that sample's 4-byte-length-prefixed AVCC
// NAL stream (with the CTA-608 SEI spliced in when captions are on).
func mdatData(t *testing.T, chunk []byte) []byte {
	t.Helper()
	r := bytes.NewReader(chunk)
	moofBox, err := mp4.DecodeBox(0, r)
	require.NoError(t, err)
	mdatBox, err := mp4.DecodeBox(moofBox.Size(), r)
	require.NoError(t, err)
	mdat, ok := mdatBox.(*mp4.MdatBox)
	require.True(t, ok, "second box should be mdat, got %T", mdatBox)
	return mdat.Data
}

// decodeGroupCaption feeds a whole group's per-object SEI through the cta608
// decoder in order and returns the number of on-screen flips plus the text of
// rows 13 and 14 at the last flip.
func decodeGroupCaption(t *testing.T, objects []MoQObject, codec carriage.Codec) (flips int, row13, row14 string) {
	t.Helper()
	var dec cta608.Decoder
	for i, obj := range objects {
		nalus, err := avc.GetNalusFromSample(mdatData(t, obj))
		require.NoErrorf(t, err, "object %d", i)
		f1, _, err := carriage.FieldPairs(nalus, codec)
		require.NoErrorf(t, err, "object %d FieldPairs", i)
		if len(f1) == 0 {
			continue
		}
		require.NoErrorf(t, dec.Feed(f1), "object %d decode", i)
		if dec.Changed() {
			flips++
			row13 = rowText(dec.Screen(), 13)
			row14 = rowText(dec.Screen(), 14)
		}
	}
	return flips, row13, row14
}

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

// TestGenMoQGroupCC608_CMAF verifies that with an enabled generator installed
// via SetCC608Generator, a CMAF-packaged video group carries a decodable CTA-608
// CC1 caption for both AVC and HEVC. It also confirms one flip per group (one
// pop-on cue per wall-clock second).
func TestGenMoQGroupCC608_CMAF(t *testing.T) {
	cases := []struct {
		name  string
		codec carriage.Codec
	}{
		{"video_400kbps_avc", carriage.CodecAVC},
		{"video_400kbps_hevc", carriage.CodecHEVC},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			asset, err := LoadAsset("../assets/test10s", 1, 1)
			require.NoError(t, err)
			asset.SetCC608Generator(cc608.New(cc608.Config{Enabled: true}))

			ct := asset.GetTrackByName(c.name)
			require.NotNil(t, ct)

			mg, err := GenMoQGroup(ct, groupNrClock, 1, MoqGroupDurMS, "cmaf")
			require.NoError(t, err)
			require.Equal(t, 25, len(mg.MoQObjects), "25 fps => 25 objects in a 1s group")

			flips, row13, row14 := decodeGroupCaption(t, mg.MoQObjects, c.codec)
			assert.Equal(t, 1, flips, "one pop-on cue per group")
			assert.Equal(t, "12:34:56.000", row13)
			assert.Equal(t, "GRP 45296", row14)
		})
	}
}

// TestGenMoQGroupCC608_NoOp proves that a nil or disabled generator is a
// complete no-op: the produced bytes are identical to captions-off, and no SEI
// is present. It also confirms AV1 video is skipped even with captions enabled.
func TestGenMoQGroupCC608_NoOp(t *testing.T) {
	load := func(t *testing.T) *Asset {
		t.Helper()
		a, err := LoadAsset("../assets/test10s", 1, 1)
		require.NoError(t, err)
		return a
	}

	genGroup := func(t *testing.T, a *Asset, name string) []MoQObject {
		t.Helper()
		ct := a.GetTrackByName(name)
		require.NotNil(t, ct)
		mg, err := GenMoQGroup(ct, groupNrClock, 1, MoqGroupDurMS, "cmaf")
		require.NoError(t, err)
		return mg.MoQObjects
	}

	equalObjects := func(t *testing.T, want, got []MoQObject) {
		t.Helper()
		require.Equal(t, len(want), len(got))
		for i := range want {
			assert.Truef(t, bytes.Equal(want[i], got[i]), "object %d differs", i)
		}
	}

	// Baseline: no generator installed at all.
	off := genGroup(t, load(t), "video_400kbps_avc")

	// Disabled generator must be byte-identical to captions-off.
	disabledAsset := load(t)
	disabledAsset.SetCC608Generator(cc608.New(cc608.Config{Enabled: false}))
	equalObjects(t, off, genGroup(t, disabledAsset, "video_400kbps_avc"))

	// Enabled generator must change the AVC bytes (SEI added).
	onAsset := load(t)
	onAsset.SetCC608Generator(cc608.New(cc608.Config{Enabled: true}))
	on := genGroup(t, onAsset, "video_400kbps_avc")
	require.Equal(t, len(off), len(on))
	differs := false
	for i := range off {
		if !bytes.Equal(off[i], on[i]) {
			differs = true
			break
		}
	}
	assert.True(t, differs, "enabled generator should add SEI to AVC video")

	// AV1 must be skipped (unsupported codec) even with captions enabled.
	av1Off := genGroup(t, load(t), "video_400kbps_av1")
	av1AssetOn := load(t)
	av1AssetOn.SetCC608Generator(cc608.New(cc608.Config{Enabled: true}))
	equalObjects(t, av1Off, genGroup(t, av1AssetOn, "video_400kbps_av1"))
}
