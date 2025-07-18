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
	"sync/atomic"
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
	logger          *slog.Logger
	addr            string
	tlsConfig       *tls.Config
	namespace       []string
	asset           *internal.Asset
	catalog         *internal.Catalog
	publisherMgr    *internal.PublisherManager
	logfh           io.Writer
	fingerprintPort int
	nextSessionID   atomic.Uint64
}

// newMoqHandler creates a new handler with the PublisherManager architecture
func newMoqHandler(
	logger *slog.Logger,
	addr string,
	tlsConfig *tls.Config,
	namespace []string,
	asset *internal.Asset,
	catalog *internal.Catalog,
	logfh io.Writer,
	fingerprintPort int,
) *moqHandlerNew {
	publisherMgr := internal.NewPublisherManager(logger, asset, catalog)

	return &moqHandlerNew{
		logger:          logger,
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
	defer func() {
		if err := h.publisherMgr.Stop(); err != nil {
			h.logger.Error("failed to stop publisher manager", "error", err)
		}
	}()

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
			h.logger.Error("upgrading to webtransport failed", "error", err)
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
			go serveQUICConn(h.logger, &wt, conn)
		}
		if conn.ConnectionState().TLS.NegotiatedProtocol == "moq-00" {
			go h.handle(quicmoq.NewServer(conn))
		}
	}
}

func (h *moqHandlerNew) startFingerprintServer() {
	// Validate certificate for WebTransport requirements
	if err := h.validateCertificateForWebTransport(); err != nil {
		h.logger.Warn("Certificate does not meet WebTransport fingerprint requirements", "error", err)
		h.logger.Warn("Fingerprint server may not work properly with WebTransport")
	}

	fingerprint := h.getCertificateFingerprint()
	if fingerprint == "" {
		h.logger.Error("failed to get certificate fingerprint")
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
		h.logger.Debug("Served fingerprint", "fingerprint", fingerprint)
	})

	addr := fmt.Sprintf(":%d", h.fingerprintPort)
	h.logger.Info("Starting fingerprint HTTP server", "addr", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		h.logger.Error("fingerprint server failed", "error", err)
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
		h.logger.Error("failed to parse certificate", "error", err)
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

	h.logger.Info("Certificate meets WebTransport fingerprint requirements",
		"algorithm", x509Cert.PublicKeyAlgorithm.String(),
		"validity_days", validityDuration.Hours()/24,
		"self_signed", true)

	return nil
}

func (h *moqHandlerNew) getHandler(sessionID uint64) moqtransport.Handler {
	return moqtransport.HandlerFunc(func(w moqtransport.ResponseWriter, r *moqtransport.Message) {
		switch r.Method {
		case moqtransport.MessageAnnounce:
			h.logger.Warn("got unexpected announcement", "sessionID", sessionID, "namespace", r.Namespace)
			err := w.Reject(0, fmt.Sprintf("%s doesn't take announcements", appName))
			if err != nil {
				h.logger.Error("failed to reject announcement", "sessionID", sessionID, "error", err)
			}
			return
		}
	})
}

func (h *moqHandlerNew) getSubscribeHandler(sessionID uint64) moqtransport.SubscribeHandler {
	return moqtransport.SubscribeHandlerFunc(func(
		w *moqtransport.SubscribeResponseWriter,
		m *moqtransport.SubscribeMessage,
	) {
		if !tupleEqual(m.Namespace, h.namespace) {
			h.logger.Warn("got unexpected subscription namespace",
				"sessionID", sessionID,
				"received", m.Namespace,
				"expected", h.namespace)
			err := w.Reject(0, fmt.Sprintf("%s doesn't take subscriptions", appName))
			if err != nil {
				h.logger.Error("failed to reject subscription", "sessionID", sessionID, "error", err)
			}
			return
		}

		subID := internal.SubscriptionID{
			SessionID: sessionID,
			RequestID: m.RequestID,
		}

		h.logger.Info("received subscribe message",
			"sessionID", sessionID,
			"subscriptionID", subID.String(),
			"track", m.Track,
			"namespace", m.Namespace,
			"filterType", m.FilterType,
			"subscriberPriority", m.SubscriberPriority)

		// Handle subscription using publisher manager
		err := h.publisherMgr.HandleSubscribe(m, w, sessionID)
		if err != nil {
			h.logger.Error("failed to handle subscription", "sessionID", sessionID, "track", m.Track, "error", err)
			errorCode := moqtransport.ErrorCodeInternal
			if err.Error() == "track not found: "+m.Track {
				errorCode = moqtransport.ErrorCodeSubscribeTrackDoesNotExist
			}
			rejErr := w.Reject(errorCode, err.Error())
			if rejErr != nil {
				h.logger.Error("failed to reject subscription", "sessionID", sessionID, "error", rejErr)
			}
			return
		}
	})
}

func (h *moqHandlerNew) getSubscribeUpdateHandler(sessionID uint64) moqtransport.SubscribeUpdateHandler {
	return moqtransport.SubscribeUpdateHandlerFunc(func(m *moqtransport.SubscribeUpdateMessage) {
		subID := internal.SubscriptionID{
			SessionID: sessionID,
			RequestID: m.RequestID,
		}
		h.logger.Info("received subscribe update message",
			"sessionID", sessionID,
			"subscriptionID", subID.String(),
			"endGroup", m.EndGroup,
			"subscriberPriority", m.SubscriberPriority)

		// Handle subscription update using publisher manager
		err := h.publisherMgr.HandleSubscribeUpdate(m, sessionID)
		if err != nil {
			h.logger.Error("failed to handle subscription update",
				"sessionID", sessionID,
				"subscriptionID", subID.String(),
				"error", err)
			return
		}
	})
}

func (h *moqHandlerNew) handle(conn moqtransport.Connection) {
	id := h.nextSessionID.Add(1)
	session := &moqtransport.Session{
		Handler:                h.getHandler(id),
		SubscribeHandler:       h.getSubscribeHandler(id),
		SubscribeUpdateHandler: h.getSubscribeUpdateHandler(id),
		InitialMaxRequestID:    100,
		Qlogger: qlog.NewQLOGHandler(
			h.logfh, "MoQ QLOG", "MoQ QLOG",
			conn.Perspective().String(), moqt.Schema,
		),
	}
	if err := session.Run(conn); err != nil {
		h.logger.Error("MoQ Session initialization failed", "sessionID", id, "error", err)
		err = conn.CloseWithError(0, "session initialization error")
		if err != nil {
			h.logger.Error("failed to close connection", "sessionID", id, "error", err)
		}
		return
	}
	if err := session.Announce(context.Background(), h.namespace); err != nil {
		h.logger.Error("failed to announce namespace", "sessionID", id, "namespace", h.namespace, "error", err)
		return
	}
}

// serveQUICConn serves HTTP/3 connections
func serveQUICConn(logger *slog.Logger, wt *webtransport.Server, conn quic.Connection) {
	err := wt.ServeQUICConn(conn)
	if err != nil {
		logger.Error("failed to serve QUIC connection", "error", err)
	}
}

// tupleEqual compares two string slices for equality
func tupleEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i, t := range a {
		if t != b[i] {
			return false
		}
	}
	return true
}
