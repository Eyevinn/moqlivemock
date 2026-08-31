package relay

import (
	"errors"
	"log/slog"
	"slices"

	"github.com/Eyevinn/moqtransport"
)

// fetchHandler answers downstream FETCHes. A range fully covered by an
// active track's group cache is served from it -- this makes recent media
// groups fetchable through the relay even when the origin only serves the
// catalog via FETCH. Anything else is proxied upstream as a standalone FETCH
// with the (already resolved) range: re-resolving a joining FETCH relatively
// against the upstream would race its live edge.
func (h *Handler) fetchHandler() moqtransport.FetchHandler {
	return moqtransport.FetchHandlerFunc(func(r *moqtransport.FetchRequest) {
		ann := h.lookupAnnouncement(r.Namespace())
		if ann == nil {
			slog.Info("rejecting fetch for unknown namespace",
				"namespace", r.Namespace(), "track", r.Track())
			if err := r.Reject(moqtransport.RequestErrorDoesNotExist, "unknown namespace"); err != nil {
				slog.Error("failed to reject fetch", "error", err)
			}
			return
		}
		if h.serveFetchFromCache(r) {
			return
		}
		h.proxyFetch(r, ann.session)
	})
}

// serveFetchFromCache answers the FETCH from an active track's cache and
// reports whether it did. Only a fully covered range qualifies.
func (h *Handler) serveFetchFromCache(r *moqtransport.FetchRequest) bool {
	key := trackKey{ns: keyForNamespace(r.Namespace()), track: r.Track()}
	h.mu.Lock()
	rt := h.tracks[key]
	h.mu.Unlock()
	if rt == nil {
		return false
	}
	select {
	case <-rt.ready:
	default:
		return false // upstream subscription not answered yet
	}
	start, end := r.Range()
	rt.mu.Lock()
	objects, covered := rt.cache.collectRange(start, end)
	rt.mu.Unlock()
	if !covered {
		return false
	}

	if r.GroupOrder() == moqtransport.GroupOrderDescending {
		reorderGroupsDescending(objects)
	}
	response, err := r.Accept()
	if err != nil {
		slog.Error("failed to accept fetch", "error", err)
		return true
	}
	for _, obj := range objects {
		if err := response.WriteObject(*obj); err != nil {
			slog.Error("failed to write cached object to fetch", "error", err)
			response.Reset(moqtransport.StreamErrorInternal)
			return true
		}
	}
	slog.Info("served fetch from cache", "namespace", r.Namespace(), "track", r.Track(),
		"objects", len(objects), "start", start, "end", end)
	if err := response.Close(); err != nil {
		slog.Debug("failed to close fetch response", "error", err)
	}
	return true
}

// reorderGroupsDescending reverses the group order of an ascending-sorted
// object slice while keeping ascending object IDs within each group.
func reorderGroupsDescending(objects []*moqtransport.Object) {
	byGroup := make(map[uint64][]*moqtransport.Object)
	var groups []uint64
	for _, obj := range objects {
		if _, ok := byGroup[obj.GroupID]; !ok {
			groups = append(groups, obj.GroupID)
		}
		byGroup[obj.GroupID] = append(byGroup[obj.GroupID], obj)
	}
	i := 0
	for _, g := range slices.Backward(groups) {
		for _, obj := range byGroup[g] {
			objects[i] = obj
			i++
		}
	}
}

// proxyFetch forwards the FETCH to the announcing session and pumps the
// response through -- the most lossless path in moqtransport: group,
// subgroup, object, priority, properties, the datagram bit and End of Range
// markers all survive.
func (h *Handler) proxyFetch(r *moqtransport.FetchRequest, upstream *moqtransport.Session) {
	start, end := r.Range()
	fs, err := upstream.Fetch(r.Context(), r.Namespace(), r.Track(), start, end,
		moqtransport.WithFetchGroupOrder(r.GroupOrder()))
	if err != nil {
		code := moqtransport.RequestErrorDoesNotExist
		reason := "upstream fetch failed"
		var reqErr *moqtransport.RequestError
		if errors.As(err, &reqErr) {
			code, reason = reqErr.Code, reqErr.Reason
		}
		slog.Info("upstream rejected fetch", "namespace", r.Namespace(), "track", r.Track(),
			"code", code, "reason", reason)
		if err := r.Reject(code, reason); err != nil {
			slog.Error("failed to reject fetch", "error", err)
		}
		return
	}
	defer func() {
		if err := fs.Close(); err != nil {
			slog.Debug("failed to close upstream fetch", "error", err)
		}
	}()

	opts := []moqtransport.FetchOkOption{
		moqtransport.WithFetchEndLocation(fs.EndLocation()),
	}
	if fs.EndOfTrack() {
		opts = append(opts, moqtransport.WithEndOfTrack())
	}
	response, err := r.Accept(opts...)
	if err != nil {
		slog.Error("failed to accept fetch", "error", err)
		return
	}
	slog.Info("proxying fetch", "namespace", r.Namespace(), "track", r.Track(),
		"start", start, "end", end)

	for {
		fo, err := fs.ReadObject(r.Context())
		if errors.Is(err, moqtransport.ErrFetchComplete) {
			if err := response.Close(); err != nil {
				slog.Debug("failed to close fetch response", "error", err)
			}
			return
		}
		if err != nil {
			// The upstream broke off or the subscriber cancelled; either way
			// the response is incomplete.
			response.Reset(moqtransport.StreamErrorInternal)
			return
		}
		if fo.EndOfRange != moqtransport.EndOfRange(0) {
			err = response.WriteEndOfRange(fo.EndOfRange,
				moqtransport.Location{Group: fo.GroupID, Object: fo.ObjectID})
		} else {
			err = response.WriteObject(fo.Object)
		}
		if err != nil {
			slog.Info("failed to write fetched object downstream", "error", err)
			return
		}
	}
}
