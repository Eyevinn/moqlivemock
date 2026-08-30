// mlmtest is an interop test client for the MoQ interop runner.
// It implements the 6 test cases defined in moq-interop-runner and
// outputs TAP v14 results on stdout.
package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Eyevinn/moqlivemock/internal"
	"github.com/Eyevinn/moqtransport"
	"github.com/Eyevinn/moqtransport/quicmoq"
	"github.com/quic-go/quic-go"
)

const defaultTimeout = 5 * time.Second

type testCase struct {
	name string
	fn   func(ctx context.Context, relay string, tlsSkipVerify bool, draft int) error
}

var testCases = []testCase{
	{"setup-only", testSetupOnly},
	{"announce-only", testAnnounceOnly},
	{"publish-namespace-done", testPublishNamespaceDone},
	{"subscribe-error", testSubscribeError},
	{"announce-subscribe", testAnnounceSubscribe},
	{"subscribe-before-announce", testSubscribeBeforeAnnounce},
}

func main() {
	relay := flag.String("r", "", "Relay URL (moqt:// for QUIC)")
	test := flag.String("t", "", "Specific test to run (default: all)")
	list := flag.Bool("l", false, "List available tests")
	tlsSkipVerify := flag.Bool("tls-disable-verify", false, "Skip TLS verification")
	draft := flag.Int("draft", 18, "MoQ Transport draft version (18)")
	flag.Parse()

	// Environment variable overrides
	if env := os.Getenv("RELAY_URL"); env != "" && *relay == "" {
		*relay = env
	}
	if env := os.Getenv("TESTCASE"); env != "" && *test == "" {
		*test = env
	}
	if os.Getenv("TLS_DISABLE_VERIFY") == "1" {
		*tlsSkipVerify = true
	}
	if env := os.Getenv("DRAFT"); env != "" {
		if d, err := strconv.Atoi(env); err == nil {
			*draft = d
		}
	}
	if *draft != 18 {
		fmt.Fprintf(os.Stderr, "Error: draft must be 18, got %d\n", *draft)
		os.Exit(1)
	}

	if *list {
		for _, tc := range testCases {
			fmt.Println(tc.name)
		}
		os.Exit(0)
	}

	if *relay == "" {
		fmt.Fprintf(os.Stderr, "Error: relay URL required (-r or RELAY_URL env)\n")
		os.Exit(1)
	}

	// Select tests to run
	cases := testCases
	if *test != "" {
		cases = nil
		for _, tc := range testCases {
			if tc.name == *test {
				cases = append(cases, tc)
				break
			}
		}
		if len(cases) == 0 {
			fmt.Fprintf(os.Stderr, "Unknown test case: %s\n", *test)
			os.Exit(127)
		}
	}

	// From draft-17 the ALPN is the whole of version negotiation, and the
	// library knows which ones it speaks.
	alpn := moqtransport.SupportedALPNs()[0]

	// TAP v14 output
	fmt.Println("TAP version 14")
	fmt.Printf("# moqlivemock %s\n", internal.GetVersion())
	fmt.Printf("# Relay: %s\n", *relay)
	fmt.Printf("# Draft: %d (ALPN: %s)\n", *draft, alpn)
	fmt.Printf("1..%d\n", len(cases))

	failed := 0
	for i, tc := range cases {
		ctx, cancel := context.WithTimeout(context.Background(), defaultTimeout)
		err := tc.fn(ctx, *relay, *tlsSkipVerify, *draft)
		cancel()

		num := i + 1
		if err != nil {
			fmt.Printf("not ok %d - %s\n", num, tc.name)
			fmt.Printf("  ---\n  error: %v\n  ...\n", err)
			failed++
		} else {
			fmt.Printf("ok %d - %s\n", num, tc.name)
		}
	}

	if failed > 0 {
		os.Exit(1)
	}
}

// dial connects to the relay using raw QUIC with the appropriate ALPN for the draft.
func dial(ctx context.Context, relay string, tlsSkipVerify bool, draft int) (moqtransport.Connection, error) {
	u, err := url.Parse(relay)
	if err != nil {
		return nil, fmt.Errorf("parse relay URL: %w", err)
	}

	addr := u.Host
	if u.Port() == "" {
		addr = net.JoinHostPort(u.Hostname(), "443")
	}

	alpn := moqtransport.SupportedALPNs()[0]

	conn, err := quic.DialAddr(ctx, addr, &tls.Config{
		InsecureSkipVerify: tlsSkipVerify,
		NextProtos:         []string{alpn},
	}, &quic.Config{
		EnableDatagrams:                  true,
		EnableStreamResetPartialDelivery: true,
	})
	if err != nil {
		return nil, fmt.Errorf("QUIC dial: %w", err)
	}
	return quicmoq.NewClient(conn), nil
}

// runSession creates a session with a no-op handler, runs it, and returns it.
func runSession(ctx context.Context, conn moqtransport.Connection, draft int) (*moqtransport.Session, error) {
	s := &moqtransport.Session{
		// Accept announcements from the relay.
		PublishNamespaceHandler: moqtransport.PublishNamespaceHandlerFunc(
			func(r *moqtransport.PublishNamespaceRequest) {
				_ = r.Accept()
			}),
		Implementation: "Eyevinn/mlmtest",
	}
	if err := s.Run(ctx, conn); err != nil {
		return nil, fmt.Errorf("session setup: %w", err)
	}
	return s, nil
}

// testSetupOnly: connect, exchange SETUP, close.
func testSetupOnly(ctx context.Context, relay string, tlsSkipVerify bool, draft int) error {
	conn, err := dial(ctx, relay, tlsSkipVerify, draft)
	if err != nil {
		return err
	}
	s, err := runSession(ctx, conn, draft)
	if err != nil {
		return err
	}
	closeSession(s)
	return nil
}

// testAnnounceOnly: setup, PUBLISH_NAMESPACE, wait for REQUEST_OK, close.
func testAnnounceOnly(ctx context.Context, relay string, tlsSkipVerify bool, draft int) error {
	conn, err := dial(ctx, relay, tlsSkipVerify, draft)
	if err != nil {
		return err
	}
	s, err := runSession(ctx, conn, draft)
	if err != nil {
		return err
	}
	if _, err := s.PublishNamespace(ctx, []string{"moq-test", "interop"}); err != nil {
		return fmt.Errorf("PUBLISH_NAMESPACE: %w", err)
	}
	closeSession(s)
	return nil
}

// testPublishNamespaceDone: setup, PUBLISH_NAMESPACE, REQUEST_OK, PUBLISH_NAMESPACE_DONE, close.
func testPublishNamespaceDone(ctx context.Context, relay string, tlsSkipVerify bool, draft int) error {
	conn, err := dial(ctx, relay, tlsSkipVerify, draft)
	if err != nil {
		return err
	}
	s, err := runSession(ctx, conn, draft)
	if err != nil {
		return err
	}
	publication, err := s.PublishNamespace(ctx, []string{"moq-test", "interop"})
	if err != nil {
		return fmt.Errorf("PUBLISH_NAMESPACE: %w", err)
	}
	// Closing the announcement's stream is what draft-18 uses in place of
	// UNANNOUNCE.
	if err := publication.Close(); err != nil {
		return fmt.Errorf("PUBLISH_NAMESPACE_DONE: %w", err)
	}
	closeSession(s)
	return nil
}

// testSubscribeError: setup, SUBSCRIBE non-existent track, expect REQUEST_ERROR.
func testSubscribeError(ctx context.Context, relay string, tlsSkipVerify bool, draft int) error {
	conn, err := dial(ctx, relay, tlsSkipVerify, draft)
	if err != nil {
		return err
	}
	s, err := runSession(ctx, conn, draft)
	if err != nil {
		return err
	}
	_, subErr := s.Subscribe(ctx, []string{"nonexistent", "namespace"}, "test-track")
	if subErr == nil {
		closeSession(s)
		return fmt.Errorf("expected REQUEST_ERROR but got SUBSCRIBE_OK")
	}
	// Any error (subscribe error, timeout) is acceptable — the relay rejected the subscribe
	if strings.Contains(subErr.Error(), "context deadline exceeded") {
		closeSession(s)
		return fmt.Errorf("timed out waiting for REQUEST_ERROR")
	}
	closeSession(s)
	return nil
}

// runPublisherSession creates a session that announces a namespace and accepts
// subscriptions. The announcement lasts as long as the returned handle is
// open: closing it is draft-18's PUBLISH_NAMESPACE_DONE.
func runPublisherSession(
	ctx context.Context, relay string, tlsSkipVerify bool, draft int, namespace []string,
) (*moqtransport.Session, *moqtransport.NamespacePublication, error) {
	conn, err := dial(ctx, relay, tlsSkipVerify, draft)
	if err != nil {
		return nil, nil, err
	}
	s := &moqtransport.Session{
		PublishNamespaceHandler: moqtransport.PublishNamespaceHandlerFunc(
			func(r *moqtransport.PublishNamespaceRequest) {
				_ = r.Accept()
			}),
		SubscribeHandler: moqtransport.SubscribeHandlerFunc(
			func(r *moqtransport.SubscribeRequest) {
				_, _ = r.Accept()
			}),
		Implementation: "Eyevinn/mlmtest",
	}
	if err := s.Run(ctx, conn); err != nil {
		return nil, nil, fmt.Errorf("publisher session setup: %w", err)
	}
	publication, err := s.PublishNamespace(ctx, namespace)
	if err != nil {
		closeSession(s)
		return nil, nil, fmt.Errorf("PUBLISH_NAMESPACE: %w", err)
	}
	return s, publication, nil
}

// closeSession ends a session cleanly. Every close in these tests is a normal
// end of a test case, not a failure.
func closeSession(s *moqtransport.Session) {
	_ = s.Close(moqtransport.SessionErrorNoError, "test complete")
}

// testAnnounceSubscribe: publisher announces, subscriber subscribes, both succeed.
func testAnnounceSubscribe(ctx context.Context, relay string, tlsSkipVerify bool, draft int) error {
	ns := []string{"moq-test", "interop"}

	// Publisher: connect, setup, announce
	pub, _, err := runPublisherSession(ctx, relay, tlsSkipVerify, draft, ns)
	if err != nil {
		return fmt.Errorf("publisher: %w", err)
	}
	defer closeSession(pub)

	// Subscriber: connect, setup, subscribe
	conn, err := dial(ctx, relay, tlsSkipVerify, draft)
	if err != nil {
		return fmt.Errorf("subscriber dial: %w", err)
	}
	sub, err := runSession(ctx, conn, draft)
	if err != nil {
		return fmt.Errorf("subscriber session: %w", err)
	}
	defer closeSession(sub)

	_, subErr := sub.Subscribe(ctx, ns, "test-track")
	if subErr != nil {
		return fmt.Errorf("SUBSCRIBE: %w", subErr)
	}
	return nil
}

// testSubscribeBeforeAnnounce: subscriber subscribes first, publisher 500ms later.
// Either SUBSCRIBE_OK or REQUEST_ERROR is valid.
func testSubscribeBeforeAnnounce(ctx context.Context, relay string, tlsSkipVerify bool, draft int) error {
	ns := []string{"moq-test", "interop"}

	// Subscriber: connect first
	subConn, err := dial(ctx, relay, tlsSkipVerify, draft)
	if err != nil {
		return fmt.Errorf("subscriber dial: %w", err)
	}
	sub, err := runSession(ctx, subConn, draft)
	if err != nil {
		return fmt.Errorf("subscriber session: %w", err)
	}
	defer closeSession(sub)

	// Subscribe in background (may block waiting for publisher)
	var subErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, subErr = sub.Subscribe(ctx, ns, "test-track")
	}()

	// Wait 500ms, then publisher connects and announces
	time.Sleep(500 * time.Millisecond)

	pub, _, pubErr := runPublisherSession(ctx, relay, tlsSkipVerify, draft, ns)
	if pubErr != nil {
		return fmt.Errorf("publisher: %w", pubErr)
	}
	defer closeSession(pub)

	// Wait for subscriber result
	wg.Wait()

	// Either SUBSCRIBE_OK or REQUEST_ERROR is acceptable
	if subErr != nil && strings.Contains(subErr.Error(), "context deadline exceeded") {
		return fmt.Errorf("subscriber timed out (neither OK nor ERROR received)")
	}
	// subErr == nil means SUBSCRIBE_OK, non-nil means REQUEST_ERROR — both valid
	return nil
}
