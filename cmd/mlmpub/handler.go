package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/Eyevinn/moqlivemock/internal/pub"
	"github.com/Eyevinn/moqtransport/quicmoq"
	"github.com/Eyevinn/moqtransport/webtransportmoq"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/webtransport-go"
)

type server struct {
	addr      string
	tlsConfig *tls.Config
	handler   *pub.Handler
	sidePort  int
}

func (s *server) runServer(ctx context.Context) error {
	// Start HTTP side server for /fingerprint and /clearkey
	if s.sidePort > 0 {
		go s.startSideServer()
	}

	slog.Info("Starting MoQ server", "addr", s.addr)
	listener, err := quic.ListenAddr(s.addr, s.tlsConfig, &quic.Config{
		EnableDatagrams:                  true,
		EnableStreamResetPartialDelivery: true,
	})
	if err != nil {
		return err
	}
	h3Server := &http3.Server{
		Addr:      s.addr,
		TLSConfig: s.tlsConfig,
		QUICConfig: &quic.Config{
			EnableDatagrams:                  true,
			EnableStreamResetPartialDelivery: true,
		},
	}
	webtransport.ConfigureHTTP3Server(h3Server)
	// Add newer WebTransport setting codepoints for Safari 26.4+ compatibility.
	// webtransport-go only sends the old draft-02 setting (0x2b603742).
	// Safari requires the draft-13/14 or draft-15 codepoint to recognize WebTransport support.
	// See https://github.com/Eyevinn/warp-player/issues/88
	h3Server.AdditionalSettings[0x14e9cd29] = 1 // SETTINGS_WT_MAX_SESSIONS (draft-13/14)
	h3Server.AdditionalSettings[0x2c7cf000] = 1 // SETTINGS_WT_ENABLED (draft-15)
	wt := webtransport.Server{
		H3: h3Server,
		CheckOrigin: func(r *http.Request) bool {
			return true
		},
		ApplicationProtocols: []string{"moqt-16", "moq-00"},
	}
	http.HandleFunc("/moq", func(w http.ResponseWriter, r *http.Request) {
		session, err := wt.Upgrade(w, r)
		if err != nil {
			slog.Error("upgrading to webtransport failed", "error", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		s.handler.Handle(ctx, webtransportmoq.NewServer(session))
	})
	for {
		conn, err := listener.Accept(ctx)
		if err != nil {
			return err
		}
		alpn := conn.ConnectionState().TLS.NegotiatedProtocol
		switch alpn {
		case "h3":
			go serveQUICConn(&wt, conn)
		case "moq-00", "moqt-16":
			go s.handler.Handle(ctx, quicmoq.NewServer(conn))
		default:
			slog.Warn("unknown ALPN, closing connection", "alpn", alpn)
			_ = conn.CloseWithError(0, "unsupported protocol")
		}
	}
}

func (s *server) startSideServer() {
	// Validate certificate for WebTransport requirements
	if err := s.validateCertificateForWebTransport(); err != nil {
		slog.Warn("Certificate does not meet WebTransport fingerprint requirements", "error", err)
		slog.Warn("Fingerprint server may not work properly with WebTransport")
	}

	fingerprint := s.getCertificateFingerprint()
	if fingerprint == "" {
		slog.Error("failed to get certificate fingerprint")
		return
	}

	mux := http.NewServeMux()

	// Middleware to handle CORS and OPTIONS preflight
	withCORS := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "*")

			if r.Method == "OPTIONS" {
				w.WriteHeader(http.StatusOK)
				return
			}
			next(w, r)
		}
	}

	mux.HandleFunc("/fingerprint", withCORS(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, fingerprint)
		slog.Debug("Served fingerprint", "fingerprint", fingerprint)
	}))

	mux.HandleFunc("/clearkey", withCORS(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req struct {
			Kids []string `json:"kids"`
		}
		err := json.NewDecoder(r.Body).Decode(&req)
		if err != nil {
			http.Error(w, "failed to decode request body", http.StatusBadRequest)
			return
		}

		type keyInfo struct {
			Kty string `json:"kty"`
			K   string `json:"k"`
			Kid string `json:"kid"`
		}
		type clearKeyResponse struct {
			Keys []keyInfo `json:"keys"`
			Type string    `json:"type"`
		}

		var keys []keyInfo
		for _, kid := range req.Kids {
			keys = append(keys, keyInfo{
				Kty: "oct",
				K:   kid,
				Kid: kid,
			})
		}

		response := clearKeyResponse{
			Keys: keys,
			Type: "temporary",
		}
		w.Header().Set("Content-Type", "application/json")
		err = json.NewEncoder(w).Encode(response)
		if err != nil {
			slog.Error("failed to encode ClearKey response", "error", err)
			return
		}
		slog.Info("Served ClearKey license")
	}))

	addr := fmt.Sprintf(":%d", s.sidePort)
	slog.Info("Starting HTTP side server", "addr", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		slog.Error("side server failed", "error", err)
	}
}

func (s *server) getCertificateFingerprint() string {
	if len(s.tlsConfig.Certificates) == 0 {
		return ""
	}

	cert := s.tlsConfig.Certificates[0]
	if len(cert.Certificate) == 0 {
		return ""
	}

	// Parse the certificate
	x509Cert, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		slog.Error("failed to parse certificate", "error", err)
		return ""
	}

	// Calculate SHA-256 fingerprint
	fingerprint := sha256.Sum256(x509Cert.Raw)
	return hex.EncodeToString(fingerprint[:])
}

func (s *server) validateCertificateForWebTransport() error {
	if len(s.tlsConfig.Certificates) == 0 {
		return fmt.Errorf("no certificates found")
	}

	cert := s.tlsConfig.Certificates[0]
	if len(cert.Certificate) == 0 {
		return fmt.Errorf("certificate is empty")
	}

	// Parse the certificate
	x509Cert, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return fmt.Errorf("failed to parse certificate: %w", err)
	}

	// Check 1: Must be self-signed (issuer == subject)
	if x509Cert.Issuer.String() != x509Cert.Subject.String() {
		return fmt.Errorf("certificate is not self-signed (issuer: %s, subject: %s)",
			x509Cert.Issuer.String(), x509Cert.Subject.String())
	}

	// Check 2: Must use ECDSA algorithm
	if x509Cert.PublicKeyAlgorithm != x509.ECDSA {
		return fmt.Errorf("certificate must use ECDSA algorithm, but uses %s",
			x509Cert.PublicKeyAlgorithm.String())
	}

	// Check 3: Must be valid for 14 days or less
	validityDuration := x509Cert.NotAfter.Sub(x509Cert.NotBefore)
	maxDuration := 14 * 24 * time.Hour
	if validityDuration > maxDuration {
		validityDays := validityDuration.Hours() / 24
		return fmt.Errorf("certificate validity exceeds 14 days (valid for %.1f days)", validityDays)
	}

	slog.Info("Certificate meets WebTransport fingerprint requirements",
		"algorithm", x509Cert.PublicKeyAlgorithm.String(),
		"validity_days", validityDuration.Hours()/24,
		"self_signed", true)

	return nil
}

func serveQUICConn(wt *webtransport.Server, conn *quic.Conn) {
	err := wt.ServeQUICConn(conn)
	if err != nil {
		slog.Error("failed to serve QUIC connection", "error", err)
	}
}
