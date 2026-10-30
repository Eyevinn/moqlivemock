package relay_test

import (
	"io"
	"slices"
	"sync/atomic"
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
// with PUBLISH_DONE TrackEnded -- all in one breath. Serving and closing
// this fast used to race SUBSCRIBE_OK against the stream end and
// PUBLISH_DONE against the object's subgroup stream; moqtransport v0.12.0
// fixed both, and running the relay tests against a publisher this abrupt is
// what keeps them fixed.
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
// Without RENDEZVOUS_TIMEOUT the subscriber wants no wait at all (Section
// 10.2.6), so the answer is DOES_NOT_EXIST and it comes at once.
func TestSubscribeUnknownNamespace(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := relay.NewHandler(io.Discard)
		session, sConn, cConn := connect(t, h)

		start := time.Now()
		_, err := session.Subscribe(t.Context(), []string{"nonexistent", "namespace"}, "test-track")
		requireRequestError(t, err, moqtransport.RequestErrorDoesNotExist)
		require.Zero(t, time.Since(start), "no RENDEZVOUS_TIMEOUT, no hold")

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

// acceptingPublisher is a publisher session that accepts every SUBSCRIBE and
// reports the namespace it was asked for, under its name.
func acceptingPublisher(name string, asked chan<- []string) *moqtransport.Session {
	return &moqtransport.Session{
		Implementation: name,
		SubscribeHandler: moqtransport.SubscribeHandlerFunc(func(r *moqtransport.SubscribeRequest) {
			asked <- append([]string{name}, r.Namespace()...)
			_, _ = r.Accept()
		}),
	}
}

// TestSubscribePrefixMatch: a SUBSCRIBE for a namespace that extends an
// announced one reaches the publisher of the announced one, which is asked for
// the full namespace (Namespace Prefix Matching, draft-18 Section 9.5). The
// interop runner's data-subgroup-basic case subscribes like this. A namespace
// that only shares a field's leading characters is no match.
func TestSubscribePrefixMatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := relay.NewHandler(io.Discard)
		asked := make(chan []string, 1)
		pubSession := acceptingPublisher("pub", asked)
		psConn, pcConn := connectSession(t, h, pubSession)
		subSession, ssConn, scConn := connect(t, h)

		_, err := pubSession.PublishNamespace(t.Context(), testNamespace)
		require.NoError(t, err)

		deep := append(slices.Clone(testNamespace), "moq-test-00", "0", "2")
		_, err = subSession.Subscribe(t.Context(), deep, "track")
		require.NoError(t, err)
		require.Equal(t, append([]string{"pub"}, deep...), <-asked)

		_, err = subSession.Subscribe(t.Context(), []string{"moq-test", "interop2"}, "track")
		requireRequestError(t, err, moqtransport.RequestErrorDoesNotExist)

		shutdown(psConn, pcConn, ssConn, scConn)
	})
}

// TestSubscribeLongestPrefix: of two matching announcements, the more specific
// one serves the subscription.
func TestSubscribeLongestPrefix(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := relay.NewHandler(io.Discard)
		asked := make(chan []string, 2)
		short := acceptingPublisher("short", asked)
		long := acceptingPublisher("long", asked)
		shConn, scConn1 := connectSession(t, h, short)
		lsConn, lcConn := connectSession(t, h, long)
		subSession, ssConn, scConn := connect(t, h)

		_, err := short.PublishNamespace(t.Context(), []string{"moq-test"})
		require.NoError(t, err)
		_, err = long.PublishNamespace(t.Context(), testNamespace)
		require.NoError(t, err)

		_, err = subSession.Subscribe(t.Context(), append(slices.Clone(testNamespace), "x"), "track")
		require.NoError(t, err)
		require.Equal(t, "long", (<-asked)[0])

		_, err = subSession.Subscribe(t.Context(), []string{"moq-test", "other"}, "track")
		require.NoError(t, err)
		require.Equal(t, "short", (<-asked)[0])

		shutdown(shConn, scConn1, lsConn, lcConn, ssConn, scConn)
	})
}

// TestRendezvousPrefix: a held SUBSCRIBE is answered by the announcement of a
// prefix of its namespace, and not by an unrelated one.
func TestRendezvousPrefix(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := relay.NewHandler(io.Discard)
		asked := make(chan []string, 2)
		other := acceptingPublisher("other", asked)
		pub := acceptingPublisher("pub", asked)
		osConn, ocConn := connectSession(t, h, other)
		psConn, pcConn := connectSession(t, h, pub)
		subSession, ssConn, scConn := connect(t, h)

		deep := append(slices.Clone(testNamespace), "deep")
		subscribed := make(chan error, 1)
		go func() {
			_, err := subSession.Subscribe(t.Context(), deep, "track",
				moqtransport.WithRendezvousTimeout(time.Second))
			subscribed <- err
		}()

		time.Sleep(200 * time.Millisecond)
		_, err := other.PublishNamespace(t.Context(), []string{"moq-test", "other"})
		require.NoError(t, err)
		time.Sleep(300 * time.Millisecond)
		_, err = pub.PublishNamespace(t.Context(), testNamespace)
		require.NoError(t, err)

		require.NoError(t, <-subscribed)
		require.Equal(t, append([]string{"pub"}, deep...), <-asked)

		shutdown(osConn, ocConn, psConn, pcConn, ssConn, scConn)
	})
}

// TestFetchPrefixMatch: a FETCH is routed by the same prefix matching as a
// SUBSCRIBE, and proxied with the full namespace.
func TestFetchPrefixMatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := relay.NewHandler(io.Discard)
		asked := make(chan []string, 1)
		pubSession := &moqtransport.Session{
			Implementation: "mlmrel-test-publisher",
			FetchHandler: moqtransport.FetchHandlerFunc(func(r *moqtransport.FetchRequest) {
				asked <- r.Namespace()
				response, err := r.Accept()
				if err != nil {
					return
				}
				_ = response.WriteObject(moqtransport.Object{
					GroupID: 0, ObjectID: 0, Priority: 128, Payload: []byte("fetched"),
				})
				_ = response.Close()
			}),
		}
		psConn, pcConn := connectSession(t, h, pubSession)
		subSession, ssConn, scConn := connect(t, h)

		_, err := pubSession.PublishNamespace(t.Context(), testNamespace)
		require.NoError(t, err)

		deep := append(slices.Clone(testNamespace), "deep")
		fs, err := subSession.Fetch(t.Context(), deep, "test-track",
			moqtransport.AbsoluteRange(moqtransport.Location{}, moqtransport.Location{}))
		require.NoError(t, err)
		require.Equal(t, deep, <-asked)
		fo, err := fs.ReadObject(t.Context())
		require.NoError(t, err)
		require.Equal(t, []byte("fetched"), fo.Payload)

		shutdown(psConn, pcConn, ssConn, scConn)
	})
}

// TestRendezvous is the subscribe-before-announce rendezvous a subscriber
// asks for: with RENDEZVOUS_TIMEOUT on the SUBSCRIBE, a request arriving
// before the announcement is held and answered once the announcement lands.
func TestRendezvous(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := relay.NewHandler(io.Discard)

		pubSession := oneObjectPublisher([]byte("late but present"))
		psConn, pcConn := connectSession(t, h, pubSession)
		subSession, ssConn, scConn := connect(t, h)

		subscribed := make(chan error, 1)
		go func() {
			_, err := subSession.Subscribe(t.Context(), testNamespace, "test-track",
				moqtransport.WithRendezvousTimeout(time.Second))
			subscribed <- err
		}()

		// Announce 500 ms after the SUBSCRIBE, like the interop case does.
		time.Sleep(500 * time.Millisecond)
		_, err := pubSession.PublishNamespace(t.Context(), testNamespace)
		require.NoError(t, err)

		require.NoError(t, <-subscribed)

		shutdown(psConn, pcConn, ssConn, scConn)
	})
}

// TestRendezvousExpires: when no publisher appears within the asked wait,
// the answer is TIMEOUT, at the end of the wait rather than never.
func TestRendezvousExpires(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := relay.NewHandler(io.Discard)

		session, sConn, cConn := connect(t, h)
		start := time.Now()
		_, err := session.Subscribe(t.Context(), testNamespace, "test-track",
			moqtransport.WithRendezvousTimeout(time.Second))
		requireRequestError(t, err, moqtransport.RequestErrorTimeout)
		require.Equal(t, time.Second, time.Since(start))

		shutdown(sConn, cConn)
	})
}

// TestRendezvousCapped: the relay may wait less than asked (Section 10.2.6);
// MaxRendezvous is that cap.
func TestRendezvousCapped(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := relay.NewHandler(io.Discard)
		h.MaxRendezvous = time.Second

		session, sConn, cConn := connect(t, h)
		start := time.Now()
		_, err := session.Subscribe(t.Context(), testNamespace, "test-track",
			moqtransport.WithRendezvousTimeout(5*time.Second))
		requireRequestError(t, err, moqtransport.RequestErrorTimeout)
		require.Equal(t, time.Second, time.Since(start))

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

// fanoutPublisher is a controllable publisher client: it accepts any
// SUBSCRIBE, hands the subscription to the test to drive, counts accepts,
// and signals when a subscription's request ends.
type fanoutPublisher struct {
	session      *moqtransport.Session
	accepts      atomic.Int32
	subs         chan *moqtransport.Subscription
	unsubscribed chan struct{}
}

func newFanoutPublisher() *fanoutPublisher {
	p := &fanoutPublisher{
		subs:         make(chan *moqtransport.Subscription, 4),
		unsubscribed: make(chan struct{}, 4),
	}
	p.session = &moqtransport.Session{
		Implementation: "mlmrel-test-publisher",
		SubscribeHandler: moqtransport.SubscribeHandlerFunc(func(r *moqtransport.SubscribeRequest) {
			p.accepts.Add(1)
			sub, err := r.Accept()
			if err != nil {
				return
			}
			p.subs <- sub
			<-r.Context().Done()
			p.unsubscribed <- struct{}{}
		}),
	}
	return p
}

// TestFanoutSharedUpstream is the heart of phase 3: two subscribers share one
// upstream subscription, the late joiner starts at the newest cached group,
// and the upstream subscription goes away a linger after the last subscriber.
func TestFanoutSharedUpstream(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := relay.NewHandler(io.Discard)
		h.Linger = 100 * time.Millisecond

		pub := newFanoutPublisher()
		psConn, pcConn := connectSession(t, h, pub.session)
		subA, asConn, acConn := connect(t, h)
		subB, bsConn, bcConn := connect(t, h)

		_, err := pub.session.PublishNamespace(t.Context(), testNamespace)
		require.NoError(t, err)

		remoteA, err := subA.Subscribe(t.Context(), testNamespace, "test-track")
		require.NoError(t, err)
		upSub := <-pub.subs

		sg1, err := upSub.OpenSubgroup(1, 0, 128)
		require.NoError(t, err)
		_, err = sg1.WriteObject(0, []byte("g1o0"))
		require.NoError(t, err)
		obj, err := remoteA.ReadObject(t.Context())
		require.NoError(t, err)
		require.Equal(t, []byte("g1o0"), obj.Payload)

		// B joins late: still exactly one upstream subscription, SUBSCRIBE_OK
		// carries the relay's largest location, and delivery starts at the
		// beginning of the newest cached group.
		remoteB, err := subB.Subscribe(t.Context(), testNamespace, "test-track")
		require.NoError(t, err)
		require.Equal(t, int32(1), pub.accepts.Load())
		largest, ok := remoteB.LargestObject()
		require.True(t, ok)
		require.Equal(t, moqtransport.Location{Group: 1, Object: 0}, largest)
		obj, err = remoteB.ReadObject(t.Context())
		require.NoError(t, err)
		require.Equal(t, uint64(1), obj.GroupID)
		require.Equal(t, []byte("g1o0"), obj.Payload)

		// A new group reaches both.
		require.NoError(t, sg1.Close())
		sg2, err := upSub.OpenSubgroup(2, 0, 128)
		require.NoError(t, err)
		_, err = sg2.WriteObject(0, []byte("g2o0"))
		require.NoError(t, err)
		for _, remote := range []*moqtransport.RemoteTrack{remoteA, remoteB} {
			obj, err := remote.ReadObject(t.Context())
			require.NoError(t, err)
			require.Equal(t, uint64(2), obj.GroupID)
			require.Equal(t, []byte("g2o0"), obj.Payload)
		}

		// Both leave; the upstream subscription survives the linger, no
		// longer.
		require.NoError(t, remoteA.Close())
		require.NoError(t, remoteB.Close())
		time.Sleep(50 * time.Millisecond)
		synctest.Wait()
		select {
		case <-pub.unsubscribed:
			t.Fatal("upstream unsubscribed before the linger elapsed")
		default:
		}
		time.Sleep(100 * time.Millisecond)
		synctest.Wait()
		select {
		case <-pub.unsubscribed:
		default:
			t.Fatal("upstream not unsubscribed after the linger")
		}

		shutdown(psConn, pcConn, asConn, acConn, bsConn, bcConn)
	})
}

// TestFetchProxied: a FETCH for a range the cache does not cover is proxied
// to the announcing session and pumped through losslessly.
func TestFetchProxied(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := relay.NewHandler(io.Discard)

		pubSession := &moqtransport.Session{
			Implementation: "mlmrel-test-publisher",
			FetchHandler: moqtransport.FetchHandlerFunc(func(r *moqtransport.FetchRequest) {
				response, err := r.Accept()
				if err != nil {
					return
				}
				_ = response.WriteObject(moqtransport.Object{
					GroupID: 0, ObjectID: 0, Priority: 128, Payload: []byte("fetched"),
				})
				_ = response.Close()
			}),
		}
		psConn, pcConn := connectSession(t, h, pubSession)
		subSession, ssConn, scConn := connect(t, h)

		_, err := pubSession.PublishNamespace(t.Context(), testNamespace)
		require.NoError(t, err)

		fs, err := subSession.Fetch(t.Context(), testNamespace, "test-track",
			moqtransport.AbsoluteRange(moqtransport.Location{}, moqtransport.Location{}))
		require.NoError(t, err)
		fo, err := fs.ReadObject(t.Context())
		require.NoError(t, err)
		require.Equal(t, []byte("fetched"), fo.Payload)
		_, err = fs.ReadObject(t.Context())
		require.ErrorIs(t, err, moqtransport.ErrFetchComplete)

		shutdown(psConn, pcConn, ssConn, scConn)
	})
}

// TestFetchFromCache: a FETCH range fully covered by an active track's group
// cache is served by the relay itself. The publisher has no FetchHandler, so
// a proxied fetch would fail with NOT_SUPPORTED -- success proves the cache.
func TestFetchFromCache(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := relay.NewHandler(io.Discard)

		pub := newFanoutPublisher() // no FetchHandler
		psConn, pcConn := connectSession(t, h, pub.session)
		subSession, ssConn, scConn := connect(t, h)

		_, err := pub.session.PublishNamespace(t.Context(), testNamespace)
		require.NoError(t, err)
		remote, err := subSession.Subscribe(t.Context(), testNamespace, "test-track")
		require.NoError(t, err)
		upSub := <-pub.subs

		sg1, err := upSub.OpenSubgroup(1, 0, 128)
		require.NoError(t, err)
		_, err = sg1.WriteObject(0, []byte("a"))
		require.NoError(t, err)
		_, err = sg1.WriteObject(1, []byte("b"))
		require.NoError(t, err)
		require.NoError(t, sg1.Close())
		sg2, err := upSub.OpenSubgroup(2, 0, 128)
		require.NoError(t, err)
		_, err = sg2.WriteObject(0, []byte("c")) // completes group 1 in the cache
		require.NoError(t, err)

		// Drain the subscription so the relay has seen everything.
		for range 3 {
			_, err := remote.ReadObject(t.Context())
			require.NoError(t, err)
		}

		fs, err := subSession.Fetch(t.Context(), testNamespace, "test-track",
			moqtransport.AbsoluteRange(moqtransport.Location{Group: 1}, moqtransport.Location{Group: 1, Object: 1}))
		require.NoError(t, err)
		var payloads []string
		for {
			fo, err := fs.ReadObject(t.Context())
			if err != nil {
				require.ErrorIs(t, err, moqtransport.ErrFetchComplete)
				break
			}
			require.Equal(t, uint64(1), fo.GroupID)
			payloads = append(payloads, string(fo.Payload))
		}
		require.Equal(t, []string{"a", "b"}, payloads)

		shutdown(psConn, pcConn, ssConn, scConn)
	})
}

// TestFill: a draft-22 subscriber joining with a fill gets it from the
// relay's cache when the cache covers the fill range. The publisher has no
// FetchHandler, so a fill the relay has to fetch upstream fails, and the
// relay resets it; the subscription carries on regardless.
func TestFill(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := relay.NewHandler(io.Discard)

		pub := newFanoutPublisher() // no FetchHandler
		psConn, pcConn := connectSession(t, h, pub.session)
		subSession, ssConn, scConn := connect(t, h)
		require.Equal(t, moqtransport.Version22, subSession.Version())

		_, err := pub.session.PublishNamespace(t.Context(), testNamespace)
		require.NoError(t, err)
		first, err := subSession.Subscribe(t.Context(), testNamespace, "test-track")
		require.NoError(t, err)
		upSub := <-pub.subs

		sg1, err := upSub.OpenSubgroup(1, 0, 128)
		require.NoError(t, err)
		for i, p := range []string{"a", "b"} {
			_, err = sg1.WriteObject(uint64(i), []byte(p))
			require.NoError(t, err)
		}
		require.NoError(t, sg1.Close())
		sg2, err := upSub.OpenSubgroup(2, 0, 128)
		require.NoError(t, err)
		_, err = sg2.WriteObject(0, []byte("c")) // completes group 1 in the cache
		require.NoError(t, err)
		for range 3 {
			_, err := first.ReadObject(t.Context())
			require.NoError(t, err)
		}

		// Two Groups back from the Next Group is Group 1, and the fill ends
		// at the Largest Object, {2, 0}: all of it is cached.
		cached, err := subSession.Subscribe(t.Context(), testNamespace, "test-track",
			moqtransport.WithFilter(moqtransport.NextObject()),
			moqtransport.WithFill(moqtransport.FillRange(moqtransport.RelativeStart(2))))
		require.NoError(t, err)
		var payloads []string
		for {
			fo, err := cached.Fill().ReadObject(t.Context())
			if err != nil {
				require.ErrorIs(t, err, moqtransport.ErrFetchComplete)
				break
			}
			payloads = append(payloads, string(fo.Payload))
		}
		require.Equal(t, []string{"a", "b", "c"}, payloads)

		// Group 0 was never cached, so this fill goes upstream and fails.
		uncached, err := subSession.Subscribe(t.Context(), testNamespace, "test-track",
			moqtransport.WithFilter(moqtransport.NextObject()),
			moqtransport.WithFill(moqtransport.FillRange(moqtransport.AbsoluteStart(moqtransport.Location{}))))
		require.NoError(t, err)
		_, err = uncached.Fill().ReadObject(t.Context())
		require.Error(t, err)
		require.NotErrorIs(t, err, moqtransport.ErrFetchComplete)

		_, err = sg2.WriteObject(1, []byte("d"))
		require.NoError(t, err)
		for {
			o, err := uncached.ReadObject(t.Context())
			require.NoError(t, err, "the subscription outlives its failed fill")
			if string(o.Payload) == "d" {
				break
			}
		}

		shutdown(psConn, pcConn, ssConn, scConn)
	})
}

// TestSubscribeNamespacePropagation: SUBSCRIBE_NAMESPACE gets live
// announcements, withdrawals, and replay of already-known namespaces.
func TestSubscribeNamespacePropagation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := relay.NewHandler(io.Discard)

		observer, osConn, ocConn := connect(t, h)
		nsSub, err := observer.SubscribeNamespace(t.Context(), []string{"moq-test"})
		require.NoError(t, err)

		pubSession, psConn, pcConn := connect(t, h)
		publication, err := pubSession.PublishNamespace(t.Context(), testNamespace)
		require.NoError(t, err)

		ev := <-nsSub.Namespaces()
		require.True(t, ev.Available)
		require.Equal(t, testNamespace, ev.Namespace)

		// A second observer arriving now gets the namespace replayed.
		observer2, o2sConn, o2cConn := connect(t, h)
		nsSub2, err := observer2.SubscribeNamespace(t.Context(), []string{"moq-test"})
		require.NoError(t, err)
		ev = <-nsSub2.Namespaces()
		require.True(t, ev.Available)
		require.Equal(t, testNamespace, ev.Namespace)

		// Withdrawal reaches both.
		require.NoError(t, publication.Close())
		ev = <-nsSub.Namespaces()
		require.False(t, ev.Available)
		ev = <-nsSub2.Namespaces()
		require.False(t, ev.Available)

		shutdown(osConn, ocConn, psConn, pcConn, o2sConn, o2cConn)
	})
}

// TestMultiPublisherNamespaceRetention is the interop runner's
// publish-namespace-multi-publisher-retention case: two publishers announce
// the same namespace and one leaves, by withdrawing or by dropping its
// connection. The other still publishes it (Section 9.3), so the namespace
// stays: no NAMESPACE_DONE, a late namespace subscriber is told about it, and
// a SUBSCRIBE reaches the publisher that remains. Only the last publisher to
// leave ends it.
func TestMultiPublisherNamespaceRetention(t *testing.T) {
	for _, leave := range []string{"withdraw", "disconnect"} {
		t.Run(leave, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				h := relay.NewHandler(io.Discard)
				observer, osConn, ocConn := connect(t, h)
				nsSub, err := observer.SubscribeNamespace(t.Context(), []string{"moq-test"})
				require.NoError(t, err)

				asked := make(chan []string, 3)
				first := acceptingPublisher("first", asked)
				fsConn, fcConn := connectSession(t, h, first)
				firstPub, err := first.PublishNamespace(t.Context(), testNamespace)
				require.NoError(t, err)
				ev := <-nsSub.Namespaces()
				require.True(t, ev.Available)

				second := acceptingPublisher("second", asked)
				s2sConn, s2cConn := connectSession(t, h, second)
				secondPub, err := second.PublishNamespace(t.Context(), testNamespace)
				require.NoError(t, err)

				// While both publish, the newer announcement serves.
				subSession, ssConn, scConn := connect(t, h)
				_, err = subSession.Subscribe(t.Context(), testNamespace, "track-a")
				require.NoError(t, err)
				require.Equal(t, "second", (<-asked)[0])

				if leave == "withdraw" {
					require.NoError(t, secondPub.Close())
				} else {
					shutdown(s2sConn, s2cConn)
				}
				synctest.Wait()
				select {
				case ev := <-nsSub.Namespaces():
					t.Fatalf("namespace event %+v while the first publisher remains", ev)
				default:
				}

				late, lsConn, lcConn := connect(t, h)
				lateSub, err := late.SubscribeNamespace(t.Context(), []string{"moq-test"})
				require.NoError(t, err)
				ev = <-lateSub.Namespaces()
				require.True(t, ev.Available)
				require.Equal(t, testNamespace, ev.Namespace)

				_, err = subSession.Subscribe(t.Context(), testNamespace, "track-b")
				require.NoError(t, err)
				require.Equal(t, "first", (<-asked)[0])

				require.NoError(t, firstPub.Close())
				ev = <-nsSub.Namespaces()
				require.False(t, ev.Available)
				ev = <-lateSub.Namespaces()
				require.False(t, ev.Available)
				_, err = subSession.Subscribe(t.Context(), testNamespace, "track-c")
				requireRequestError(t, err, moqtransport.RequestErrorDoesNotExist)

				shutdown(osConn, ocConn, fsConn, fcConn, s2sConn, s2cConn, ssConn, scConn, lsConn, lcConn)
			})
		})
	}
}

// TestAnnouncementNotForwardedToSilentSessions: a session that never sent
// SUBSCRIBE_NAMESPACE is told nothing. Section 8.4 has a relay forward
// PUBLISH_NAMESPACE to matching subscribers, and a session that asked for
// nothing matches none of them -- neither for a namespace announced before it
// arrived nor for one announced after.
func TestAnnouncementNotForwardedToSilentSessions(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := relay.NewHandler(io.Discard)

		pubSession, psConn, pcConn := connect(t, h)
		publication, err := pubSession.PublishNamespace(t.Context(), testNamespace)
		require.NoError(t, err)

		received := make(chan []string, 4)
		observer := &moqtransport.Session{
			Implementation: "mlmrel-test-observer",
			PublishNamespaceHandler: moqtransport.PublishNamespaceHandlerFunc(
				func(r *moqtransport.PublishNamespaceRequest) {
					if err := r.Accept(); err != nil {
						return
					}
					received <- r.Namespace()
				}),
		}
		osConn, ocConn := connectSession(t, h, observer)

		// Nothing replayed when the session joins.
		synctest.Wait()
		require.Empty(t, received)

		// Nothing when a further namespace is announced either.
		second := []string{"moq-test", "second"}
		publication2, err := pubSession.PublishNamespace(t.Context(), second)
		require.NoError(t, err)
		synctest.Wait()
		require.Empty(t, received)

		require.NoError(t, publication.Close())
		require.NoError(t, publication2.Close())
		shutdown(psConn, pcConn, osConn, ocConn)
	})
}

// TestUpstreamNamespaceDiscovery: the relay asks its upstream for namespaces
// instead of waiting to be told, so an upstream that announces nothing
// unprompted -- which is what a conformant relay is -- still fills the table
// and is routable from downstream.
func TestUpstreamNamespaceDiscovery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := relay.NewHandler(io.Discard)

		// An upstream that answers SUBSCRIBE_NAMESPACE and volunteers nothing.
		upstream := &moqtransport.Session{
			Implementation: "mlmrel-test-upstream",
			SubscribeNamespaceHandler: moqtransport.SubscribeNamespaceHandlerFunc(
				func(r *moqtransport.SubscribeNamespaceRequest) {
					announcer, err := r.Accept()
					if err != nil {
						return
					}
					if err := announcer.Announce(testNamespace); err != nil {
						return
					}
					<-r.Context().Done()
				}),
		}
		usConn, ucConn := testconn.Pair()
		go h.HandleUpstream(t.Context(), usConn)
		require.NoError(t, upstream.Run(t.Context(), ucConn))

		// Downstream learns it the only way it is offered: by asking.
		observer, osConn, ocConn := connect(t, h)
		nsSub, err := observer.SubscribeNamespace(t.Context(), []string{"moq-test"})
		require.NoError(t, err)
		ev := <-nsSub.Namespaces()
		require.True(t, ev.Available)
		require.Equal(t, testNamespace, ev.Namespace)

		shutdown(usConn, ucConn, osConn, ocConn)
	})
}

// TestUpstreamSetupOptions: the relay sends its upstream the PATH and
// AUTHORITY setup options a moqt:// -upstream URL calls for, and only its
// upstream: on a downstream session the relay is the server, which may send
// neither, so applying them there would fail the session.
func TestUpstreamSetupOptions(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		qlog := &testconn.QlogBuffer{}
		h := relay.NewHandler(qlog)
		h.UpstreamPath = "/anon?jwt=x"
		h.UpstreamAuthority = "relay.example:443"

		// The relay dials its upstream, so it holds the client end.
		usConn, ucConn := testconn.Pair()
		go h.HandleUpstream(t.Context(), ucConn)
		upstream := &moqtransport.Session{Implementation: "mlmrel-test-upstream"}
		require.NoError(t, upstream.Run(t.Context(), usConn))

		_, osConn, ocConn := connect(t, h)
		synctest.Wait()

		require.True(t, qlog.HasSetupOption(testconn.SetupOptionPath, "/anon?jwt=x"))
		require.True(t, qlog.HasSetupOption(testconn.SetupOptionAuthority, "relay.example:443"))
		shutdown(usConn, ucConn, osConn, ocConn)
	})
}

// TestResetPropagation: an upstream subgroup reset reaches the downstream
// subscriber as a reset, not as a clean end. The downstream test session
// turns on SubgroupEndEvents to observe exactly what the relay emitted.
func TestResetPropagation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := relay.NewHandler(io.Discard)

		pub := newFanoutPublisher()
		psConn, pcConn := connectSession(t, h, pub.session)
		subSession := &moqtransport.Session{
			Implementation:    "mlmrel-test-client",
			SubgroupEndEvents: true,
		}
		ssConn, scConn := connectSession(t, h, subSession)

		_, err := pub.session.PublishNamespace(t.Context(), testNamespace)
		require.NoError(t, err)
		remote, err := subSession.Subscribe(t.Context(), testNamespace, "test-track")
		require.NoError(t, err)
		upSub := <-pub.subs

		sg, err := upSub.OpenSubgroup(1, 0, 128)
		require.NoError(t, err)
		_, err = sg.WriteObject(0, []byte("before the reset"))
		require.NoError(t, err)

		obj, err := remote.ReadObject(t.Context())
		require.NoError(t, err)
		require.Equal(t, []byte("before the reset"), obj.Payload)

		// The upstream publisher abandons the subgroup; the relay must reset
		// its downstream counterpart rather than pretend it completed.
		sg.Reset(moqtransport.StreamErrorTooFarBehind)
		marker, err := remote.ReadObject(t.Context())
		require.NoError(t, err)
		require.True(t, marker.SubgroupReset, "the downstream subgroup must be reset, not FINed")
		require.False(t, marker.EndsSubgroup)
		require.Equal(t, uint64(1), marker.GroupID)

		shutdown(psConn, pcConn, ssConn, scConn)
	})
}

// TestEndOfGroupBitForwarded: the END_OF_GROUP bit on an upstream subgroup
// (the catalog track uses it) survives the relay, and its FIN arrives as a
// clean end-of-subgroup downstream.
func TestEndOfGroupBitForwarded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := relay.NewHandler(io.Discard)

		pub := newFanoutPublisher()
		psConn, pcConn := connectSession(t, h, pub.session)
		subSession := &moqtransport.Session{
			Implementation:    "mlmrel-test-client",
			SubgroupEndEvents: true,
		}
		ssConn, scConn := connectSession(t, h, subSession)

		_, err := pub.session.PublishNamespace(t.Context(), testNamespace)
		require.NoError(t, err)
		remote, err := subSession.Subscribe(t.Context(), testNamespace, "test-track")
		require.NoError(t, err)
		upSub := <-pub.subs

		sg, err := upSub.OpenSubgroup(0, 0, 128, moqtransport.WithEndOfGroup())
		require.NoError(t, err)
		_, err = sg.WriteObject(0, []byte("catalog-like"))
		require.NoError(t, err)
		require.NoError(t, sg.Close())

		obj, err := remote.ReadObject(t.Context())
		require.NoError(t, err)
		require.Equal(t, []byte("catalog-like"), obj.Payload)
		require.True(t, obj.EndOfGroup, "the END_OF_GROUP bit must survive the relay")

		marker, err := remote.ReadObject(t.Context())
		require.NoError(t, err)
		require.True(t, marker.EndsSubgroup, "the upstream FIN must arrive as a clean end")
		require.True(t, marker.EndOfGroup)

		shutdown(psConn, pcConn, ssConn, scConn)
	})
}

// TestUpstreamTimeout: a publisher that never answers the forwarded SUBSCRIBE
// costs the subscriber UpstreamTimeout, not its own patience. It gets a
// TIMEOUT, the upstream request is cancelled, and the failed attempt leaves
// no track behind, so the next SUBSCRIBE tries upstream afresh.
func TestUpstreamTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := relay.NewHandler(io.Discard)
		h.UpstreamTimeout = time.Second

		var cancelled atomic.Int32
		pubSession := &moqtransport.Session{
			Implementation: "mlmrel-test-publisher",
			SubscribeHandler: moqtransport.SubscribeHandlerFunc(func(r *moqtransport.SubscribeRequest) {
				<-r.Context().Done() // never answers
				cancelled.Add(1)
			}),
		}
		psConn, pcConn := connectSession(t, h, pubSession)
		subSession, ssConn, scConn := connect(t, h)

		_, err := pubSession.PublishNamespace(t.Context(), testNamespace)
		require.NoError(t, err)

		for attempt := range 2 {
			start := time.Now()
			_, err = subSession.Subscribe(t.Context(), testNamespace, "test-track")
			requireRequestError(t, err, moqtransport.RequestErrorTimeout)
			require.Equal(t, time.Second, time.Since(start), "attempt %d", attempt)
			synctest.Wait()
			require.Equal(t, int32(attempt+1), cancelled.Load(), "upstream request not cancelled")
		}

		shutdown(psConn, pcConn, ssConn, scConn)
	})
}

// TestFetchProxiedUpstreamTimeout: the same bound applies to a proxied FETCH
// whose upstream never answers.
func TestFetchProxiedUpstreamTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		h := relay.NewHandler(io.Discard)
		h.UpstreamTimeout = time.Second

		pubSession := &moqtransport.Session{
			Implementation: "mlmrel-test-publisher",
			FetchHandler: moqtransport.FetchHandlerFunc(func(r *moqtransport.FetchRequest) {
				<-r.Context().Done() // never answers
			}),
		}
		psConn, pcConn := connectSession(t, h, pubSession)
		subSession, ssConn, scConn := connect(t, h)

		_, err := pubSession.PublishNamespace(t.Context(), testNamespace)
		require.NoError(t, err)

		start := time.Now()
		_, err = subSession.Fetch(t.Context(), testNamespace, "test-track",
			moqtransport.AbsoluteRange(moqtransport.Location{}, moqtransport.Location{}))
		requireRequestError(t, err, moqtransport.RequestErrorTimeout)
		require.Equal(t, time.Second, time.Since(start))

		shutdown(psConn, pcConn, ssConn, scConn)
	})
}
