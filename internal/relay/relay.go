// Package relay implements a MoQ Transport relay. It accepts sessions from
// publishers and subscribers alike, keeps a table of announced namespaces,
// and forwards subscriptions to the session that announced the namespace:
// one upstream subscription per (namespace, track) is fanned out to any
// number of downstream subscribers through a small cache of recent groups,
// which also gives a late subscriber a group-aligned start. FETCHes are
// served from that cache when it covers the range and proxied upstream
// otherwise. Announcements are propagated to every other session and to
// SUBSCRIBE_NAMESPACE subscribers.
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
	sessions      map[*moqtransport.Session]*sessionState
	announcers    map[*nsAnnouncer]struct{}

	// nsMu serializes namespace fan-out (replays to new sessions and
	// announcers, and live announce/withdraw notifications) so nobody sees a
	// duplicate or missed announcement. Fan-out writes wait for the peer's
	// answer, so a stalled peer slows announcement propagation -- never
	// object forwarding.
	nsMu sync.Mutex
}

// sessionState is what the relay tracks per connected session.
type sessionState struct {
	session *moqtransport.Session
	// pubs are this relay's own announcements toward the session, one per
	// namespace some other session announced.
	pubs map[nsKey]*moqtransport.NamespacePublication
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
		sessions:        make(map[*moqtransport.Session]*sessionState),
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
	request   *moqtransport.PublishNamespaceRequest
}

// Handle runs a MoQ session on the given connection and blocks until the
// session or the context ends. Announcements made by the session are dropped
// from the table when it ends.
func (h *Handler) Handle(ctx context.Context, conn moqtransport.Connection) {
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
	h.addSession(session)

	select {
	case <-ctx.Done():
	case <-session.Context().Done():
		slog.Info("MoQ session ended", "reason", context.Cause(session.Context()))
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
		h.announceToAll(r.Namespace(), session)

		<-r.Context().Done()
		if h.deregister(r) {
			slog.Info("announcement withdrawn", "namespace", r.Namespace())
			h.withdrawFromAll(r.Namespace())
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

// dropSession removes every announcement the session owns, withdraws them
// from the other sessions and announcers, and forgets the session.
func (h *Handler) dropSession(session *moqtransport.Session) {
	h.mu.Lock()
	var dropped [][]string
	for key, a := range h.announcements {
		if a.session == session {
			delete(h.announcements, key)
			dropped = append(dropped, a.namespace)
		}
	}
	delete(h.sessions, session)
	h.mu.Unlock()
	for _, namespace := range dropped {
		h.withdrawFromAll(namespace)
	}
}

// addSession registers a newly established session and replays the known
// namespaces to it, so clients that wait for announcements (warp-player)
// work behind the relay. Peers that take no announcements (a bare publisher)
// reject them, which is harmless.
func (h *Handler) addSession(session *moqtransport.Session) {
	h.nsMu.Lock()
	defer h.nsMu.Unlock()
	state := &sessionState{
		session: session,
		pubs:    make(map[nsKey]*moqtransport.NamespacePublication),
	}
	h.mu.Lock()
	h.sessions[session] = state
	existing := make([]*announcement, 0, len(h.announcements))
	for _, a := range h.announcements {
		if a.session != session {
			existing = append(existing, a)
		}
	}
	h.mu.Unlock()
	for _, a := range existing {
		h.announceLocked(state, a.namespace)
	}
}

// announceToAll tells every other session and every matching announcer about
// a newly announced namespace.
func (h *Handler) announceToAll(namespace []string, from *moqtransport.Session) {
	h.nsMu.Lock()
	defer h.nsMu.Unlock()
	h.mu.Lock()
	states := make([]*sessionState, 0, len(h.sessions))
	for session, state := range h.sessions {
		if session != from {
			states = append(states, state)
		}
	}
	announcers := make([]*nsAnnouncer, 0, len(h.announcers))
	for na := range h.announcers {
		announcers = append(announcers, na)
	}
	h.mu.Unlock()

	for _, state := range states {
		h.announceLocked(state, namespace)
	}
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

// announceLocked announces one namespace on one session and keeps the handle
// for withdrawal. Called with nsMu held.
func (h *Handler) announceLocked(state *sessionState, namespace []string) {
	key := keyForNamespace(namespace)
	if _, ok := state.pubs[key]; ok {
		return
	}
	publication, err := state.session.PublishNamespace(state.session.Context(), namespace)
	if err != nil {
		// A peer that takes no announcements answers every one this way.
		slog.Debug("session did not take announcement", "namespace", namespace, "error", err)
		return
	}
	state.pubs[key] = publication
}

// withdrawFromAll closes the relay's announcements of a withdrawn namespace
// toward every session and notifies matching announcers.
func (h *Handler) withdrawFromAll(namespace []string) {
	h.nsMu.Lock()
	defer h.nsMu.Unlock()
	key := keyForNamespace(namespace)
	h.mu.Lock()
	states := make([]*sessionState, 0, len(h.sessions))
	for _, state := range h.sessions {
		states = append(states, state)
	}
	announcers := make([]*nsAnnouncer, 0, len(h.announcers))
	for na := range h.announcers {
		announcers = append(announcers, na)
	}
	h.mu.Unlock()

	for _, state := range states {
		if publication, ok := state.pubs[key]; ok {
			if err := publication.Close(); err != nil {
				slog.Debug("failed to withdraw announcement", "error", err)
			}
			delete(state.pubs, key)
		}
	}
	for _, na := range announcers {
		if na.announced[key] {
			if err := na.announcer.Done(namespace); err != nil {
				slog.Debug("failed to notify namespace subscriber", "error", err)
			}
			delete(na.announced, key)
		}
	}
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
