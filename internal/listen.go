package internal

import (
	"context"
	"crypto/tls"
	"log/slog"
	"net/http"
	"slices"

	"github.com/Eyevinn/moqtransport"
	"github.com/Eyevinn/moqtransport/quicmoq"
	"github.com/Eyevinn/moqtransport/webtransportmoq"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/webtransport-go"
)

// SessionHandler handles one MoQ connection. Handle should block until the
// session ends; RunMoQServer runs it on its own goroutine per connection.
type SessionHandler interface {
	Handle(ctx context.Context, conn moqtransport.Connection)
}

// RunMoQServer listens on addr and serves MoQ sessions over both raw QUIC
// (ALPN from moqtransport.SupportedALPNs()) and WebTransport (ALPN h3,
// endpoint /moq) on the same port. It blocks until the listener fails or ctx
// is cancelled, so tlsConfig.NextProtos must carry both ALPN sets.
func RunMoQServer(ctx context.Context, addr string, tlsConfig *tls.Config, handler SessionHandler) error {
	slog.Info("Starting MoQ server", "addr", addr)
	listener, err := quic.ListenAddr(addr, tlsConfig, &quic.Config{
		EnableDatagrams:                  true,
		EnableStreamResetPartialDelivery: true,
	})
	if err != nil {
		return err
	}
	defer func() {
		_ = listener.Close()
	}()
	h3Server := &http3.Server{
		Addr:      addr,
		TLSConfig: tlsConfig,
		QUICConfig: &quic.Config{
			EnableDatagrams:                  true,
			EnableStreamResetPartialDelivery: true,
		},
	}
	// ConfigureHTTP3Server (webtransport-go v0.11.0) sends the full set of
	// WebTransport SETTINGS, including the WT_MAX_SESSIONS and flow-control
	// codepoints Safari 26.4+ requires. See https://github.com/Eyevinn/warp-player/issues/88
	webtransport.ConfigureHTTP3Server(h3Server)
	wt := webtransport.Server{
		H3: h3Server,
		CheckOrigin: func(r *http.Request) bool {
			return true
		},
		ApplicationProtocols: moqtransport.SupportedALPNs(),
	}
	// A dedicated mux rather than http.DefaultServeMux, so that starting a
	// second server in one process (as tests may) cannot panic on a duplicate
	// pattern registration.
	mux := http.NewServeMux()
	mux.HandleFunc("/moq", func(w http.ResponseWriter, r *http.Request) {
		session, err := wt.Upgrade(w, r)
		if err != nil {
			slog.Error("upgrading to webtransport failed", "error", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		handler.Handle(ctx, webtransportmoq.NewServer(session))
	})
	h3Server.Handler = mux
	for {
		conn, err := listener.Accept(ctx)
		if err != nil {
			return err
		}
		alpn := conn.ConnectionState().TLS.NegotiatedProtocol
		switch {
		case alpn == "h3":
			go serveQUICConn(&wt, conn)
		case slices.Contains(moqtransport.SupportedALPNs(), alpn):
			go handler.Handle(ctx, quicmoq.NewServer(conn))
		default:
			slog.Warn("unknown ALPN, closing connection", "alpn", alpn)
			_ = conn.CloseWithError(0, "unsupported protocol")
		}
	}
}

func serveQUICConn(wt *webtransport.Server, conn *quic.Conn) {
	err := wt.ServeQUICConn(conn)
	if err != nil {
		slog.Error("failed to serve QUIC connection", "error", err)
	}
}
