package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"testing"

	"github.com/Eyevinn/moqtransport"
	"github.com/Eyevinn/moqtransport/quicmoq"
	"github.com/quic-go/quic-go"
)

// startInteropServer starts a QUIC-based MoQ server that accepts
// interop test operations (announce, subscribe) on both draft-14 and draft-16 ALPNs.
// It returns the listener address and a cancel function.
func startInteropServer(t *testing.T) (addr string, cancel func()) {
	t.Helper()
	tlsConfig, err := generateTLSConfig()
	if err != nil {
		t.Fatal(err)
	}
	listener, err := quic.ListenAddr("localhost:0", tlsConfig, &quic.Config{
		EnableDatagrams: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, ctxCancel := context.WithCancel(context.Background())

	go func() {
		for {
			conn, err := listener.Accept(ctx)
			if err != nil {
				return
			}
			go serveInteropConn(ctx, conn)
		}
	}()

	port := listener.Addr().(*net.UDPAddr).Port
	return fmt.Sprintf("moqt://localhost:%d", port), func() {
		ctxCancel()
		_ = listener.Close()
	}
}

func serveInteropConn(ctx context.Context, conn *quic.Conn) {
	s := &moqtransport.Session{
		InitialMaxRequestID: 64,
		Handler: moqtransport.HandlerFunc(func(w moqtransport.ResponseWriter, r *moqtransport.Message) {
			if r.Method == moqtransport.MessageAnnounce {
				_ = w.Accept()
			}
		}),
		SubscribeHandler: moqtransport.SubscribeHandlerFunc(
			func(w *moqtransport.SubscribeResponseWriter, m *moqtransport.SubscribeMessage) {
				// Accept interop namespace, reject everything else
				if len(m.Namespace) == 2 && m.Namespace[0] == "moq-test" && m.Namespace[1] == "interop" {
					_ = w.Accept()
					return
				}
				_ = w.Reject(0, "unknown namespace")
			}),
	}
	if err := s.Run(quicmoq.NewServer(conn)); err != nil {
		return
	}
	<-ctx.Done()
	s.Close()
}

func generateTLSConfig() (*tls.Config, error) {
	key, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		return nil, err
	}
	template := x509.Certificate{SerialNumber: big.NewInt(1)}
	certDER, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		return nil, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	return &tls.Config{
		Certificates: []tls.Certificate{tlsCert},
		NextProtos:   []string{"moqt-16", "moq-00"},
	}, nil
}

func TestInteropTestCases(t *testing.T) {
	addr, cancel := startInteropServer(t)
	defer cancel()

	for _, draft := range []int{14, 16} {
		for _, tc := range testCases {
			t.Run(fmt.Sprintf("draft%d/%s", draft, tc.name), func(t *testing.T) {
				ctx, ctxCancel := context.WithTimeout(context.Background(), defaultTimeout)
				defer ctxCancel()
				if err := tc.fn(ctx, addr, true, draft); err != nil {
					t.Fatalf("%s (draft-%d): %v", tc.name, draft, err)
				}
			})
		}
	}
}
