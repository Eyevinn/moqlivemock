package internal

import (
	"context"
	"crypto/tls"
	"net"
	"testing"
	"time"

	"github.com/Eyevinn/moqtransport"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"github.com/stretchr/testify/require"
)

// idleHandler holds each session open until the server stops.
type idleHandler struct{}

func (idleHandler) Handle(ctx context.Context, _ moqtransport.Connection) { <-ctx.Done() }

// TestWebTransportAdvertisesStreamLimits checks that the WebTransport server
// sends initial flow-control limits in its HTTP/3 SETTINGS. Safari enforces
// WebTransport flow control and reads a missing limit as zero, so without them
// it can open no stream at all, not even the one that carries the MoQ SETUP.
func TestWebTransportAdvertisesStreamLimits(t *testing.T) {
	tlsConfig, err := generateSelfSignedTLSConfig()
	require.NoError(t, err)
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := pc.LocalAddr().String()
	require.NoError(t, pc.Close())

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() { _ = RunMoQServer(ctx, addr, tlsConfig, idleHandler{}) }()

	conn := dialH3(t, ctx, addr)
	defer func() { _ = conn.CloseWithError(0, "") }()
	cc := (&http3.Transport{}).NewClientConn(conn)
	select {
	case <-cc.ReceivedSettings():
	case <-time.After(5 * time.Second):
		t.Fatal("no HTTP/3 SETTINGS from the server")
	}
	other := cc.Settings().Other
	for name, id := range map[string]uint64{
		"SETTINGS_WT_INITIAL_MAX_STREAMS_UNI":  0x2b64,
		"SETTINGS_WT_INITIAL_MAX_STREAMS_BIDI": 0x2b65,
		"SETTINGS_WT_INITIAL_MAX_DATA":         0x2b61,
	} {
		require.NotZero(t, other[id], "%s must be advertised", name)
	}
}

// dialH3 dials the server with the h3 ALPN, retrying until it listens.
func dialH3(t *testing.T, ctx context.Context, addr string) *quic.Conn {
	t.Helper()
	tlsConf := &tls.Config{InsecureSkipVerify: true, NextProtos: []string{http3.NextProtoH3}}
	quicConf := &quic.Config{EnableDatagrams: true, EnableStreamResetPartialDelivery: true}
	deadline := time.Now().Add(5 * time.Second)
	for {
		dialCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		conn, err := quic.DialAddr(dialCtx, addr, tlsConf, quicConf)
		cancel()
		if err == nil {
			return conn
		}
		if time.Now().After(deadline) {
			t.Fatalf("dialing %s: %v", addr, err)
		}
	}
}
