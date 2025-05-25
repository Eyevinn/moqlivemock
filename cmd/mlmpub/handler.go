package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
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

const (
	mediaPriority = 128
)

type moqHandler struct {
	addr            string
	tlsConfig       *tls.Config
	namespace       []string
	asset           *internal.Asset
	catalog         *internal.Catalog
	logfh           io.Writer
	fingerprintPort int
}

func (h *moqHandler) runServer(ctx context.Context) error {
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

func (h *moqHandler) startFingerprintServer() {
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

func (h *moqHandler) getCertificateFingerprint() string {
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

func (h *moqHandler) validateCertificateForWebTransport() error {
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

func serveQUICConn(wt *webtransport.Server, conn quic.Connection) {
	err := wt.ServeQUICConn(conn)
	if err != nil {
		slog.Error("failed to serve QUIC connection", "error", err)
	}
}

func (h *moqHandler) getHandler() moqtransport.Handler {
	return moqtransport.HandlerFunc(func(w moqtransport.ResponseWriter, r *moqtransport.Message) {
		switch r.Method {
		case moqtransport.MessageAnnounce:
			slog.Warn("got unexpected announcement", "namespace", r.Namespace)
			err := w.Reject(0, fmt.Sprintf("%s doesn't take announcements", appName))
			if err != nil {
				slog.Error("failed to reject announcement", "error", err)
			}
			return
		case moqtransport.MessageSubscribe:
			if !tupleEqual(r.Namespace, h.namespace) {
				slog.Warn("got unexpected subscription namespace",
					"received", r.Namespace,
					"expected", h.namespace)
				err := w.Reject(0, fmt.Sprintf("%s doesn't take subscriptions", appName))
				if err != nil {
					slog.Error("failed to reject subscription", "error", err)
				}
				return
			}
			if r.Track == "catalog" {
				err := w.Accept()
				if err != nil {
					slog.Error("failed to accept subscription", "error", err)
					return
				}
				publisher, ok := w.(moqtransport.Publisher)
				if !ok {
					slog.Error("subscription response writer does not implement publisher")
				}
				sg, err := publisher.OpenSubgroup(0, 0, 0)
				if err != nil {
					slog.Error("failed to open subgroup", "error", err)
					return
				}
				json, err := json.Marshal(h.catalog)
				if err != nil {
					slog.Error("failed to marshal catalog", "error", err)
					return
				}
				_, err = sg.WriteObject(0, json)
				if err != nil {
					slog.Error("failed to write catalog", "error", err)
					return
				}
				err = sg.Close()
				if err != nil {
					slog.Error("failed to close subgroup", "error", err)
					return
				}
				return
			}
			for _, track := range h.catalog.Tracks {
				if r.Track == track.Name {
					err := w.Accept()
					if err != nil {
						slog.Error("failed to accept subscription", "error", err)
						return
					}
					publisher, ok := w.(moqtransport.Publisher)
					if !ok {
						slog.Error("subscription response writer does not implement publisher")
					}
					slog.Info("got subscription", "track", track.Name)
					go publishTrack(context.TODO(), publisher, h.asset, track.Name)
					return
				}
			}
			// If we get here, the track was not found
			err := w.Reject(moqtransport.ErrorCodeSubscribeTrackDoesNotExist, "unknown track")
			if err != nil {
				slog.Error("failed to reject subscription", "error", err)
			}
			// case moqtransport.MessageUnsubscribe:
			// Need to handle unsubscribe
			// For nice switching, it should be possible to unsubscrit to one video track
			// and subscribe to another, and the publisher should go on until the end
			// of the current MoQ group, and start the new track on the next MoQ group.
			// This is not currently implemented.
		}
	})
}

func publishTrack(ctx context.Context, publisher moqtransport.Publisher, asset *internal.Asset, trackName string) {
	ct := asset.GetTrackByName(trackName)
	if ct == nil {
		slog.Error("track not found", "track", trackName)
		return
	}
	now := time.Now().UnixMilli()
	currGroupNr := internal.CurrMoQGroupNr(ct, uint64(now), internal.MoqGroupDurMS)
	groupNr := currGroupNr + 1 // Start stream on next group
	slog.Info("publishing track", "track", trackName, "group", groupNr)
	for {
		if ctx.Err() != nil {
			return
		}
		sg, err := publisher.OpenSubgroup(groupNr, 0, mediaPriority)
		if err != nil {
			slog.Error("failed to open subgroup", "error", err)
			return
		}
		mg := internal.GenMoQGroup(ct, groupNr, ct.SampleBatch, internal.MoqGroupDurMS)
		slog.Info("writing MoQ group", "track", ct.Name, "group", groupNr, "objects", len(mg.MoQObjects))
		err = internal.WriteMoQGroup(ctx, ct, mg, sg.WriteObject)
		if err != nil {
			slog.Error("failed to write MoQ group", "error", err)
			return
		}
		err = sg.Close()
		if err != nil {
			slog.Error("failed to close subgroup", "error", err)
			return
		}
		slog.Debug("published MoQ group", "track", ct.Name, "group", groupNr, "objects", len(mg.MoQObjects))
		groupNr++
	}
}

func (h *moqHandler) handle(conn moqtransport.Connection) {
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
