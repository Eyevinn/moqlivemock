package sub

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/Eyevinn/locmaf"
	"github.com/Eyevinn/moqlivemock/internal"
	"github.com/Eyevinn/mp4ff/bits"
	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/stretchr/testify/require"
)

func TestDecryptInitUpdatesCatalogTrack(t *testing.T) {
	const (
		kidStr = "39112233445566778899aabbccddeeff"
		keyStr = "40112233445566778899aabbccddeeff"
		ivStr  = "41112233445566778899aabbccddeeff"
	)

	licenseServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)

		var req clearKeyRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
		require.Len(t, req.Kids, 1)

		resp := clearKeyResponse{
			Keys: []keyInfo{{
				Kty: "oct",
				K:   base64.RawURLEncoding.EncodeToString(mustKey(t, keyStr)),
				Kid: req.Kids[0],
			}},
		}
		require.NoError(t, json.NewEncoder(w).Encode(resp))
	}))
	defer licenseServer.Close()

	eccp, err := internal.ParseCENCflags("cbcs", kidStr, keyStr, ivStr, licenseServer.URL)
	require.NoError(t, err)

	asset, err := internal.LoadAssetWithProtection(filepath.Join("..", "..", "assets", "test10s"), 1, 1, nil, eccp)
	require.NoError(t, err)

	catalog, err := asset.GenCMAFCatalogEntry("cmsf/eccp-cbcs", internal.ProtectionECCP, 0)
	require.NoError(t, err)

	var encryptedTrack *internal.Track
	for i := range catalog.Tracks {
		if catalog.Tracks[i].Name == "video_400kbps_avc_eccp" {
			encryptedTrack = &catalog.Tracks[i]
			break
		}
	}
	require.NotNil(t, encryptedTrack)

	h := &Handler{
		catalog: catalog,
		cenc: &CENC{
			DecryptInfo:   make(map[string]mp4.DecryptInfo),
			ProtectedMoov: make(map[string]*mp4.MoovBox),
		},
	}

	encryptedInit, ok := catalog.InitDataFor(encryptedTrack)
	require.True(t, ok)
	decryptedInit, err := h.decryptInit(encryptedTrack, encryptedInit)
	require.NoError(t, err)
	require.NotEqual(t, encryptedInit, decryptedInit)
	require.Contains(t, h.cenc.DecryptInfo, encryptedTrack.Name)

	initBytes, err := base64.StdEncoding.DecodeString(decryptedInit)
	require.NoError(t, err)
	f, err := mp4.DecodeFileSR(bits.NewFixedSliceReader(initBytes))
	require.NoError(t, err)
	require.NotNil(t, f.Init)
	require.Equal(t, "avc1", f.Init.Moov.Trak.Mdia.Minf.Stbl.Stsd.Children[0].Type())
}

// TestDecompressLocmafObjectRoundTrip covers the mlmsub-side wrapper
// `decompressLocmafObject`. The single-sample track matters: its size
// is never on the wire and must derive from the mdat-payload length
// (a v0.2-era regression sliced the mdat tail off before decoding and
// produced sample_size = 0 fragments that ffmpeg rejected). The
// canonical reconstruction may hoist a uniform size into the tfhd
// default, so assertions resolve sizes through the CMAF defaults
// chain.
func TestDecompressLocmafObjectRoundTrip(t *testing.T) {
	asset, err := internal.LoadAsset(filepath.Join("..", "..", "assets", "test10s"), 1, 1)
	require.NoError(t, err)

	for _, trackName := range []string{
		"video_400kbps_avc",
		"audio_monotonic_128kbps_aac", // single-sample-per-chunk → mdat-length derivation
	} {
		t.Run(trackName, func(t *testing.T) {
			track := asset.GetTrackByName(trackName)
			require.NotNil(t, track)

			state := locmaf.NewState()
			compressed, err := track.GenLocmafChunk(0, 0, 1, state)
			require.NoError(t, err)

			rxState := locmaf.NewState()
			got, err := decompressLocmafObject(compressed, track.SpecData.GetInit().Moov, rxState)
			require.NoError(t, err)
			require.NotNil(t, got)

			gotMoof, gotMdat := decodeFragment(t, got)

			expected, err := track.GenCMAFChunk(0, 0, 1)
			require.NoError(t, err)
			expectedMoof, expectedMdat := decodeFragment(t, expected)

			// The resolved sample sizes MUST be non-zero and sum to
			// the mdat payload length — the v0.2-era regression
			// produced sample_size == 0.
			trex := track.SpecData.GetInit().Moov.Mvex.Trex
			gotSizes := resolveSampleSizes(t, gotMoof, len(gotMdat.Data), trex)
			require.NotEmpty(t, gotSizes)
			var sumSizes uint64
			for _, size := range gotSizes {
				require.NotZero(t, size, "reconstructed sample_size must be non-zero")
				sumSizes += uint64(size)
			}
			require.Equal(t, uint64(len(gotMdat.Data)), sumSizes,
				"sum of reconstructed sample sizes must equal mdat payload length")

			// Per-sample structure must match the unmodified CMAF.
			expectedSizes := resolveSampleSizes(t, expectedMoof, len(expectedMdat.Data), trex)
			require.Equal(t, expectedSizes, gotSizes)
			require.Equal(t, expectedMdat.Data, gotMdat.Data)
		})
	}
}

// TestDecompressLocmafClearKeyRoundTrip exercises the full
// mlmpub→mlmsub ClearKey (ECCP) data plane for LOCMAF in BOTH cbcs
// and cenc modes, over audio (no subsamples) and video (clear+protected
// subsamples): the publisher builds an encrypted wire chunk, the
// subscriber runs it through `decompressLocmafObject`, then decrypts
// the reconstructed fragment. The decrypted payload must equal the
// plain-CMAF chunk for the corresponding clear track.
func TestDecompressLocmafClearKeyRoundTrip(t *testing.T) {
	const keyStr = "40112233445566778899aabbccddeeff"

	tracks := []struct {
		clear, protected string
	}{
		{"audio_monotonic_128kbps_aac", "audio_monotonic_128kbps_aac_eccp"},
		{"video_400kbps_avc", "video_400kbps_avc_eccp"},
	}

	for _, scheme := range []string{"cbcs", "cenc"} {
		t.Run(scheme, func(t *testing.T) {
			eccp, err := internal.ParseCENCflags(
				scheme,
				"39112233445566778899aabbccddeeff",
				keyStr,
				"41112233445566778899aabbccddeeff",
				"http://localhost:8081/clearkey",
			)
			require.NoError(t, err)

			asset, err := internal.LoadAssetWithProtection(
				filepath.Join("..", "..", "assets", "test10s"), 1, 1, nil, eccp)
			require.NoError(t, err)

			key, err := mp4.UnpackKey(keyStr)
			require.NoError(t, err)

			for _, tc := range tracks {
				t.Run(tc.clear, func(t *testing.T) {
					clearTrack := asset.GetTrackByName(tc.clear)
					protectedTrack := asset.GetTrackByName(tc.protected)
					require.NotNil(t, clearTrack)
					require.NotNil(t, protectedTrack)

					protectedInit, err := protectedTrack.SpecData.GenCMAFInitData()
					require.NoError(t, err)
					_, _, decryptInfo, err := internal.DecryptInit(protectedInit)
					require.NoError(t, err)

					state := locmaf.NewState()
					compressed, err := protectedTrack.GenLocmafChunk(0, 0, 1, state)
					require.NoError(t, err)

					rxState := locmaf.NewState()
					got, err := decompressLocmafObject(compressed,
						protectedTrack.SpecData.GetInit().Moov, rxState)
					require.NoError(t, err)
					require.NotNil(t, got)

					// Sanity: resolved sample sizes must be non-zero.
					gotMoof, gotMdat := decodeFragment(t, got)
					trex := protectedTrack.SpecData.GetInit().Moov.Mvex.Trex
					for i, size := range resolveSampleSizes(t, gotMoof, len(gotMdat.Data), trex) {
						require.NotZero(t, size, "encrypted sample %d size must be non-zero", i)
					}
					require.Greater(t, len(gotMdat.Data), 0)

					got, err = internal.DecryptFragment(got, decryptInfo, key)
					require.NoError(t, err)

					expected, err := clearTrack.GenCMAFChunk(0, 0, 1)
					require.NoError(t, err)
					_, expectedMdat := decodeFragment(t, expected)
					_, decryptedMdat := decodeFragment(t, got)
					require.Equal(t, expectedMdat.Data, decryptedMdat.Data,
						"decrypted payload must match the clear-track reference")
				})
			}
		})
	}
}

func decodeFragment(t *testing.T, data []byte) (*mp4.MoofBox, *mp4.MdatBox) {
	t.Helper()

	reader := bytes.NewReader(data)

	moofBox, err := mp4.DecodeBox(0, reader)
	require.NoError(t, err)
	moof, ok := moofBox.(*mp4.MoofBox)
	require.True(t, ok)

	mdatBox, err := mp4.DecodeBox(moof.Size(), reader)
	require.NoError(t, err)
	mdat, ok := mdatBox.(*mp4.MdatBox)
	require.True(t, ok)

	return moof, mdat
}

func mustKey(t *testing.T, key string) []byte {
	t.Helper()

	decoded, err := mp4.UnpackKey(key)
	require.NoError(t, err)
	return decoded
}

// resolveSampleSizes resolves each sample's size through the CMAF
// defaults chain — trun per-sample, tfhd default, non-zero trex
// default, single-sample mdat length — mirroring how a CMAF parser
// reads the canonical reconstruction.
func resolveSampleSizes(t *testing.T, moof *mp4.MoofBox, mdatLen int, trex *mp4.TrexBox) []uint32 {
	t.Helper()
	trun, tfhd := moof.Traf.Trun, moof.Traf.Tfhd
	n := len(trun.Samples)
	out := make([]uint32, n)
	switch {
	case trun.HasSampleSize():
		for i, s := range trun.Samples {
			out[i] = s.Size
		}
	case tfhd.HasDefaultSampleSize():
		for i := range out {
			out[i] = tfhd.DefaultSampleSize
		}
	case trex != nil && trex.DefaultSampleSize != 0:
		for i := range out {
			out[i] = trex.DefaultSampleSize
		}
	case n == 1:
		out[0] = uint32(mdatLen) //nolint:gosec // test payloads are small
	}
	return out
}
