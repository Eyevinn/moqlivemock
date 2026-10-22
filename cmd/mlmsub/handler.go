package main

import (
	"context"
	"io"
	"strings"

	"github.com/Eyevinn/moqlivemock/internal"
	"github.com/Eyevinn/moqlivemock/internal/sub"
)

// relayURL reads -addr: a moqt:// or https:// URL, or a bare host[:port],
// which is short for moqt://host[:port].
func relayURL(addr string) (internal.RelayURL, error) {
	if !strings.Contains(addr, "://") {
		addr = "moqt://" + addr
	}
	return internal.ParseRelayURL(addr)
}

// runClientWithDial connects to the relay and runs the subscriber on the
// connection, offering alpn as the only MOQT version. It does not verify the
// server certificate: the peers are test servers on self-signed certificates.
func runClientWithDial(
	ctx context.Context, relay internal.RelayURL, alpn string, h *sub.Handler, outs map[string]io.Writer,
) error {
	conn, err := relay.Dial(ctx, internal.DialConfig{SkipVerify: true, ALPNs: []string{alpn}})
	if err != nil {
		return err
	}
	h.Outs = outs
	h.Path = relay.Path()
	h.Authority = relay.Authority()
	return h.RunWithConn(ctx, conn)
}
