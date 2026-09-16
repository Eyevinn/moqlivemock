// Package relay implements a MoQ Transport relay. It accepts sessions from
// publishers and subscribers alike, keeps a table of announced namespaces,
// and forwards subscriptions to the session that announced the namespace:
// one upstream subscription per (namespace, track) is fanned out to any
// number of downstream subscribers through a small cache of recent groups,
// which also gives a late subscriber a group-aligned start. FETCHes are
// served from that cache when it covers the range and proxied upstream
// otherwise. Announcements reach the sessions that asked for them with
// SUBSCRIBE_NAMESPACE, and nobody else.
package relay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/Eyevinn/moqlivemock/internal/qlogfilter"
	"github.com/Eyevinn/moqtransport"
	"github.com/mengelbart/qlog"
)

// Handler handles MoQ relay sessions. One Handler serves every connection of
// a relay instance; the announcement and track tables are what its sessions
// share.
type Handler struct {
	// Logfh receives the qlog for every session.
	Logfh io.Writer
	// QlogFilter selects which qlog events reach Logfh; nil writes them all.
	// Build one with qlogfilter.ParseClasses.
	QlogFilter func(qlog.Event) bool
	// MaxRendezvous caps how long a SUBSCRIBE for a namespace nobody has
	// announced is held waiting for a publisher. The subscriber asks with
	// RENDEZVOUS_TIMEOUT (Section 10.2.6) and the hold is the shorter of the
	// two; a hold that runs out is answered with TIMEOUT. A SUBSCRIBE without
	// the parameter wants an immediate answer and gets DOES_NOT_EXIST at once.
	MaxRendezvous time.Duration
	// CacheGroups is how many recent groups are kept per track, for
	// group-aligned late joins and cache-served FETCHes.
	CacheGroups int
	// QueueLen is each subscriber's object queue length. A subscriber whose
	// queue overflows has its open subgroups reset and skips to the next
	// group boundary rather than stalling the upstream read loop.
	QueueLen int
	// Linger is how long an upstream subscription survives its last
	// downstream subscriber, so bouncing clients do not thrash the upstream.
	Linger time.Duration
	// UpstreamTimeout bounds how long a forwarded SUBSCRIBE or a proxied FETCH
	// waits for the upstream's answer. When it expires the downstream request
	// is rejected with TIMEOUT rather than left open for as long as the
	// subscriber cares to wait: a publisher that never answers must not take
	// its subscribers down with it. Zero waits indefinitely.
	UpstreamTimeout time.Duration

	mu            sync.Mutex
	announcements map[nsKey]*announcement
	waiters       map[nsKey][]chan *announcement
	tracks        map[trackKey]*relayTrack
	announcers    map[*nsAnnouncer]struct{}

	// nsMu serializes namespace fan-out (the replay to a new announcer and
	// live announce/withdraw notifications) so nobody sees a duplicate or
	// missed announcement. Fan-out writes wait for the peer's
	// answer, so a stalled peer slows announcement propagation -- never
	// object forwarding.
	nsMu sync.Mutex
}

// nsAnnouncer is one accepted SUBSCRIBE_NAMESPACE.
type nsAnnouncer struct {
	prefix    []string
	announcer *moqtransport.NamespaceAnnouncer
	announced map[nsKey]bool
}

// NewHandler creates a relay session handler writing its qlog to logfh.
func NewHandler(logfh io.Writer) *Handler {
	return &Handler{
		Logfh:           logfh,
		CacheGroups:     3,
		QueueLen:        256,
		Linger:          2 * time.Second,
		UpstreamTimeout: 5 * time.Second,
		MaxRendezvous:   10 * time.Second,
		announcements:   make(map[nsKey]*announcement),
		waiters:         make(map[nsKey][]chan *announcement),
		tracks:          make(map[trackKey]*relayTrack),
		announcers:      make(map[*nsAnnouncer]struct{}),
	}
}

// upstreamContext bounds a wait for an upstream answer by UpstreamTimeout. In
// moqtransport the context passed to Subscribe or Fetch governs only the
// request's establishment -- an established subscription or fetch lives on
// the session -- so the caller cancels it as soon as the answer is in.
func (h *Handler) upstreamContext(parent context.Context) (context.Context, context.CancelFunc) {
	if h.UpstreamTimeout <= 0 {
		return context.WithCancel(parent)
	}
	return context.WithTimeout(parent, h.UpstreamTimeout)
}

// upstreamError maps the expiry of an upstreamContext to the REQUEST_ERROR
// the downstream request should carry and leaves any other error alone. It
// must run before the context is cancelled, which replaces the deadline error.
func (h *Handler) upstreamError(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &moqtransport.RequestError{
			Code:   moqtransport.RequestErrorTimeout,
			Reason: fmt.Sprintf("upstream did not answer within %v", h.UpstreamTimeout),
		}
	}
	return err
}

// nsKey is a namespace tuple flattened to a comparable map key. The separator
// cannot appear in a tuple element, which is a UTF-8 string.
type nsKey string

func keyForNamespace(namespace []string) nsKey {
	return nsKey(strings.Join(namespace, "\x1f"))
}

// prefixMatches reports whether the namespace starts with the prefix tuple.
func prefixMatches(prefix, namespace []string) bool {
	if len(prefix) > len(namespace) {
		return false
	}
	for i, p := range prefix {
		if namespace[i] != p {
			return false
		}
	}
	return true
}

// announcement is a namespace some session has announced and not withdrawn.
type announcement struct {
	namespace []string
	session   *moqtransport.Session
	// request is the PUBLISH_NAMESPACE that created the entry, and is what
	// withdraws it. It is nil for a namespace the relay learned from its own
	// SUBSCRIBE_NAMESPACE upstream, which NAMESPACE_DONE withdraws instead.
	request *moqtransport.PublishNamespaceRequest
}

// Handle runs a MoQ session on the given connection and blocks until the
// session or the context ends. Announcements made by the session are dropped
// from the table when it ends.
func (h *Handler) Handle(ctx context.Context, conn moqtransport.Connection) {
	h.handle(ctx, conn, false)
}

// HandleUpstream runs a session toward a publisher the relay dialled and asks
// it for every namespace it has, rather than waiting to be told.
//
// A relay owes PUBLISH_NAMESPACE only to subscribers that asked for it
// (Section 8.4), so an upstream that plays by the rules announces nothing to a
// peer that stays quiet. Waiting works against mlmpub, which volunteers its
// namespaces as an origin publisher may, and against nothing else -- putting a
// relay in front of another relay needs the relay to ask.
func (h *Handler) HandleUpstream(ctx context.Context, conn moqtransport.Connection) {
	h.handle(ctx, conn, true)
}

func (h *Handler) handle(ctx context.Context, conn moqtransport.Connection, discover bool) {
	session := &moqtransport.Session{
		Implementation: "Eyevinn/moqlivemock/mlmrel",
		Qlogger: qlogfilter.Wrap(qlog.NewQLOGHandler(h.Logfh, "MoQ QLOG", "MoQ QLOG",
			conn.Perspective().String(), moqtransport.QlogSchema), h.QlogFilter),
		// The relay re-emits subgroups, so it needs the real end of each
		// upstream subgroup stream -- FIN or RESET -- rather than inferring
		// ends from group numbering.
		SubgroupEndEvents: true,
	}
	session.PublishNamespaceHandler = h.publishNamespaceHandler(session)
	session.SubscribeHandler = h.subscribeHandler()
	session.FetchHandler = h.fetchHandler()
	session.SubscribeNamespaceHandler = h.subscribeNamespaceHandler()

	slog.Info("starting MoQ session", "perspective", conn.Perspective())
	if err := session.Run(ctx, conn); err != nil {
		slog.Error("MoQ session initialization failed", "error", err)
		if err := conn.CloseWithError(0, "session initialization error"); err != nil {
			slog.Error("failed to close connection", "error", err)
		}
		return
	}
	slog.Info("MoQ session established", "version", session.Version())
	if discover {
		go h.discoverNamespaces(session)
	}

	select {
	case <-ctx.Done():
	case <-session.Context().Done():
		slog.Info("MoQ session ended", "reason", context.Cause(session.Context()))
	case <-conn.Context().Done():
		// The session context is not cancelled when the connection dies under
		// it: the accept loops see the error and simply return. Watching only
		// the session leaves a peer that vanished -- idle timeout, crash,
		// pulled cable -- registered forever, and with -upstream it also means
		// runUpstream never gets its session back to redial.
		slog.Info("connection closed", "reason", context.Cause(conn.Context()))
	}
	// The per-announcement watchers normally clean the table, but sweep by
	// session as well so that nothing this session announced can go stale:
	// a relay that retains namespace state after a session ends breaks the
	// peer's next announcement of the same name.
	h.dropSession(session)
}

// publishNamespaceHandler accepts any announcement: the interop-runner tests
// (and real publishers) announce arbitrary namespaces with no authorization.
// The handler runs on its own goroutine and the announcement lasts as long as
// its request stream, so it blocks here until withdrawal.
func (h *Handler) publishNamespaceHandler(session *moqtransport.Session) moqtransport.PublishNamespaceHandler {
	return moqtransport.PublishNamespaceHandlerFunc(func(r *moqtransport.PublishNamespaceRequest) {
		// Register before answering: the moment REQUEST_OK reaches the
		// publisher it may tell a subscriber to come, and a SUBSCRIBE that
		// observed the OK must resolve. Accepting first left a window in
		// which the relay rejected a namespace it had just acknowledged.
		h.register(session, r)
		slog.Info("registered announcement", "namespace", r.Namespace())
		if err := r.Accept(); err != nil {
			slog.Error("failed to accept announcement", "namespace", r.Namespace(), "error", err)
			h.deregister(r)
			return
		}
		h.announceToSubscribers(r.Namespace())

		<-r.Context().Done()
		if h.deregister(r) {
			slog.Info("announcement withdrawn", "namespace", r.Namespace())
			h.withdrawFromSubscribers(r.Namespace())
		}
	})
}

func (h *Handler) subscribeHandler() moqtransport.SubscribeHandler {
	return moqtransport.SubscribeHandlerFunc(func(r *moqtransport.SubscribeRequest) {
		hold := h.rendezvousHold(r)
		ann := h.awaitAnnouncement(r.Context(), r.Namespace(), hold)
		if ann == nil {
			// Answer promptly either way: a relay that sits silent here fails
			// the interop-runner's subscribe-error case. Section 10.2.6 wants
			// DOES_NOT_EXIST for a subscriber that did not ask to wait and
			// TIMEOUT for one whose wait ran out.
			code, reason := moqtransport.RequestErrorDoesNotExist, "unknown namespace"
			if hold > 0 {
				code, reason = moqtransport.RequestErrorTimeout, fmt.Sprintf("no publisher within %v", hold)
			}
			slog.Info("rejecting subscription to unknown namespace",
				"namespace", r.Namespace(), "track", r.Track(), "held", hold)
			if err := r.Reject(code, reason); err != nil {
				slog.Error("failed to reject subscription", "error", err)
			}
			return
		}
		rt := h.trackFor(ann, r.Namespace(), r.Track())
		select {
		case <-rt.ready:
		case <-r.Context().Done():
			rt.release()
			return
		}
		if rt.err != nil {
			rt.release()
			rejectWithUpstreamError(r, rt.err)
			return
		}
		rt.serveSubscriber(r)
	})
}

// subscribeNamespaceHandler accepts SUBSCRIBE_NAMESPACE, replays the matching
// known namespaces, and keeps the subscriber posted on later changes.
func (h *Handler) subscribeNamespaceHandler() moqtransport.SubscribeNamespaceHandler {
	return moqtransport.SubscribeNamespaceHandlerFunc(func(r *moqtransport.SubscribeNamespaceRequest) {
		announcer, err := r.Accept()
		if err != nil {
			slog.Error("failed to accept namespace subscription", "prefix", r.Prefix(), "error", err)
			return
		}
		slog.Info("namespace subscription", "prefix", r.Prefix())
		na := &nsAnnouncer{
			prefix:    r.Prefix(),
			announcer: announcer,
			announced: make(map[nsKey]bool),
		}
		h.addAnnouncer(na)
		<-r.Context().Done()
		h.removeAnnouncer(na)
	})
}

// rendezvousHold is how long a SUBSCRIBE may wait for a publisher: the
// subscriber's RENDEZVOUS_TIMEOUT capped by MaxRendezvous, since Section
// 10.2.6 lets the relay use a shorter timeout than requested.
func (h *Handler) rendezvousHold(r *moqtransport.SubscribeRequest) time.Duration {
	hold, _ := r.RendezvousTimeout()
	if hold > h.MaxRendezvous {
		hold = h.MaxRendezvous
	}
	return hold
}

// awaitAnnouncement returns the announcement covering the namespace. When
// none exists and hold is positive, it waits that long for one to arrive
// (the rendezvous a subscribe-before-announce race needs) before giving up.
func (h *Handler) awaitAnnouncement(ctx context.Context, namespace []string, hold time.Duration) *announcement {
	key := keyForNamespace(namespace)
	h.mu.Lock()
	if a, ok := h.announcements[key]; ok {
		h.mu.Unlock()
		return a
	}
	if hold <= 0 {
		h.mu.Unlock()
		return nil
	}
	ch := make(chan *announcement, 1)
	h.waiters[key] = append(h.waiters[key], ch)
	h.mu.Unlock()

	timer := time.NewTimer(hold)
	defer timer.Stop()
	select {
	case a := <-ch:
		return a
	case <-timer.C:
	case <-ctx.Done():
	}
	h.removeWaiter(key, ch)
	// register may have won a race with the timeout; the channel is buffered,
	// so a notification sent before removal is still there.
	select {
	case a := <-ch:
		return a
	default:
		return nil
	}
}

func (h *Handler) removeWaiter(key nsKey, ch chan *announcement) {
	h.mu.Lock()
	defer h.mu.Unlock()
	waiters := h.waiters[key]
	for i, w := range waiters {
		if w == ch {
			h.waiters[key] = append(waiters[:i], waiters[i+1:]...)
			break
		}
	}
	if len(h.waiters[key]) == 0 {
		delete(h.waiters, key)
	}
}

// lookupAnnouncement returns the announcement covering the namespace, or nil.
func (h *Handler) lookupAnnouncement(namespace []string) *announcement {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.announcements[keyForNamespace(namespace)]
}

// register adds the announcement to the table. A namespace announced again
// replaces the previous announcement: with per-request cleanup keyed on the
// request, the stale entry would otherwise linger if its withdrawal signal
// were lost, and last-writer-wins is the useful reading of a re-announce.
func (h *Handler) register(session *moqtransport.Session, r *moqtransport.PublishNamespaceRequest) {
	key := keyForNamespace(r.Namespace())
	h.mu.Lock()
	defer h.mu.Unlock()
	if prev, ok := h.announcements[key]; ok {
		slog.Warn("namespace announced again, replacing previous announcement",
			"namespace", r.Namespace(), "sameSession", prev.session == session)
	}
	a := &announcement{
		namespace: r.Namespace(),
		session:   session,
		request:   r,
	}
	h.announcements[key] = a
	for _, ch := range h.waiters[key] {
		ch <- a // buffered, one per waiter
	}
	delete(h.waiters, key)
}

// deregister removes the announcement created by r and reports whether it was
// still in the table. An entry replaced by a newer announcement of the same
// namespace is left alone.
func (h *Handler) deregister(r *moqtransport.PublishNamespaceRequest) bool {
	key := keyForNamespace(r.Namespace())
	h.mu.Lock()
	defer h.mu.Unlock()
	if a, ok := h.announcements[key]; ok && a.request == r {
		delete(h.announcements, key)
		return true
	}
	return false
}

// dropSession removes every announcement the session owns and withdraws them
// from the namespace subscribers.
func (h *Handler) dropSession(session *moqtransport.Session) {
	h.mu.Lock()
	var dropped [][]string
	for key, a := range h.announcements {
		if a.session == session {
			delete(h.announcements, key)
			dropped = append(dropped, a.namespace)
		}
	}
	h.mu.Unlock()
	for _, namespace := range dropped {
		h.withdrawFromSubscribers(namespace)
	}
}

// announceToSubscribers tells every matching namespace subscriber about a
// newly announced namespace.
//
// Only subscribers, never every session: Section 8.4 has a relay forward
// PUBLISH_NAMESPACE to matching subscribers, and a session that sent no
// SUBSCRIBE_NAMESPACE matches nothing. Announcing to everyone cost a
// bidirectional stream per session and namespace, and an answer from each
// peer, for something none of them asked for.
func (h *Handler) announceToSubscribers(namespace []string) {
	h.nsMu.Lock()
	defer h.nsMu.Unlock()
	h.mu.Lock()
	announcers := make([]*nsAnnouncer, 0, len(h.announcers))
	for na := range h.announcers {
		announcers = append(announcers, na)
	}
	h.mu.Unlock()

	key := keyForNamespace(namespace)
	for _, na := range announcers {
		if !na.announced[key] && prefixMatches(na.prefix, namespace) {
			if err := na.announcer.Announce(namespace); err != nil {
				slog.Debug("failed to announce to namespace subscriber", "error", err)
				continue
			}
			na.announced[key] = true
		}
	}
}

// withdrawFromSubscribers tells every namespace subscriber that saw a
// namespace that it is gone.
func (h *Handler) withdrawFromSubscribers(namespace []string) {
	h.nsMu.Lock()
	defer h.nsMu.Unlock()
	key := keyForNamespace(namespace)
	h.mu.Lock()
	announcers := make([]*nsAnnouncer, 0, len(h.announcers))
	for na := range h.announcers {
		announcers = append(announcers, na)
	}
	h.mu.Unlock()

	for _, na := range announcers {
		if na.announced[key] {
			if err := na.announcer.Done(namespace); err != nil {
				slog.Debug("failed to notify namespace subscriber", "error", err)
			}
			delete(na.announced, key)
		}
	}
}

// discoverNamespaces asks the peer for every namespace it has and keeps the
// relay's table in step with the answer.
//
// This is the same request a downstream subscriber makes of this relay, made
// upstream: a namespace the relay never hears about is one it cannot route a
// SUBSCRIBE to, however correctly the subscriber asks for it.
func (h *Handler) discoverNamespaces(session *moqtransport.Session) {
	sub, err := session.SubscribeNamespace(session.Context(), nil)
	if err != nil {
		// A publisher with no namespace subscription to offer answers
		// NOT_SUPPORTED, as mlmpub does, and announces what it has unprompted.
		// Nothing to recover from and nothing lost.
		slog.Info("upstream took no namespace subscription", "error", err)
		return
	}
	slog.Info("subscribed to all upstream namespaces")
	defer func() {
		if err := sub.Close(); err != nil {
			slog.Debug("failed to close namespace subscription", "error", err)
		}
	}()
	for {
		select {
		case <-session.Context().Done():
			return
		case ev := <-sub.Namespaces():
			if ev.Available {
				if h.registerDiscovered(session, ev.Namespace) {
					slog.Info("discovered upstream namespace", "namespace", ev.Namespace)
					h.announceToSubscribers(ev.Namespace)
				}
				continue
			}
			if h.deregisterDiscovered(session, ev.Namespace) {
				slog.Info("upstream namespace withdrawn", "namespace", ev.Namespace)
				h.withdrawFromSubscribers(ev.Namespace)
			}
		}
	}
}

// registerDiscovered adds a namespace learned from a namespace subscription
// rather than from a PUBLISH_NAMESPACE, and reports whether it was new. An
// entry a peer announced directly carries the request that withdraws it, so it
// wins over a discovered duplicate of the same namespace.
func (h *Handler) registerDiscovered(session *moqtransport.Session, namespace []string) bool {
	key := keyForNamespace(namespace)
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.announcements[key]; ok {
		return false
	}
	a := &announcement{namespace: namespace, session: session}
	h.announcements[key] = a
	for _, ch := range h.waiters[key] {
		ch <- a // buffered, one per waiter
	}
	delete(h.waiters, key)
	return true
}

// deregisterDiscovered removes a discovered namespace the upstream says is
// gone, and reports whether it was still there. An entry backed by a
// PUBLISH_NAMESPACE is left to its own withdrawal path.
func (h *Handler) deregisterDiscovered(session *moqtransport.Session, namespace []string) bool {
	key := keyForNamespace(namespace)
	h.mu.Lock()
	defer h.mu.Unlock()
	if a, ok := h.announcements[key]; ok && a.request == nil && a.session == session {
		delete(h.announcements, key)
		return true
	}
	return false
}

// addAnnouncer registers a namespace subscriber and replays the matching
// known namespaces to it.
func (h *Handler) addAnnouncer(na *nsAnnouncer) {
	h.nsMu.Lock()
	defer h.nsMu.Unlock()
	h.mu.Lock()
	h.announcers[na] = struct{}{}
	matches := make([][]string, 0)
	for _, a := range h.announcements {
		if prefixMatches(na.prefix, a.namespace) {
			matches = append(matches, a.namespace)
		}
	}
	h.mu.Unlock()
	for _, namespace := range matches {
		if err := na.announcer.Announce(namespace); err != nil {
			slog.Debug("failed to announce to namespace subscriber", "error", err)
			continue
		}
		na.announced[keyForNamespace(namespace)] = true
	}
}

func (h *Handler) removeAnnouncer(na *nsAnnouncer) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.announcers, na)
}
