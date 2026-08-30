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
	"strconv"
	"strings"
	"testing"

	"github.com/Eyevinn/moqtransport"
	"github.com/Eyevinn/moqtransport/quicmoq"
	"github.com/quic-go/quic-go"
)

// startInteropServer starts a QUIC-based MoQ server that accepts
// interop test operations (announce, subscribe) against every draft this build
// speaks. From draft-17 the ALPN is the whole of version negotiation, so the
// set of drafts and the set of ALPNs are the same thing.
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
		PublishNamespaceHandler: moqtransport.PublishNamespaceHandlerFunc(
			func(r *moqtransport.PublishNamespaceRequest) {
				_ = r.Accept()
			}),
		SubscribeHandler: moqtransport.SubscribeHandlerFunc(
			func(r *moqtransport.SubscribeRequest) {
				// Accept the interop namespace, reject everything else.
				ns := r.Namespace()
				if len(ns) == 2 && ns[0] == "moq-test" && ns[1] == "interop" {
					_, _ = r.Accept()
					return
				}
				_ = r.Reject(moqtransport.RequestErrorDoesNotExist, "unknown namespace")
			}),
		Implementation: "Eyevinn/mlmtest interop stub",
	}
	if err := s.Run(ctx, quicmoq.NewServer(conn)); err != nil {
		return
	}
	<-ctx.Done()
	_ = s.Close(moqtransport.SessionErrorNoError, "test over")
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
		NextProtos:   moqtransport.SupportedALPNs(),
	}, nil
}

func TestInteropTestCases(t *testing.T) {
	addr, cancel := startInteropServer(t)
	defer cancel()

	for _, alpn := range moqtransport.SupportedALPNs() {
		draft, err := strconv.Atoi(strings.TrimPrefix(alpn, "moqt-"))
		if err != nil {
			t.Fatalf("cannot read a draft number out of ALPN %q: %v", alpn, err)
		}
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
