// Package relay implements a MoQ Transport relay. It accepts sessions from
// publishers and subscribers alike, keeps a table of announced namespaces,
// and forwards a subscription to the session that announced its namespace:
// any PUBLISH_NAMESPACE is accepted and registered, withdrawal (the request
// stream ending) and session death deregister it, a SUBSCRIBE for an unknown
// namespace is rejected promptly (or after PendingWait, when configured), and
// a SUBSCRIBE for an announced one is subscribed upstream and its objects
// relayed. Fanout -- several subscribers sharing one upstream subscription
// through a cache -- is the next phase; today each subscription is forwarded
// individually.
package relay

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/Eyevinn/moqtransport"
	"github.com/mengelbart/qlog"
)

// Handler handles MoQ relay sessions. One Handler serves every connection of
// a relay instance; the announcement table is what its sessions share.
type Handler struct {
	// Logfh receives the qlog for every session.
	Logfh io.Writer
	// PendingWait is how long a SUBSCRIBE for an unannounced namespace waits
	// for an announcement before it is rejected. Zero rejects immediately,
	// which is also a valid answer to a subscribe-before-announce race.
	PendingWait time.Duration

	mu            sync.Mutex
	announcements map[nsKey]*announcement
	waiters       map[nsKey][]chan *announcement
}

// NewHandler creates a relay session handler writing its qlog to logfh.
func NewHandler(logfh io.Writer) *Handler {
	return &Handler{
		Logfh:         logfh,
		announcements: make(map[nsKey]*announcement),
		waiters:       make(map[nsKey][]chan *announcement),
	}
}

// nsKey is a namespace tuple flattened to a comparable map key. The separator
// cannot appear in a tuple element, which is a UTF-8 string.
type nsKey string

func keyForNamespace(namespace []string) nsKey {
	return nsKey(strings.Join(namespace, "\x1f"))
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
		Qlogger: qlog.NewQLOGHandler(h.Logfh, "MoQ QLOG", "MoQ QLOG",
			conn.Perspective().String(), moqtransport.QlogSchema),
	}
	session.PublishNamespaceHandler = h.publishNamespaceHandler(session)
	session.SubscribeHandler = h.subscribeHandler()

	slog.Info("starting MoQ session", "perspective", conn.Perspective())
	if err := session.Run(ctx, conn); err != nil {
		slog.Error("MoQ session initialization failed", "error", err)
		if err := conn.CloseWithError(0, "session initialization error"); err != nil {
			slog.Error("failed to close connection", "error", err)
		}
		return
	}
	slog.Info("MoQ session established", "version", session.Version())

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
		if err := r.Accept(); err != nil {
			slog.Error("failed to accept announcement", "namespace", r.Namespace(), "error", err)
			return
		}
		h.register(session, r)
		slog.Info("registered announcement", "namespace", r.Namespace())

		<-r.Context().Done()
		if h.deregister(r) {
			slog.Info("announcement withdrawn", "namespace", r.Namespace())
		}
	})
}

func (h *Handler) subscribeHandler() moqtransport.SubscribeHandler {
	return moqtransport.SubscribeHandlerFunc(func(r *moqtransport.SubscribeRequest) {
		ann := h.awaitAnnouncement(r.Context(), r.Namespace())
		if ann == nil {
			// A prompt REQUEST_ERROR: a relay that sits silent here fails the
			// interop-runner's subscribe-error case.
			slog.Info("rejecting subscription to unknown namespace",
				"namespace", r.Namespace(), "track", r.Track())
			if err := r.Reject(moqtransport.RequestErrorDoesNotExist, "unknown namespace"); err != nil {
				slog.Error("failed to reject subscription", "error", err)
			}
			return
		}
		forward(r, ann.session)
	})
}

// awaitAnnouncement returns the announcement covering the namespace. When
// none exists and PendingWait is positive, it waits that long for one to
// arrive (the rendezvous a subscribe-before-announce race needs) before
// giving up.
func (h *Handler) awaitAnnouncement(ctx context.Context, namespace []string) *announcement {
	key := keyForNamespace(namespace)
	h.mu.Lock()
	if a, ok := h.announcements[key]; ok {
		h.mu.Unlock()
		return a
	}
	if h.PendingWait <= 0 {
		h.mu.Unlock()
		return nil
	}
	ch := make(chan *announcement, 1)
	h.waiters[key] = append(h.waiters[key], ch)
	h.mu.Unlock()

	timer := time.NewTimer(h.PendingWait)
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

// dropSession removes every announcement the session owns.
func (h *Handler) dropSession(session *moqtransport.Session) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for key, a := range h.announcements {
		if a.session == session {
			delete(h.announcements, key)
		}
	}
}
