package internal

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRelayURLSetupOptions(t *testing.T) {
	for _, tc := range []struct {
		url       string
		authority string
		path      string
	}{
		{"moqt://cdn.moq.dev:443/anon", "cdn.moq.dev:443", "/anon"},
		{"moqt://relay.example/moq?jwt=abc", "relay.example", "/moq?jwt=abc"},
		{"moqt://relay.example/a%20b", "relay.example", "/a%20b"},
		{"moqt://relay.example", "relay.example", ""},
		{"moqt://[::1]:4443/", "[::1]:4443", "/"},
		// WebTransport carries both in the CONNECT request, never in SETUP.
		{"https://cdn.moq.dev/anon", "", ""},
	} {
		t.Run(tc.url, func(t *testing.T) {
			r, err := ParseRelayURL(tc.url)
			require.NoError(t, err)
			assert.Equal(t, tc.authority, r.Authority())
			assert.Equal(t, tc.path, r.Path())
		})
	}
}

func TestParseRelayURLRejects(t *testing.T) {
	for _, raw := range []string{
		"relay.example:443", // no scheme
		"http://relay.example",
		"quic://relay.example",
		"moqt:///anon", // no host
	} {
		_, err := ParseRelayURL(raw)
		assert.Error(t, err, raw)
	}
}

func TestDraftALPNs(t *testing.T) {
	for _, c := range []struct {
		draft string
		want  []string
	}{
		{"", nil},
		{"22", []string{"moqt-22"}},
		{"draft-22", []string{"moqt-22"}},
		{"18", []string{"moqt-18"}},
		{"draft-18", []string{"moqt-18"}},
	} {
		got, err := DraftALPNs(c.draft)
		require.NoError(t, err, c.draft)
		assert.Equal(t, c.want, got, c.draft)
	}
	for _, draft := range []string{"16", "draft-20", "moqt-22", "x"} {
		_, err := DraftALPNs(draft)
		assert.Error(t, err, draft)
	}
}
