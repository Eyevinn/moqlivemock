package main

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Eyevinn/moqlivemock/internal"
)

const (
	testKID = "11223344556677889900aabbccddeeff"
	testKey = "ffeeddccbbaa00998877665544332211"
	testIV  = "0123456789abcdef0123456789abcdef"
)

type clearKeyResp struct {
	Keys []struct {
		Kty string `json:"kty"`
		K   string `json:"k"`
		Kid string `json:"kid"`
	} `json:"keys"`
	Type string `json:"type"`
}

// b64 is the base64url-without-padding form of a hex-encoded KID or key, which
// is how both travel in an EME ClearKey exchange.
func b64(t *testing.T, hexStr string) string {
	t.Helper()
	raw, err := hex.DecodeString(hexStr)
	require.NoError(t, err)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func testServer(t *testing.T, kidStr, keyStr string) *server {
	t.Helper()
	eccp, err := internal.ParseCENCflags("cbcs", kidStr, keyStr, testIV, "http://localhost:8081/clearkey")
	require.NoError(t, err)
	require.NotNil(t, eccp)
	return &server{eccp: eccp}
}

func postClearKey(t *testing.T, s *server, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/clearkey", strings.NewReader(body))
	rec := httptest.NewRecorder()
	s.serveClearKey(rec, req)
	return rec
}

// TestServeClearKeyServesConfiguredKey is the regression test for #122: the
// endpoint used to echo the requested KID back as the content key, so any run
// with -cenckey set served a license that could not decrypt the content.
func TestServeClearKeyServesConfiguredKey(t *testing.T) {
	s := testServer(t, testKID, testKey)

	rec := postClearKey(t, s, `{"kids":["`+b64(t, testKID)+`"],"type":"temporary"}`)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp clearKeyResp
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp.Keys, 1)
	require.Equal(t, "oct", resp.Keys[0].Kty)
	require.Equal(t, b64(t, testKID), resp.Keys[0].Kid)
	require.Equal(t, b64(t, testKey), resp.Keys[0].K, "k must be the configured -cenckey")
	require.NotEqual(t, resp.Keys[0].Kid, resp.Keys[0].K, "k must not be the KID echoed back")
	require.Equal(t, "temporary", resp.Type)
}

// TestServeClearKeyKIDIsKeyWhenNoCencKey covers the -cenckey-omitted case, where
// ParseCENCflags defaults the key to the KID: k and kid then legitimately match,
// which is why the bug went unnoticed.
func TestServeClearKeyKIDIsKeyWhenNoCencKey(t *testing.T) {
	s := testServer(t, testKID, "")

	rec := postClearKey(t, s, `{"kids":["`+b64(t, testKID)+`"]}`)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp clearKeyResp
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp.Keys, 1)
	require.Equal(t, b64(t, testKID), resp.Keys[0].K)
}

// TestServeClearKeyPaddedKID accepts the "=" padding the spec forbids but some
// clients send anyway, and normalizes it away in the response.
func TestServeClearKeyPaddedKID(t *testing.T) {
	s := testServer(t, testKID, testKey)
	padded := base64.URLEncoding.EncodeToString(mustHex(t, testKID))
	require.Contains(t, padded, "=", "the 16-byte KID must pad, or this test proves nothing")

	rec := postClearKey(t, s, `{"kids":["`+padded+`"]}`)
	require.Equal(t, http.StatusOK, rec.Code)

	var resp clearKeyResp
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp.Keys, 1)
	require.Equal(t, b64(t, testKID), resp.Keys[0].Kid, "response kid must be unpadded")
	require.Equal(t, b64(t, testKey), resp.Keys[0].K)
}

// TestServeClearKeyUnknownKID checks an unknown KID is a 404 rather than an
// echo: serving a key for content this publisher did not encrypt is exactly the
// failure #122 describes.
func TestServeClearKeyUnknownKID(t *testing.T) {
	s := testServer(t, testKID, testKey)

	other := b64(t, "aabbccddeeff00112233445566778899")
	rec := postClearKey(t, s, `{"kids":["`+other+`"]}`)
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.NotContains(t, rec.Body.String(), other, "an unknown kid must not be echoed back as a key")
}

// TestServeClearKeyNoECCP covers -sideport without -kid/-iv: there is no key to
// serve, and a nil *DRMInfo must not panic.
func TestServeClearKeyNoECCP(t *testing.T) {
	s := &server{}
	rec := postClearKey(t, s, `{"kids":["`+b64(t, testKID)+`"]}`)
	require.Equal(t, http.StatusNotFound, rec.Code)
}

func TestServeClearKeyBadRequests(t *testing.T) {
	s := testServer(t, testKID, testKey)

	rec := postClearKey(t, s, `not json`)
	require.Equal(t, http.StatusBadRequest, rec.Code)

	req := httptest.NewRequest(http.MethodGet, "/clearkey", nil)
	rec = httptest.NewRecorder()
	s.serveClearKey(rec, req)
	require.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	raw, err := hex.DecodeString(s)
	require.NoError(t, err)
	return raw
}
