package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRelayURL(t *testing.T) {
	for _, tc := range []struct {
		addr, url, path, authority string
	}{
		// A bare address is native QUIC, as -addr has always meant.
		{"localhost:4443", "moqt://localhost:4443", "", "localhost:4443"},
		{"[::1]:4443", "moqt://[::1]:4443", "", "[::1]:4443"},
		{"moqt://cdn.moq.dev/anon", "moqt://cdn.moq.dev/anon", "/anon", "cdn.moq.dev"},
		{"https://localhost:4443/moq", "https://localhost:4443/moq", "", ""},
	} {
		t.Run(tc.addr, func(t *testing.T) {
			relay, err := relayURL(tc.addr)
			require.NoError(t, err)
			assert.Equal(t, tc.url, relay.String())
			assert.Equal(t, tc.path, relay.Path())
			assert.Equal(t, tc.authority, relay.Authority())
		})
	}
	_, err := relayURL("http://localhost:4443")
	assert.Error(t, err)
}
