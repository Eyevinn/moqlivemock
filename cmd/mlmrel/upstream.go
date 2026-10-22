package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/Eyevinn/moqlivemock/internal"
	"github.com/Eyevinn/moqlivemock/internal/relay"
	"github.com/Eyevinn/moqtransport"
)

const upstreamRedialDelay = 2 * time.Second

// upstreamKeepAlive keeps the upstream connection warm. A relay's upstream
// carries no traffic whenever nobody is subscribed, and an idle QUIC
// connection dies at the peer's idle timeout -- 30s against mlmpub. That took
// every upstream announcement with it after the first quiet spell, so the
// relay stood there with an empty namespace table until something restarted
// it. Redialling recovers from a genuine loss; this stops the quiet ones.
const upstreamKeepAlive = 10 * time.Second

// runUpstream keeps a session to the static upstream publisher alive. The
// upstream's announcements land in the relay's table like anyone else's, so
// routing needs no special case for it. Redials with a delay until ctx ends.
func runUpstream(ctx context.Context, h *relay.Handler, rawURL string) {
	for {
		conn, err := dialUpstream(ctx, rawURL)
		if err != nil {
			slog.Error("failed to dial upstream", "url", rawURL, "error", err)
		} else {
			slog.Info("connected to upstream", "url", rawURL)
			// HandleUpstream, not Handle: the relay has to ask the upstream
			// for its namespaces rather than wait to be told, or a conformant
			// upstream -- one that answers only what it is asked -- leaves it
			// with an empty table and nothing to route.
			h.HandleUpstream(ctx, conn) // blocks until the session ends
			slog.Warn("upstream session ended", "url", rawURL)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(upstreamRedialDelay):
		}
	}
}

// dialUpstream connects to moqt://host[:port] over raw QUIC or to
// https://host[:port][/path] over WebTransport. The default port is 443.
// Like mlmsub, it does not verify the server certificate: the peers are test
// servers on self-signed certificates. A moqt:// path is not sent as a PATH
// setup option: relay.Handler builds the upstream session, and mlmpub serves
// the same namespaces whatever the path.
func dialUpstream(ctx context.Context, rawURL string) (moqtransport.Connection, error) {
	upstream, err := internal.ParseRelayURL(rawURL)
	if err != nil {
		return nil, err
	}
	return upstream.Dial(ctx, internal.DialConfig{SkipVerify: true, KeepAlive: upstreamKeepAlive})
}
