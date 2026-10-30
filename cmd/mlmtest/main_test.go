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
	"sync/atomic"
	"testing"
	"time"

	"github.com/Eyevinn/moqlivemock/internal"
	"github.com/Eyevinn/moqtransport"
	"github.com/quic-go/quic-go"
)

// startInteropServer starts a MoQ server that accepts the interop test
// operations (announce, subscribe) over both native QUIC and WebTransport on
// one port, the way internal.RunMoQServer serves mlmpub and mlmrel. It serves
// every draft this build speaks: from draft-17 the ALPN is the whole of version
// negotiation, so the set of drafts and the set of ALPNs are the same thing.
// It returns the server's address and the stub, which counts what it saw.
func startInteropServer(t *testing.T) (string, *interopStub) {
	t.Helper()
	tlsConfig, err := generateTLSConfig()
	if err != nil {
		t.Fatal(err)
	}
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := pc.LocalAddr().String()
	if err := pc.Close(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	stub := &interopStub{}
	go func() { _ = internal.RunMoQServer(ctx, addr, tlsConfig, stub) }()
	waitForServer(t, addr)
	return addr, stub
}

// waitForServer dials until the server answers, so that no test case races
// its startup.
func waitForServer(t *testing.T, addr string) {
	t.Helper()
	tlsConfig := &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"h3"}}
	deadline := time.Now().Add(5 * time.Second)
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		conn, err := quic.DialAddr(ctx, addr, tlsConfig, nil)
		cancel()
		if err == nil {
			_ = conn.CloseWithError(0, "")
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("server at %s did not start: %v", addr, err)
		}
	}
}

// interopStub is the relay side of the interop test cases. It counts the
// announcements it accepts, and those withdrawn while their session was still
// up rather than ended by the session closing.
type interopStub struct {
	announced atomic.Int64
	withdrawn atomic.Int64
}

func (st *interopStub) Handle(ctx context.Context, conn moqtransport.Connection) {
	var s *moqtransport.Session
	s = &moqtransport.Session{
		PublishNamespaceHandler: moqtransport.PublishNamespaceHandlerFunc(
			func(r *moqtransport.PublishNamespaceRequest) {
				if r.Accept() != nil {
					return
				}
				st.announced.Add(1)
				<-r.Context().Done()
				if s.Context().Err() == nil {
					st.withdrawn.Add(1)
				}
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
	if err := s.Run(ctx, conn); err != nil {
		return
	}
	select {
	case <-ctx.Done():
	case <-s.Context().Done():
	}
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
		NextProtos:   append(moqtransport.SupportedALPNs(), "h3"),
	}, nil
}

// TestInteropTestCases runs every test case over both transports, picked by
// the relay URL's scheme. The moqt:// URL has a path, so its sessions carry
// the PATH and AUTHORITY setup options.
func TestInteropTestCases(t *testing.T) {
	addr, _ := startInteropServer(t)
	relays := []struct{ transport, url string }{
		{"quic", "moqt://" + addr + "/anon"},
		{"webtransport", "https://" + addr + "/moq"},
	}

	for _, relay := range relays {
		for _, alpn := range moqtransport.SupportedALPNs() {
			for _, tc := range testCases {
				t.Run(fmt.Sprintf("%s/%s/%s", relay.transport, alpn, tc.name), func(t *testing.T) {
					ctx, ctxCancel := context.WithTimeout(context.Background(), defaultTimeout)
					defer ctxCancel()
					if err := tc.fn(ctx, relay.url, true, []string{alpn}); err != nil {
						t.Fatalf("%s (%s over %s): %v", tc.name, alpn, relay.transport, err)
					}
				})
			}
		}
	}
}

// TestPublishersWithdraw: every test case that announces withdraws the
// namespace before it closes the session, so a relay that is slow to notice a
// closed session no longer holds the namespace when the next case announces
// it again.
func TestPublishersWithdraw(t *testing.T) {
	addr, stub := startInteropServer(t)
	relay := "moqt://" + addr
	for _, tc := range testCases {
		ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
		if err := tc.fn(ctx, relay, true, nil); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		cancel()
	}
	waitFor(t, func() bool { return stub.withdrawn.Load() == stub.announced.Load() })
	if n := stub.announced.Load(); n != 4 {
		t.Fatalf("expected 4 announcements, one per announcing test case, got %d", n)
	}
}

// waitFor polls cond until it holds, failing the test after a second.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met within a second")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
