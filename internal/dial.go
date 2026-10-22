package internal

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/url"
	"time"

	"github.com/Eyevinn/moqtransport"
	"github.com/Eyevinn/moqtransport/quicmoq"
	"github.com/Eyevinn/moqtransport/webtransportmoq"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/webtransport-go"
)

// RelayURL is the address of a MoQ server or relay. The scheme picks the
// transport: moqt://host[:port][/path][?query] is native QUIC and
// https://host[:port][/path] is WebTransport. The port defaults to 443.
type RelayURL struct {
	u *url.URL
}

// ParseRelayURL parses a moqt:// or https:// relay URL.
func ParseRelayURL(raw string) (RelayURL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return RelayURL{}, fmt.Errorf("parse relay URL: %w", err)
	}
	if u.Scheme != "moqt" && u.Scheme != "https" {
		return RelayURL{}, fmt.Errorf("relay URL scheme must be moqt:// or https://, got %q", raw)
	}
	if u.Hostname() == "" {
		return RelayURL{}, fmt.Errorf("relay URL %q has no host", raw)
	}
	return RelayURL{u: u}, nil
}

func (r RelayURL) String() string { return r.u.String() }

// Authority is the value of the AUTHORITY setup option. draft-ietf-moq-transport-18
// Section 3.1.4 has a native QUIC client send the URI's authority in SETUP;
// over WebTransport the CONNECT request carries it and the option must not be
// sent, so it is empty.
func (r RelayURL) Authority() string {
	if r.u.Scheme != "moqt" {
		return ""
	}
	return r.u.Host
}

// Path is the value of the PATH setup option: the path-abempty portion of a
// moqt:// URI, followed by ? and the query when there is one
// (draft-ietf-moq-transport-18 Section 10.3.1.2). It is empty over
// WebTransport, where the path is in the CONNECT request.
func (r RelayURL) Path() string {
	if r.u.Scheme != "moqt" {
		return ""
	}
	path := r.u.EscapedPath()
	if r.u.RawQuery != "" || r.u.ForceQuery {
		path += "?" + r.u.RawQuery
	}
	return path
}

// DialConfig tunes Dial.
type DialConfig struct {
	// SkipVerify accepts any server certificate, for test servers on
	// self-signed ones.
	SkipVerify bool
	// KeepAlive, when non-zero, keeps an idle connection open by sending a
	// keep-alive this often.
	KeepAlive time.Duration
	// ALPNs are the MOQT versions offered, as the QUIC ALPN or the
	// WebTransport subprotocol. Nil offers every version this build speaks.
	ALPNs []string
}

// Dial opens a connection to the relay over the transport the URL's scheme
// names. The PATH and AUTHORITY setup options are the caller's to put on the
// moqtransport.Session it runs on the connection.
func (r RelayURL) Dial(ctx context.Context, cfg DialConfig) (moqtransport.Connection, error) {
	alpns := cfg.ALPNs
	if alpns == nil {
		alpns = moqtransport.SupportedALPNs()
	}
	tlsConfig := &tls.Config{InsecureSkipVerify: cfg.SkipVerify}
	quicConfig := &quic.Config{
		EnableDatagrams:                  true,
		EnableStreamResetPartialDelivery: true,
		KeepAlivePeriod:                  cfg.KeepAlive,
	}
	u := *r.u
	if u.Port() == "" {
		u.Host = net.JoinHostPort(u.Hostname(), "443")
	}
	if u.Scheme == "moqt" {
		tlsConfig.NextProtos = alpns
		conn, err := quic.DialAddr(ctx, u.Host, tlsConfig, quicConfig)
		if err != nil {
			return nil, err
		}
		return quicmoq.NewClient(conn), nil
	}
	// The Eyevinn webtransport-go fork also sends and accepts the legacy
	// WEBTRANSPORT_MAX_SESSIONS codepoint, which the deployed
	// web-transport-quinn relays need.
	dialer := webtransport.Transport{
		TLSClientConfig: tlsConfig,
		QUICConfig:      quicConfig,
		// quinn doesn't implement the QUIC RESET_STREAM_AT extension that
		// draft-ietf-webtrans-http3-16 requires, so don't insist the peer has it.
		AllowPeerWithoutPartialDelivery: true,
		// Every deployed WebTransport relay we have met, the web-transport-quinn based
		// ones (moq-rs, cdn.moq.dev, Cloudflare) and moxygen alike, still rejects the
		// draft-15 "webtransport-h3" :protocol token, so send the legacy "webtransport".
		LegacyConnectProtocol: true,
		ApplicationProtocols:  alpns,
	}
	_, session, err := dialer.Dial(ctx, u.String(), nil)
	if err != nil {
		return nil, err
	}
	return webtransportmoq.NewClient(session), nil
}
