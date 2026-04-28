package main

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/url"

	"github.com/Eyevinn/moqlivemock/internal/sub"
	"github.com/Eyevinn/moqtransport"
	"github.com/Eyevinn/moqtransport/quicmoq"
	"github.com/Eyevinn/moqtransport/webtransportmoq"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/webtransport-go"
)

func runClientWithDial(
	ctx context.Context, addr string, useWebTransport bool, alpn string, h *sub.Handler, outs map[string]io.Writer,
) error {
	var conn moqtransport.Connection
	var err error
	if useWebTransport {
		conn, err = dialWebTransport(ctx, addr, alpn)
	} else {
		conn, err = dialQUIC(ctx, addr, alpn)
	}
	if err != nil {
		return err
	}
	h.Outs = outs
	return h.RunWithConn(ctx, conn)
}

func ensurePort(addr, defaultPort string) string {
	if _, _, err := net.SplitHostPort(addr); err != nil {
		return net.JoinHostPort(addr, defaultPort)
	}
	return addr
}

func ensureURLPort(rawURL, defaultPort string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	if u.Port() == "" {
		u.Host = net.JoinHostPort(u.Hostname(), defaultPort)
		return u.String()
	}
	return rawURL
}

func dialQUIC(ctx context.Context, addr string, alpn string) (moqtransport.Connection, error) {
	addr = ensurePort(addr, "443")
	conn, err := quic.DialAddr(ctx, addr, &tls.Config{
		InsecureSkipVerify: true,
		NextProtos:         []string{alpn},
	}, &quic.Config{
		EnableDatagrams:                  true,
		EnableStreamResetPartialDelivery: true,
	})
	if err != nil {
		return nil, err
	}
	return quicmoq.NewClient(conn), nil
}

// settingWebTransportMaxSessions is HTTP/3 SETTINGS_WEBTRANSPORT_MAX_SESSIONS
// (draft-ietf-webtrans-http3). Advertising it on the client side is technically
// a server-only setting per the spec, but several deployed relays built on
// web-transport-quinn (Cloudflare's WT endpoint, moq-rs / cdn.moq.dev, …) call
// the same supports_webtransport() check on the client's SETTINGS frame and
// close the connection with H3_NO_ERROR when it is missing. Sending it makes
// the WT handshake succeed against those relays at no cost on conformant ones.
const settingWebTransportMaxSessions = 0xc671706a

func dialWebTransport(ctx context.Context, addr string, alpn string) (moqtransport.Connection, error) {
	dialer := webtransport.Dialer{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true,
		},
		QUICConfig: &quic.Config{
			EnableDatagrams:                  true,
			EnableStreamResetPartialDelivery: true,
		},
		ApplicationProtocols: []string{alpn},
		AdditionalSettings: map[uint64]uint64{
			settingWebTransportMaxSessions: 1,
		},
	}
	_, session, err := dialer.Dial(ctx, ensureURLPort(addr, "443"), nil)
	if err != nil {
		return nil, err
	}
	return webtransportmoq.NewClient(session), nil
}
