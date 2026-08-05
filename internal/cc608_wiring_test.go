package internal

import (
	"bytes"
	"testing"

	"github.com/Eyevinn/go-608/cta608"
	"github.com/Eyevinn/locmaf"
	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Eyevinn/moqlivemock/internal/cc608"
)

// groupNrClock is a MoQ group number chosen so its wall-clock second is
// 12:34:56 UTC (45296 s after midnight), giving a deterministic caption to
// assert against: row 13 = "12:34:56.000", row 14 = "GRP 45296".
const groupNrClock = 45296

// captionCodecCases are the three video codecs that carry CTA-608, each with a
// test10s track. They all end up with the same decoded caption, which is the
// point: only the envelope (SEI NAL unit vs metadata OBU) differs.
var captionCodecCases = []struct {
	name  string
	codec cc608.Codec
}{
	{"video_400kbps_avc", cc608.CodecAVC},
	{"video_400kbps_hevc", cc608.CodecHEVC},
	{"video_400kbps_av1", cc608.CodecAV1},
}

// mdatData decodes a raw CMAF chunk (moof+mdat) and returns the mdat payload,
// which for a single-sample chunk is that sample's coded data — a
// 4-byte-length-prefixed NAL stream for AVC/HEVC, a bare OBU sequence for AV1 —
// with the CTA-608 envelope spliced in when captions are on.
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

// decodeGroupCaption feeds a whole group's per-object captions through a fresh
// cta608 decoder in order and returns how many times the displayed screen
// changed plus the text of rows 13 and 14 as the group leaves it.
//
// The decoder is fresh per call and is fed one group's objects only, so a passing
// assertion is also proof the group is self-contained: whatever the caption reads
// at the end was derived from that group's samples alone, which is what lets a
// subscriber join at an arbitrary group.
//
// The screen is read at the end rather than at a change, because the number of
// changes is mode-dependent: pop-on flips its caption on once, while paint-on
// grows it two characters at a time and so changes the screen repeatedly. Both
// leave the same finished caption displayed.
func decodeGroupCaption(t *testing.T, objects []MoQObject, codec cc608.Codec) (changes int, row13, row14 string) {
	t.Helper()
	var dec cta608.Decoder
	for i, obj := range objects {
		f1, _, err := cc608.FieldPairs(mdatData(t, obj), codec)
		require.NoErrorf(t, err, "object %d FieldPairs", i)
		if len(f1) == 0 {
			continue
		}
		require.NoErrorf(t, dec.Feed(f1), "object %d decode", i)
		if dec.Changed() {
			changes++
		}
	}
	return changes, rowText(dec.Screen(), 13), rowText(dec.Screen(), 14)
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
// CC1 caption for AVC, HEVC and AV1, in both caption modes. The mode reaching the
// serve path at all is the point of covering both here; how each one gets the
// caption on screen is pinned in the cc608 package's own tests.
func TestGenMoQGroupCC608_CMAF(t *testing.T) {
	for _, mode := range []cc608.Mode{cc608.ModePaintOn, cc608.ModePopOn} {
		for _, c := range captionCodecCases {
			t.Run(mode.String()+"/"+c.name, func(t *testing.T) {
				asset, err := LoadAsset("../assets/test10s", 1, 1)
				require.NoError(t, err)
				asset.SetCC608Generator(cc608.New(cc608.Config{Enabled: true, Mode: mode}))

				ct := asset.GetTrackByName(c.name)
				require.NotNil(t, ct)

				mg, err := GenMoQGroup(ct, groupNrClock, 1, MoqGroupDurMS, "cmaf")
				require.NoError(t, err)
				require.Equal(t, 25, len(mg.MoQObjects), "25 fps => 25 objects in a 1s group")

				changes, row13, row14 := decodeGroupCaption(t, mg.MoQObjects, c.codec)
				assert.Positive(t, changes, "the caption must reach the screen")
				assert.Equal(t, "12:34:56.000", row13)
				assert.Equal(t, "GRP 45296", row14)
			})
		}
	}
}

// TestGenMoQGroupCC608_NoOp proves that a nil or disabled generator is a
// complete no-op — the produced bytes are byte-identical to captions-off — and,
// as the converse, that an enabled generator really does change the bytes of
// every caption-carrying codec. Audio is left alone either way.
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

	differs := func(t *testing.T, a, b []MoQObject) bool {
		t.Helper()
		require.Equal(t, len(a), len(b))
		for i := range a {
			if !bytes.Equal(a[i], b[i]) {
				return true
			}
		}
		return false
	}

	for _, c := range captionCodecCases {
		t.Run(c.name, func(t *testing.T) {
			// Baseline: no generator installed at all.
			off := genGroup(t, load(t), c.name)

			// Disabled generator must be byte-identical to captions-off.
			disabledAsset := load(t)
			disabledAsset.SetCC608Generator(cc608.New(cc608.Config{Enabled: false}))
			equalObjects(t, off, genGroup(t, disabledAsset, c.name))

			// Enabled generator must change the bytes (envelope added).
			onAsset := load(t)
			onAsset.SetCC608Generator(cc608.New(cc608.Config{Enabled: true}))
			assert.True(t, differs(t, off, genGroup(t, onAsset, c.name)),
				"enabled generator should add a caption envelope to %s video", c.codec)
		})
	}

	// Audio is never captioned, so an enabled generator leaves it untouched.
	audioOff := genGroup(t, load(t), "audio_monotonic_128kbps_aac")
	audioAssetOn := load(t)
	audioAssetOn.SetCC608Generator(cc608.New(cc608.Config{Enabled: true}))
	equalObjects(t, audioOff, genGroup(t, audioAssetOn, "audio_monotonic_128kbps_aac"))
}

// TestGenMoQGroupCC608_LOCMAF verifies the CTA-608 captions survive the LOCMAF
// encode/decode round-trip: with captions on, a LOCMAF-packaged video group is
// decoded back into canonical CMAF chunks (the mdat payload is preserved
// untouched by LOCMAF), and the recovered elementary stream still carries a
// decodable CC1 caption for AVC, HEVC and AV1.
func TestGenMoQGroupCC608_LOCMAF(t *testing.T) {
	for _, c := range captionCodecCases {
		t.Run(c.name, func(t *testing.T) {
			asset, err := LoadAsset("../assets/test10s", 1, 1)
			require.NoError(t, err)
			asset.SetCC608Generator(cc608.New(cc608.Config{Enabled: true}))

			ct := asset.GetTrackByName(c.name)
			require.NotNil(t, ct)

			mg, err := GenMoQGroup(ct, groupNrClock, 1, MoqGroupDurMS, "locmaf")
			require.NoError(t, err)
			require.Equal(t, 25, len(mg.MoQObjects), "25 fps => 25 objects in a 1s group")

			// Decode each LOCMAF object back into a canonical CMAF chunk. The
			// LOCMAF codec preserves the mdat payload (and thus the spliced SEI)
			// byte-for-byte, so the reconstructed chunk is decodable exactly like
			// the native CMAF path.
			moov := ct.SpecData.GetInit().Moov
			decState := locmaf.NewState()
			chunks := make([]MoQObject, len(mg.MoQObjects))
			for i, obj := range mg.MoQObjects {
				eff, raw, err := locmaf.Decode(obj, decState, moov)
				require.NoErrorf(t, err, "object %d decode", i)
				require.Nil(t, raw)
				chunk, err := locmaf.ReconstructCanonical(moov, eff)
				require.NoErrorf(t, err, "object %d reconstruct", i)
				chunks[i] = chunk
			}

			changes, row13, row14 := decodeGroupCaption(t, chunks, c.codec)
			assert.Positive(t, changes, "the caption must reach the screen")
			assert.Equal(t, "12:34:56.000", row13)
			assert.Equal(t, "GRP 45296", row14)
		})
	}
}

// TestGenMoQGroupCC608_EncryptedPreEncryption proves the CTA-608 captions are
// spliced BEFORE encryption: createFragment splices the envelope into each
// frame's coded sample, and only then does GenCMAFChunk call encryptFragment, so
// the caption rides inside the encrypted payload. It builds an ECCP/ClearKey
// protected track with captions on, generates an encrypted CMAF group, decrypts
// every object with the known key, and decodes the CC1 caption back out. The
// pre-decrypt objects are also confirmed to differ from their plaintext, so the
// caption really travels through real ciphertext rather than a clear payload.
//
// AV1 is the interesting case: its CENC binding derives the subsample ranges
// from the sample's OBU structure, so a caption OBU inserted ahead of the frame
// OBU has to end up in the clear part and leave the tile ranges intact.
func TestGenMoQGroupCC608_EncryptedPreEncryption(t *testing.T) {
	kidStr := "39112233445566778899aabbccddeeff"
	keyStr := "40112233445566778899aabbccddeeff"
	ivStr := "41112233445566778899aabbccddeeff"

	for _, c := range captionCodecCases {
		t.Run(c.name, func(t *testing.T) {
			eccp, err := ParseCENCflags("cbcs", kidStr, keyStr, ivStr, "http://localhost:8081/clearkey")
			require.NoError(t, err)
			asset, err := LoadAssetWithProtection("../assets/test10s", 1, 1, nil, eccp)
			require.NoError(t, err)
			asset.SetCC608Generator(cc608.New(cc608.Config{Enabled: true}))

			ct := asset.GetTrackByName(c.name + "_eccp")
			require.NotNil(t, ct, "protected track must exist")
			require.NotEmpty(t, ct.contentProtectionRefIDs, "track must be encrypted")

			mg, err := GenMoQGroup(ct, groupNrClock, 1, MoqGroupDurMS, "cmaf")
			require.NoError(t, err)
			require.Equal(t, 25, len(mg.MoQObjects), "25 fps => 25 objects in a 1s group")

			// Recover the CENC DecryptInfo from the (encrypted) init segment,
			// then decrypt every object with the known content key.
			initData, err := ct.SpecData.GenCMAFInitData()
			require.NoError(t, err)
			_, _, ipd, err := DecryptInit(initData)
			require.NoError(t, err)

			dec := make([]MoQObject, len(mg.MoQObjects))
			for i, obj := range mg.MoQObjects {
				plain, err := DecryptFragment(obj, ipd, eccp.cenc.key)
				require.NoErrorf(t, err, "decrypt object %d", i)
				// Compare the mdat payloads, not the whole objects: decryption
				// also strips senc/saiz/saio, so the moof always differs and a
				// whole-object comparison would pass even on an all-clear
				// sample — precisely the AV1 failure mode (empty tile ranges)
				// this test exists to rule out.
				require.NotEqualf(t, mdatData(t, obj), mdatData(t, plain),
					"object %d media payload must actually be encrypted on the wire", i)
				dec[i] = plain
			}

			changes, row13, row14 := decodeGroupCaption(t, dec, c.codec)
			assert.Positive(t, changes, "the caption must reach the screen after decryption")
			assert.Equal(t, "12:34:56.000", row13)
			assert.Equal(t, "GRP 45296", row14)
		})
	}
}
