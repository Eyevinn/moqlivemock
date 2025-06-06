package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/Eyevinn/moqlivemock/internal"
	"github.com/mengelbart/moqtransport"
	"github.com/mengelbart/moqtransport/quicmoq"
	"github.com/mengelbart/moqtransport/webtransportmoq"
	"github.com/mengelbart/qlog"
	"github.com/mengelbart/qlog/moqt"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/webtransport-go"
)

// moqHandlerNew is the new handler that uses the TrackPublisher architecture
type moqHandlerNew struct {
	addr            string
	tlsConfig       *tls.Config
	namespace       []string
	asset           *internal.Asset
	catalog         *internal.Catalog
	publisherMgr    *internal.PublisherManager
	logfh           io.Writer
	fingerprintPort int
}

// newMoqHandler creates a new handler with the PublisherManager architecture
func newMoqHandler(addr string, tlsConfig *tls.Config, namespace []string, asset *internal.Asset, catalog *internal.Catalog, logfh io.Writer, fingerprintPort int) *moqHandlerNew {
	publisherMgr := internal.NewPublisherManager(asset, catalog)
	
	return &moqHandlerNew{
		addr:            addr,
		tlsConfig:       tlsConfig,
		namespace:       namespace,
		asset:           asset,
		catalog:         catalog,
		publisherMgr:    publisherMgr,
		logfh:           logfh,
		fingerprintPort: fingerprintPort,
	}
}

func (h *moqHandlerNew) runServer(ctx context.Context) error {
	// Start the publisher manager
	err := h.publisherMgr.Start(ctx)
	if err != nil {
		return fmt.Errorf("failed to start publisher manager: %w", err)
	}
	defer h.publisherMgr.Stop()

	// Start HTTP server for fingerprint if port is specified
	if h.fingerprintPort > 0 {
		go h.startFingerprintServer()
	}

	listener, err := quic.ListenAddr(h.addr, h.tlsConfig, &quic.Config{
		EnableDatagrams: true,
	})
	if err != nil {
		return err
	}
	wt := webtransport.Server{
		H3: http3.Server{
			Addr:      h.addr,
			TLSConfig: h.tlsConfig,
		},
		CheckOrigin: func(r *http.Request) bool {
			return true
		},
	}
	http.HandleFunc("/moq", func(w http.ResponseWriter, r *http.Request) {
		session, err := wt.Upgrade(w, r)
		if err != nil {
			slog.Error("upgrading to webtransport failed", "error", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		h.handle(webtransportmoq.NewServer(session))
	})
	for {
		conn, err := listener.Accept(ctx)
		if err != nil {
			return err
		}
		if conn.ConnectionState().TLS.NegotiatedProtocol == "h3" {
			go serveQUICConn(&wt, conn)
		}
		if conn.ConnectionState().TLS.NegotiatedProtocol == "moq-00" {
			go h.handle(quicmoq.NewServer(conn))
		}
	}
}

func (h *moqHandlerNew) startFingerprintServer() {
	// Validate certificate for WebTransport requirements
	if err := h.validateCertificateForWebTransport(); err != nil {
		slog.Warn("Certificate does not meet WebTransport fingerprint requirements", "error", err)
		slog.Warn("Fingerprint server may not work properly with WebTransport")
	}

	fingerprint := h.getCertificateFingerprint()
	if fingerprint == "" {
		slog.Error("failed to get certificate fingerprint")
		return
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/fingerprint", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "*")
		
		// Handle preflight OPTIONS request
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}
		
		fmt.Fprint(w, fingerprint)
		slog.Debug("Served fingerprint", "fingerprint", fingerprint)
	})

	addr := fmt.Sprintf(":%d", h.fingerprintPort)
	slog.Info("Starting fingerprint HTTP server", "addr", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		slog.Error("fingerprint server failed", "error", err)
	}
}

func (h *moqHandlerNew) getCertificateFingerprint() string {
	if len(h.tlsConfig.Certificates) == 0 {
		return ""
	}

	cert := h.tlsConfig.Certificates[0]
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

func (h *moqHandlerNew) validateCertificateForWebTransport() error {
	if len(h.tlsConfig.Certificates) == 0 {
		return fmt.Errorf("no certificates found")
	}

	cert := h.tlsConfig.Certificates[0]
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

func (h *moqHandlerNew) getHandler() moqtransport.Handler {
	return moqtransport.HandlerFunc(func(w moqtransport.ResponseWriter, r moqtransport.Message) {
		switch r.Method() {
		case moqtransport.MessageAnnounce:
			am, ok := r.(*moqtransport.AnnounceMessage)
			if !ok {
				slog.Error("failed to type assert AnnounceMessage")
				return
			}
			slog.Warn("got unexpected announcement", "namespace", am.Namespace)
			err := w.Reject(0, fmt.Sprintf("%s doesn't take announcements", appName))
			if err != nil {
				slog.Error("failed to reject announcement", "error", err)
			}
			return
		case moqtransport.MessageSubscribe:
			sm, ok := r.(*moqtransport.SubscribeMessage)
			if !ok {
				slog.Error("failed to type assert SubscribeMessage")
				return
			}
			if !tupleEqual(sm.Namespace, h.namespace) {
				slog.Warn("got unexpected subscription namespace",
					"received", sm.Namespace,
					"expected", h.namespace)
				err := w.Reject(0, fmt.Sprintf("%s doesn't take subscriptions", appName))
				if err != nil {
					slog.Error("failed to reject subscription", "error", err)
				}
				return
			}

			// Cast to SubscribeResponseWriter to use the new publisher manager
			subscribeWriter, ok := w.(moqtransport.SubscribeResponseWriter)
			if !ok {
				slog.Error("response writer is not a SubscribeResponseWriter")
				err := w.Reject(moqtransport.ErrorCodeInternal, "internal error")
				if err != nil {
					slog.Error("failed to reject subscription", "error", err)
				}
				return
			}

			// Handle subscription using publisher manager
			err := h.publisherMgr.HandleSubscribe(sm, subscribeWriter)
			if err != nil {
				slog.Error("failed to handle subscription", "track", sm.Track, "error", err)
				var errorCode uint64 = moqtransport.ErrorCodeInternal
				if err.Error() == "track not found: "+sm.Track {
					errorCode = moqtransport.ErrorCodeSubscribeTrackDoesNotExist
				}
				rejErr := subscribeWriter.Reject(errorCode, err.Error())
				if rejErr != nil {
					slog.Error("failed to reject subscription", "error", rejErr)
				}
				return
			}

			slog.Info("handled subscription", "track", sm.Track)
		}
	})
}

func (h *moqHandlerNew) handle(conn moqtransport.Connection) {
	session := moqtransport.NewSession(conn.Protocol(), conn.Perspective(), 100)
	transport := &moqtransport.Transport{
		Conn:    conn,
		Handler: h.getHandler(),
		Qlogger: qlog.NewQLOGHandler(h.logfh, "MoQ QLOG", "MoQ QLOG", conn.Perspective().String(), moqt.Schema),
		Session: session,
	}
	err := transport.Run()
	if err != nil {
		slog.Error("MoQ Session initialization failed", "error", err)
		err = conn.CloseWithError(0, "session initialization error")
		if err != nil {
			slog.Error("failed to close connection", "error", err)
		}
		return
	}
	if err := session.Announce(context.Background(), h.namespace); err != nil {
		slog.Error("failed to announce namespace", "namespace", h.namespace, "error", err)
		return
	}
}