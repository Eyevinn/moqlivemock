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
	"github.com/Eyevinn/mp4ff/bits"
	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/stretchr/testify/require"
)

func TestDecompressLocmafObjectRoundTrip(t *testing.T) {
	asset, err := internal.LoadAsset(filepath.Join("..", "..", "assets", "test10s"), 1, 1)
	require.NoError(t, err)

	track := asset.GetTrackByName("video_400kbps_avc")
	require.NotNil(t, track)

	var compressor internal.MoofDeltaCompressor
	compressed, err := track.GenLocmafChunk(0, 0, 1, compressor)
	require.NoError(t, err)

	expected, err := track.GenCMAFChunk(0, 0, 1)
	require.NoError(t, err)

	var decompressor internal.MoofDeltaDecompressor
	got, err := decompressLocmafObject(compressed, 0, track.SpecData.GetInit().Moov, &decompressor)
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

	var compressor internal.MoofDeltaCompressor
	compressed, err := protectedTrack.GenLocmafChunk(0, 0, 1, compressor)
	require.NoError(t, err)

	var decompressor internal.MoofDeltaDecompressor
	got, err := decompressLocmafObject(compressed, 0, protectedTrack.SpecData.GetInit().Moov, &decompressor)
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
	require.NoError(t, h.decryptInit(track))

	protectedTrack := asset.GetTrackByName(track.Name)
	require.NotNil(t, protectedTrack)
	var compressor internal.MoofDeltaCompressor
	compressed, err := protectedTrack.GenLocmafChunk(0, 0, 1, compressor)
	require.NoError(t, err)

	var decompressor internal.MoofDeltaDecompressor
	got, err := decompressLocmafObject(compressed, 0, h.cenc.ProtectedMoov[track.Name], &decompressor)
	require.NoError(t, err)

	key, err := mp4.UnpackKey(keyStr)
	require.NoError(t, err)
	_, err = internal.DecryptFragment(got, h.cenc.DecryptInfo[track.Name], key)
	require.NoError(t, err)
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
