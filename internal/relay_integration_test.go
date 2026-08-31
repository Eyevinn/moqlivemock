package internal_test

import (
	"io"
	"testing"
	"testing/synctest"

	"github.com/Eyevinn/moqlivemock/internal/relay"
	"github.com/Eyevinn/moqlivemock/internal/sub"
	"github.com/Eyevinn/moqlivemock/internal/testconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRelayedVideoAudioReceive runs the same publisher toward a direct
// subscriber and toward one behind an mlmrel relay, in one synctest bubble.
// Both subscribers start at the same fake instant, so they receive the same
// groups and the relayed bytes must equal the direct ones.
func TestRelayedVideoAudioReceive(t *testing.T) {
	asset, catalog := loadTestAsset(t)

	synctest.Test(t, func(t *testing.T) {
		newMediaSub := func(video, audio io.Writer) *sub.Handler {
			return &sub.Handler{
				Namespace:   []string{testNamespace},
				Outs:        map[string]io.Writer{"video": video, "audio": audio},
				Logfh:       io.Discard,
				VideoName:   "_avc",
				AudioName:   "_aac",
				CatalogMode: "subscribe", // joining FETCH through the relay is a later phase
			}
		}

		ph := newPubHandler(asset, catalog)

		// Direct path: pub ⇄ sub.
		directServer, directClient := testconn.Pair()
		go ph.Handle(t.Context(), directServer)
		directVideo, directAudio := newSyncBuffer(), newSyncBuffer()
		go func() { _ = newMediaSub(directVideo, directAudio).RunWithConn(t.Context(), directClient) }()

		// Relayed path: pub ⇄ relay ⇄ sub, served by the same publisher
		// handler. The relay dials no one here; it handles the upstream
		// connection's client side exactly as mlmrel -upstream does.
		upServer, upClient := testconn.Pair()
		go ph.Handle(t.Context(), upServer)
		rh := relay.NewHandler(io.Discard)
		go rh.Handle(t.Context(), upClient)
		synctest.Wait() // the publisher's announcements land in the relay's table

		downServer, downClient := testconn.Pair()
		go rh.Handle(t.Context(), downServer)
		relayVideo, relayAudio := newSyncBuffer(), newSyncBuffer()
		go func() { _ = newMediaSub(relayVideo, relayAudio).RunWithConn(t.Context(), downClient) }()

		// A couple of groups' worth of media on every output.
		const videoBytes, audioBytes = 100_000, 20_000
		directVideo.WaitForLen(videoBytes)
		relayVideo.WaitForLen(videoBytes)
		directAudio.WaitForLen(audioBytes)
		relayAudio.WaitForLen(audioBytes)

		for _, c := range []*testconn.Conn{
			directServer, directClient, upServer, upClient, downServer, downClient,
		} {
			_ = c.CloseWithError(0, "")
		}
		synctest.Wait()

		// The relayed stream is byte-identical to the direct one for as long
		// as both were received; the shutdown truncates them differently.
		compare := func(name string, direct, relayed *syncBuffer) {
			d, r := direct.Bytes(), relayed.Bytes()
			n := min(len(d), len(r))
			require.Greater(t, n, 0, name)
			assert.Equal(t, d[:n], r[:n], "%s: relayed bytes differ from direct ones", name)
		}
		compare("video", directVideo, relayVideo)
		compare("audio", directAudio, relayAudio)
	})
}
