package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"time"

	"github.com/Eyevinn/moqlivemock/internal/relay"
	"github.com/Eyevinn/moqtransport"
	"github.com/Eyevinn/moqtransport/quicmoq"
	"github.com/Eyevinn/moqtransport/webtransportmoq"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/webtransport-go"
)

const upstreamRedialDelay = 2 * time.Second

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
			h.Handle(ctx, conn) // blocks until the session ends
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
// servers on self-signed certificates.
func dialUpstream(ctx context.Context, rawURL string) (moqtransport.Connection, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("parse upstream URL: %w", err)
	}
	switch u.Scheme {
	case "moqt":
		addr := u.Host
		if u.Port() == "" {
			addr = net.JoinHostPort(u.Hostname(), "443")
		}
		conn, err := quic.DialAddr(ctx, addr, &tls.Config{
			InsecureSkipVerify: true,
			NextProtos:         moqtransport.SupportedALPNs(),
		}, &quic.Config{
			EnableDatagrams:                  true,
			EnableStreamResetPartialDelivery: true,
		})
		if err != nil {
			return nil, err
		}
		return quicmoq.NewClient(conn), nil
	case "https":
		if u.Port() == "" {
			u.Host = net.JoinHostPort(u.Hostname(), "443")
		}
		dialer := webtransport.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
			},
			QUICConfig: &quic.Config{
				EnableDatagrams:                  true,
				EnableStreamResetPartialDelivery: true,
			},
			// quinn doesn't implement the QUIC RESET_STREAM_AT extension that
			// draft-ietf-webtrans-http3-16 requires, so don't insist the peer
			// has it.
			AllowPeerWithoutPartialDelivery: true,
			ApplicationProtocols:            moqtransport.SupportedALPNs(),
		}
		_, session, err := dialer.Dial(ctx, u.String(), nil)
		if err != nil {
			return nil, err
		}
		return webtransportmoq.NewClient(session), nil
	default:
		return nil, fmt.Errorf("upstream URL scheme must be moqt:// or https://, got %q", u.Scheme)
	}
}
