package relay_test

import (
	"io"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Eyevinn/moqlivemock/internal/relay"
	"github.com/Eyevinn/moqlivemock/internal/testconn"
	"github.com/Eyevinn/moqtransport"
	"github.com/stretchr/testify/require"
)

var testNamespace = []string{"moq-test", "interop"}

// connect wires a bare client session to the relay handler over an in-memory
// connection pair, mirroring what an interop test client does after dialing.
func connect(t *testing.T, h *relay.Handler) (*moqtransport.Session, *testconn.Conn, *testconn.Conn) {
	t.Helper()
	sConn, cConn := testconn.Pair()
	go h.Handle(t.Context(), sConn)
	session := &moqtransport.Session{Implementation: "mlmrel-test-client"}
	require.NoError(t, session.Run(t.Context(), cConn))
	return session, sConn, cConn
}

// shutdown closes the connections and waits for goroutines to drain.
func shutdown(conns ...*testconn.Conn) {
	for _, c := range conns {
		_ = c.CloseWithError(0, "")
	}
	time.Sleep(time.Millisecond)
}

func requireRequestError(t *testing.T, err error, code moqtransport.RequestErrorCode) {
	t.Helper()
	var reqErr *moqtransport.RequestError
	require.ErrorAs(t, err, &reqErr)
	require.Equal(t, code, reqErr.Code)
}

// TestSubscribeUnknownNamespace is the subscribe-error interop case: a
// SUBSCRIBE for a namespace nobody announced gets a prompt REQUEST_ERROR.
func TestSubscribeUnknownNamespace(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := relay.NewHandler(io.Discard)
		session, sConn, cConn := connect(t, h)

		_, err := session.Subscribe(t.Context(), []string{"nonexistent", "namespace"}, "test-track")
		requireRequestError(t, err, moqtransport.RequestErrorDoesNotExist)

		shutdown(sConn, cConn)
	})
}

// TestAnnounceRoutingAndWithdrawal covers announce-only, publish-namespace-done
// and the routing half of announce-subscribe: an announced namespace resolves
// for a subscriber on another session, and withdrawing it empties the table.
func TestAnnounceRoutingAndWithdrawal(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := relay.NewHandler(io.Discard)
		pubSession, psConn, pcConn := connect(t, h)
		subSession, ssConn, scConn := connect(t, h)

		publication, err := pubSession.PublishNamespace(t.Context(), testNamespace)
		require.NoError(t, err)

		// The announcement resolves across sessions. Phase 1 answers
		// NOT_SUPPORTED where later phases will forward the subscription.
		_, err = subSession.Subscribe(t.Context(), testNamespace, "test-track")
		requireRequestError(t, err, moqtransport.RequestErrorNotSupported)

		// Withdrawing the announcement (PUBLISH_NAMESPACE_DONE) deregisters it.
		require.NoError(t, publication.Close())
		synctest.Wait()
		_, err = subSession.Subscribe(t.Context(), testNamespace, "test-track")
		requireRequestError(t, err, moqtransport.RequestErrorDoesNotExist)

		shutdown(psConn, pcConn, ssConn, scConn)
	})
}

// TestNoStaleStateAcrossSessions reconnects with the same namespace
// repeatedly, the way the interop-runner's test process does, and checks that
// a dead session leaves nothing behind in the table.
func TestNoStaleStateAcrossSessions(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := relay.NewHandler(io.Discard)

		for range 3 {
			session, sConn, cConn := connect(t, h)
			_, err := session.PublishNamespace(t.Context(), testNamespace)
			require.NoError(t, err)
			shutdown(sConn, cConn)
			synctest.Wait()
		}

		// Every announcing session is gone, so the namespace must be too.
		session, sConn, cConn := connect(t, h)
		_, err := session.Subscribe(t.Context(), testNamespace, "test-track")
		requireRequestError(t, err, moqtransport.RequestErrorDoesNotExist)

		shutdown(sConn, cConn)
	})
}
