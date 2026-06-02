package sub

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/Eyevinn/moqlivemock/internal"
	"github.com/Eyevinn/moqlivemock/internal/locmafv02"
	"github.com/Eyevinn/mp4ff/bits"
	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/stretchr/testify/require"
)

func TestDecompressLocmafObjectRoundTrip(t *testing.T) {
	asset, err := internal.LoadAsset(filepath.Join("..", "..", "assets", "test10s"), 1, 1)
	require.NoError(t, err)

	track := asset.GetTrackByName("video_400kbps_avc")
	require.NotNil(t, track)

	compressor := &internal.MoofDeltaCompressor{}
	compressed, err := track.GenLocmafChunk(0, 0, 1, compressor)
	require.NoError(t, err)

	expected, err := track.GenCMAFChunk(0, 0, 1)
	require.NoError(t, err)

	decompressor := &internal.MoofDeltaDecompressor{}
	got, err := decompressLocmafObject(compressed, 0, track.SpecData.GetInit().Moov, decompressor)
	require.NoError(t, err)

	expectedMoof, expectedMdat := decodeFragment(t, expected)
	gotMoof, gotMdat := decodeFragment(t, got)

	expectedCompressed, err := internal.CompressMoof(expectedMoof, track.SpecData.GetInit().Moov)
	require.NoError(t, err)
	gotCompressed, err := internal.CompressMoof(gotMoof, track.SpecData.GetInit().Moov)
	require.NoError(t, err)

	require.Equal(t, expectedCompressed, gotCompressed)
	require.Equal(t, expectedMdat.Data, gotMdat.Data)
}

func TestDecompressLocmafEncryptedAudioRoundTrip(t *testing.T) {
	const keyStr = "40112233445566778899aabbccddeeff"

	eccp, err := internal.ParseCENCflags(
		"cbcs",
		"39112233445566778899aabbccddeeff",
		keyStr,
		"41112233445566778899aabbccddeeff",
		"http://localhost:8081/clearkey",
	)
	require.NoError(t, err)

	asset, err := internal.LoadAssetWithProtection(filepath.Join("..", "..", "assets", "test10s"), 1, 1, nil, eccp)
	require.NoError(t, err)

	clearTrack := asset.GetTrackByName("audio_monotonic_128kbps_aac")
	protectedTrack := asset.GetTrackByName("audio_monotonic_128kbps_aac_eccp")
	require.NotNil(t, clearTrack)
	require.NotNil(t, protectedTrack)

	protectedInit, err := protectedTrack.SpecData.GenCMAFInitData()
	require.NoError(t, err)
	_, _, decryptInfo, err := internal.DecryptInit(protectedInit)
	require.NoError(t, err)

	compressor := &internal.MoofDeltaCompressor{}
	compressed, err := protectedTrack.GenLocmafChunk(0, 0, 1, compressor)
	require.NoError(t, err)

	decompressor := &internal.MoofDeltaDecompressor{}
	got, err := decompressLocmafObject(compressed, 0, protectedTrack.SpecData.GetInit().Moov, decompressor)
	require.NoError(t, err)

	key, err := mp4.UnpackKey(keyStr)
	require.NoError(t, err)
	got, err = internal.DecryptFragment(got, decryptInfo, key)
	require.NoError(t, err)

	expected, err := clearTrack.GenCMAFChunk(0, 0, 1)
	require.NoError(t, err)

	expectedMoof, expectedMdat := decodeFragment(t, expected)
	gotMoof, gotMdat := decodeFragment(t, got)

	expectedCompressed, err := internal.CompressMoof(expectedMoof, clearTrack.SpecData.GetInit().Moov)
	require.NoError(t, err)
	gotCompressed, err := internal.CompressMoof(gotMoof, clearTrack.SpecData.GetInit().Moov)
	require.NoError(t, err)

	require.Equal(t, expectedCompressed, gotCompressed)
	require.Equal(t, expectedMdat.Data, gotMdat.Data)
}

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

	catalog, err := asset.GenCMAFCatalogEntry("cmaf/eccp-cbcs", internal.ProtectionECCP, 0, "cmaf")
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

	encryptedInit := encryptedTrack.InitData
	require.NoError(t, h.decryptInit(encryptedTrack))
	require.NotEqual(t, encryptedInit, encryptedTrack.InitData)
	require.Contains(t, h.cenc.DecryptInfo, encryptedTrack.Name)

	initBytes, err := base64.StdEncoding.DecodeString(encryptedTrack.InitData)
	require.NoError(t, err)
	f, err := mp4.DecodeFileSR(bits.NewFixedSliceReader(initBytes))
	require.NoError(t, err)
	require.NotNil(t, f.Init)
	require.Equal(t, "avc1", f.Init.Moov.Trak.Mdia.Minf.Stbl.Stsd.Children[0].Type())
}

func TestDecryptInitKeepsProtectedMoovForLocmafEncryptedAudio(t *testing.T) {
	const (
		kidStr = "39112233445566778899aabbccddeeff"
		keyStr = "40112233445566778899aabbccddeeff"
		ivStr  = "41112233445566778899aabbccddeeff"
	)

	licenseServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req clearKeyRequest
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))
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

	catalog, err := asset.GenCMAFCatalogEntry("locmaf/eccp-cbcs", internal.ProtectionECCP, 0, "locmaf")
	require.NoError(t, err)

	var track *internal.Track
	for i := range catalog.Tracks {
		if catalog.Tracks[i].Name == "audio_monotonic_128kbps_aac_eccp" {
			track = &catalog.Tracks[i]
			break
		}
	}
	require.NotNil(t, track)

	h := &Handler{
		catalog: catalog,
		cenc: &CENC{
			DecryptInfo:   make(map[string]mp4.DecryptInfo),
			ProtectedMoov: make(map[string]*mp4.MoovBox),
		},
	}
	initData, moov, err := locmafInitData(*track)
	require.NoError(t, err)
	track.InitData = initData
	h.cenc.ProtectedMoov[track.Name] = moov
	require.NoError(t, h.decryptInit(track))

	protectedTrack := asset.GetTrackByName(track.Name)
	require.NotNil(t, protectedTrack)
	compressor := &internal.MoofDeltaCompressor{}
	compressed, err := protectedTrack.GenLocmafChunk(0, 0, 1, compressor)
	require.NoError(t, err)

	decompressor := &internal.MoofDeltaDecompressor{}
	got, err := decompressLocmafObject(compressed, 0, h.cenc.ProtectedMoov[track.Name], decompressor)
	require.NoError(t, err)

	key, err := mp4.UnpackKey(keyStr)
	require.NoError(t, err)
	_, err = internal.DecryptFragment(got, h.cenc.DecryptInfo[track.Name], key)
	require.NoError(t, err)
}

// TestDecompressLocmafV02ObjectRoundTrip covers the mlmsub-side wrapper
// `decompressLocmafV02Object`. The wire payload for a v0.2 object is
// `[header_id][properties_length][properties][mdat]`; the wrapper must
// pass the entire payload to `locmafv02.Decompress` so that the receiver
// can derive the last sample's size from the mdat-payload length per
// draft §15.1 sample-size-derivation. This regressed once (the wrapper
// sliced the mdat tail off before forwarding) and produced muxed
// fragments with `sample_size = 0` that ffmpeg rejected.
func TestDecompressLocmafV02ObjectRoundTrip(t *testing.T) {
	asset, err := internal.LoadAsset(filepath.Join("..", "..", "assets", "test10s"), 1, 1)
	require.NoError(t, err)

	for _, trackName := range []string{
		"video_400kbps_avc",
		"audio_monotonic_128kbps_aac", // single-sample-per-chunk → mdat-length derivation
	} {
		t.Run(trackName, func(t *testing.T) {
			track := asset.GetTrackByName(trackName)
			require.NotNil(t, track)

			state := locmafv02.NewState()
			compressed, err := track.GenLocmafV02Chunk(0, 0, 1, state)
			require.NoError(t, err)

			rxState := locmafv02.NewState()
			got, err := decompressLocmafV02Object(compressed, track.SpecData.GetInit().Moov, rxState)
			require.NoError(t, err)
			require.NotNil(t, got)

			gotMoof, gotMdat := decodeFragment(t, got)

			expected, err := track.GenCMAFChunk(0, 0, 1)
			require.NoError(t, err)
			expectedMoof, expectedMdat := decodeFragment(t, expected)

			// The decoded moof MUST have non-zero sample sizes summing
			// to the mdat payload length — the specific regression
			// the wrapper-slice bug produced was sample_size == 0.
			require.NotEmpty(t, gotMoof.Traf.Trun.Samples)
			var sumSizes uint64
			for _, s := range gotMoof.Traf.Trun.Samples {
				require.NotZero(t, s.Size, "reconstructed sample_size must be non-zero")
				sumSizes += uint64(s.Size)
			}
			require.Equal(t, uint64(len(gotMdat.Data)), sumSizes,
				"sum of reconstructed sample sizes must equal mdat payload length")

			// Per-sample structure must match the unmodified CMAF.
			require.Equal(t, len(expectedMoof.Traf.Trun.Samples), len(gotMoof.Traf.Trun.Samples))
			for i, want := range expectedMoof.Traf.Trun.Samples {
				got := gotMoof.Traf.Trun.Samples[i]
				require.Equal(t, want.Size, got.Size, "sample %d size", i)
				require.Equal(t, want.Dur, got.Dur, "sample %d duration", i)
			}
			require.Equal(t, expectedMdat.Data, gotMdat.Data)
		})
	}
}

// TestDecompressLocmafV02ClearKeyRoundTrip exercises the full
// mlmpub→mlmsub ClearKey (ECCP) data plane for LOCMAF v0.2 in BOTH cbcs
// and cenc modes, over audio (no subsamples) and video (clear+protected
// subsamples): the publisher builds an encrypted v0.2 wire chunk, the
// subscriber runs it through `decompressLocmafV02Object`, then decrypts
// the reconstructed fragment. The decrypted payload must equal the
// plain-CMAF chunk for the corresponding clear track.
func TestDecompressLocmafV02ClearKeyRoundTrip(t *testing.T) {
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

					state := locmafv02.NewState()
					compressed, err := protectedTrack.GenLocmafV02Chunk(0, 0, 1, state)
					require.NoError(t, err)

					rxState := locmafv02.NewState()
					got, err := decompressLocmafV02Object(compressed,
						protectedTrack.SpecData.GetInit().Moov, rxState)
					require.NoError(t, err)
					require.NotNil(t, got)

					// Sanity: reconstructed sample sizes must be non-zero.
					gotMoof, gotMdat := decodeFragment(t, got)
					require.NotEmpty(t, gotMoof.Traf.Trun.Samples)
					for i, s := range gotMoof.Traf.Trun.Samples {
						require.NotZero(t, s.Size, "encrypted sample %d size must be non-zero", i)
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
