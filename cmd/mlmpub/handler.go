package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/Eyevinn/moqlivemock/internal"
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
	// eccp is the ClearKey/ECCP config the content is encrypted with, and the
	// source of the key served by /clearkey. Nil when -kid/-iv are not set, in
	// which case /clearkey has no key to hand out.
	eccp *internal.DRMInfo
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
	// ConfigureHTTP3Server (webtransport-go v0.11.0) sends the full set of
	// WebTransport SETTINGS, including the WT_MAX_SESSIONS and flow-control
	// codepoints Safari 26.4+ requires. See https://github.com/Eyevinn/warp-player/issues/88
	webtransport.ConfigureHTTP3Server(h3Server)
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

	mux.HandleFunc("/clearkey", withCORS(s.serveClearKey))

	addr := fmt.Sprintf(":%d", s.sidePort)
	slog.Info("Starting HTTP side server", "addr", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		slog.Error("side server failed", "error", err)
	}
}

// serveClearKey answers an EME ClearKey license request (a POST of
// {"kids":[...]}) with the content key each requested KID is actually encrypted
// with, per https://www.w3.org/TR/encrypted-media/#clear-key-request-format.
//
// The KIDs arrive base64url-encoded and the response's k/kid must be base64url
// without padding. A requested KID that this publisher has no key for is
// omitted; if that leaves no keys at all the request gets 404 rather than an
// empty key list, so a misconfigured run fails loudly instead of handing the
// player a license it cannot use.
func (s *server) serveClearKey(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Kids []string `json:"kids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
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

	keys := make([]keyInfo, 0, len(req.Kids))
	for _, kidB64 := range req.Kids {
		kid, err := decodeBase64URL(kidB64)
		if err != nil {
			slog.Warn("ClearKey request has an undecodable kid", "kid", kidB64, "error", err)
			continue
		}
		key, ok := s.eccp.ContentKeyForKID(kid)
		if !ok {
			slog.Warn("ClearKey request for an unknown kid", "kid", kidB64)
			continue
		}
		keys = append(keys, keyInfo{
			Kty: "oct",
			K:   base64.RawURLEncoding.EncodeToString(key),
			Kid: base64.RawURLEncoding.EncodeToString(kid),
		})
	}
	if len(keys) == 0 {
		http.Error(w, "no key for the requested kids", http.StatusNotFound)
		slog.Warn("Served no ClearKey license", "requestedKids", req.Kids)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(clearKeyResponse{Keys: keys, Type: "temporary"}); err != nil {
		slog.Error("failed to encode ClearKey response", "error", err)
		return
	}
	slog.Info("Served ClearKey license", "keys", len(keys))
}

// decodeBase64URL decodes a base64url KID, tolerating the "=" padding that the
// spec forbids but some clients still send.
func decodeBase64URL(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
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
