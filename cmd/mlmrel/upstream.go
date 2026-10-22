package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/Eyevinn/moqlivemock/internal"
	"github.com/Eyevinn/moqlivemock/internal/relay"
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
//
// Like mlmsub, it does not verify the server certificate: the peers are test
// servers on self-signed certificates.
func runUpstream(ctx context.Context, h *relay.Handler, upstream internal.RelayURL) {
	for {
		conn, err := upstream.Dial(ctx, internal.DialConfig{SkipVerify: true, KeepAlive: upstreamKeepAlive})
		if err != nil {
			slog.Error("failed to dial upstream", "url", upstream, "error", err)
		} else {
			slog.Info("connected to upstream", "url", upstream)
			// HandleUpstream, not Handle: the relay has to ask the upstream
			// for its namespaces rather than wait to be told, or a conformant
			// upstream -- one that answers only what it is asked -- leaves it
			// with an empty table and nothing to route.
			h.HandleUpstream(ctx, conn) // blocks until the session ends
			slog.Warn("upstream session ended", "url", upstream)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(upstreamRedialDelay):
		}
	}
}
