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

// connectSession wires the given client session to the relay handler over an
// in-memory connection pair.
func connectSession(t *testing.T, h *relay.Handler, session *moqtransport.Session) (*testconn.Conn, *testconn.Conn) {
	t.Helper()
	sConn, cConn := testconn.Pair()
	go h.Handle(t.Context(), sConn)
	require.NoError(t, session.Run(t.Context(), cConn))
	return sConn, cConn
}

// connect wires a bare client session to the relay handler, mirroring what an
// interop test client does after dialing.
func connect(t *testing.T, h *relay.Handler) (*moqtransport.Session, *testconn.Conn, *testconn.Conn) {
	t.Helper()
	session := &moqtransport.Session{Implementation: "mlmrel-test-client"}
	sConn, cConn := connectSession(t, h, session)
	return session, sConn, cConn
}

// oneObjectPublisher is a client session that accepts any SUBSCRIBE, serves a
// single object at {7, 0} with the given payload, and ends the subscription
// with PUBLISH_DONE TrackEnded.
//
// The sleep before PUBLISH_DONE stands in for a real publisher's pacing, and
// under synctest it is deterministic: fake time only advances once the whole
// chain has gone idle, i.e. the object has been forwarded and read. Without
// it, PUBLISH_DONE races the object's subgroup stream (moqtransport exposes
// PublishDone.StreamCount but does not wait for the streams itself) and
// SUBSCRIBE_OK races the stream end in awaitEstablished.
func oneObjectPublisher(payload []byte) *moqtransport.Session {
	return &moqtransport.Session{
		Implementation: "mlmrel-test-publisher",
		SubscribeHandler: moqtransport.SubscribeHandlerFunc(func(r *moqtransport.SubscribeRequest) {
			sub, err := r.Accept(moqtransport.WithLargestObject(moqtransport.Location{Group: 7, Object: 0}))
			if err != nil {
				return
			}
			sg, err := sub.OpenSubgroup(7, 0, 128)
			if err != nil {
				return
			}
			_, _ = sg.WriteObject(0, payload)
			_ = sg.Close()
			time.Sleep(10 * time.Millisecond)
			_ = sub.Close(moqtransport.PublishDoneTrackEnded, "one object is all there is")
		}),
	}
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

// TestSubscribeForwarding is the announce-subscribe interop case and more: a
// subscription from one session is forwarded to the session that announced
// the namespace, the SUBSCRIBE_OK metadata and the objects come through, and
// the upstream PUBLISH_DONE propagates.
func TestSubscribeForwarding(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := relay.NewHandler(io.Discard)
		payload := []byte("hello relay")

		pubSession := oneObjectPublisher(payload)
		psConn, pcConn := connectSession(t, h, pubSession)
		subSession, ssConn, scConn := connect(t, h)

		publication, err := pubSession.PublishNamespace(t.Context(), testNamespace)
		require.NoError(t, err)

		remote, err := subSession.Subscribe(t.Context(), testNamespace, "test-track")
		require.NoError(t, err)
		largest, ok := remote.LargestObject()
		require.True(t, ok)
		require.Equal(t, moqtransport.Location{Group: 7, Object: 0}, largest)

		obj, err := remote.ReadObject(t.Context())
		require.NoError(t, err)
		require.Equal(t, uint64(7), obj.GroupID)
		require.Equal(t, uint64(0), obj.SubgroupID)
		require.Equal(t, uint64(0), obj.ObjectID)
		require.Equal(t, uint8(128), obj.Priority)
		require.Equal(t, payload, obj.Payload)

		_, err = remote.ReadObject(t.Context())
		require.Error(t, err)
		done, ok := remote.PublishDone()
		require.True(t, ok)
		require.Equal(t, moqtransport.PublishDoneTrackEnded, done.Code)

		// Withdrawing the announcement (PUBLISH_NAMESPACE_DONE) deregisters
		// the namespace again.
		require.NoError(t, publication.Close())
		synctest.Wait()
		_, err = subSession.Subscribe(t.Context(), testNamespace, "test-track")
		requireRequestError(t, err, moqtransport.RequestErrorDoesNotExist)

		shutdown(psConn, pcConn, ssConn, scConn)
	})
}

// TestRejectPropagation: the announcing session's rejection of the forwarded
// subscription reaches the subscriber with the same code.
func TestRejectPropagation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := relay.NewHandler(io.Discard)

		pubSession := &moqtransport.Session{
			Implementation: "mlmrel-test-publisher",
			SubscribeHandler: moqtransport.SubscribeHandlerFunc(func(r *moqtransport.SubscribeRequest) {
				_ = r.Reject(moqtransport.RequestErrorUninterested, "not today")
			}),
		}
		psConn, pcConn := connectSession(t, h, pubSession)
		subSession, ssConn, scConn := connect(t, h)

		_, err := pubSession.PublishNamespace(t.Context(), testNamespace)
		require.NoError(t, err)

		_, err = subSession.Subscribe(t.Context(), testNamespace, "test-track")
		requireRequestError(t, err, moqtransport.RequestErrorUninterested)

		shutdown(psConn, pcConn, ssConn, scConn)
	})
}

// TestPendingWait is the subscribe-before-announce rendezvous: with a
// positive PendingWait, a SUBSCRIBE arriving before the announcement is held
// and answered once the announcement lands.
func TestPendingWait(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := relay.NewHandler(io.Discard)
		h.PendingWait = time.Second

		pubSession := oneObjectPublisher([]byte("late but present"))
		psConn, pcConn := connectSession(t, h, pubSession)
		subSession, ssConn, scConn := connect(t, h)

		subscribed := make(chan error, 1)
		go func() {
			_, err := subSession.Subscribe(t.Context(), testNamespace, "test-track")
			subscribed <- err
		}()

		// Announce 500 ms after the SUBSCRIBE, like the interop case does.
		time.Sleep(500 * time.Millisecond)
		_, err := pubSession.PublishNamespace(t.Context(), testNamespace)
		require.NoError(t, err)

		require.NoError(t, <-subscribed)

		// Let the publisher's pacing sleep elapse and the subscription wind
		// down before tearing the bubble down.
		time.Sleep(50 * time.Millisecond)
		shutdown(psConn, pcConn, ssConn, scConn)
	})
}

// TestPendingWaitTimeout: with PendingWait set and no announcement, the
// rejection comes after the wait rather than never.
func TestPendingWaitTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := relay.NewHandler(io.Discard)
		h.PendingWait = time.Second

		session, sConn, cConn := connect(t, h)
		start := time.Now()
		_, err := session.Subscribe(t.Context(), testNamespace, "test-track")
		requireRequestError(t, err, moqtransport.RequestErrorDoesNotExist)
		require.GreaterOrEqual(t, time.Since(start), time.Second)

		shutdown(sConn, cConn)
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
