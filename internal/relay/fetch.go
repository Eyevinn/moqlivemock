package relay

import (
	"context"
	"errors"
	"log/slog"
	"slices"

	"github.com/Eyevinn/moqtransport"
)

// fetchHandler answers downstream FETCHes. A range fully covered by an
// active track's group cache is served from it -- this makes recent media
// groups fetchable through the relay even when the origin only serves the
// catalog via FETCH. Anything else is proxied upstream as a standalone FETCH
// with the request's location filter. A draft-18 joining FETCH arrives with
// its range already resolved against the downstream subscription, so it goes
// up as that explicit range: re-resolving it relatively against the upstream
// would race its live edge.
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

// fillHandler serves the fills draft-22 subscribers ask for along with a
// SUBSCRIBE. The fill range has already been resolved against the Largest
// Object the relay reported, so it is explicit: the cache serves it when it
// covers all of it, and otherwise the relay FETCHes that range upstream and
// passes the response through. A fill that cannot be served is reset, which
// leaves the subscription alone.
func (h *Handler) fillHandler() moqtransport.FillHandler {
	return moqtransport.FillHandlerFunc(func(r *moqtransport.FillRequest) {
		rng := r.Range()
		if rt := h.activeTrack(r.Namespace(), r.Track()); rt != nil {
			if objects, covered := rt.collectRange(rng); covered {
				response, err := r.Accept()
				if err != nil {
					slog.Error("failed to open fill", "error", err)
					return
				}
				if writeCached(response, objects, r.GroupOrder()) {
					slog.Info("served fill from cache", "namespace", r.Namespace(),
						"track", r.Track(), "objects", len(objects), "range", rng)
				}
				return
			}
		}
		refuse := func(reason string, args ...any) {
			slog.Info("refusing fill: "+reason,
				append([]any{"namespace", r.Namespace(), "track", r.Track(), "range", rng}, args...)...)
			if err := r.Reject(moqtransport.StreamErrorInternal); err != nil {
				slog.Debug("failed to refuse fill", "error", err)
			}
		}
		ann := h.lookupAnnouncement(r.Namespace())
		if ann == nil {
			refuse("no publisher")
			return
		}
		ctx, cancel := h.upstreamContext(r.Context())
		fs, err := ann.session.Fetch(ctx, r.Namespace(), r.Track(),
			moqtransport.AbsoluteRange(rng.Start, rng.End),
			moqtransport.WithFetchGroupOrder(r.GroupOrder()))
		if err != nil {
			err = h.upstreamError(ctx, err)
		}
		cancel()
		if err != nil {
			refuse("upstream fetch failed", "error", err)
			return
		}
		defer func() {
			if err := fs.Close(); err != nil {
				slog.Debug("failed to close upstream fetch", "error", err)
			}
		}()
		response, err := r.Accept()
		if err != nil {
			slog.Error("failed to open fill", "error", err)
			return
		}
		slog.Info("filling from upstream", "namespace", r.Namespace(), "track", r.Track(),
			"range", rng)
		pumpFetch(r.Context(), fs, response)
	})
}

// activeTrack returns the relay's track for (namespace, track) once its
// upstream subscription has been answered, or nil.
func (h *Handler) activeTrack(namespace []string, track string) *relayTrack {
	key := trackKey{ns: keyForNamespace(namespace), track: track}
	h.mu.Lock()
	rt := h.tracks[key]
	h.mu.Unlock()
	if rt == nil {
		return nil
	}
	select {
	case <-rt.ready:
		return rt
	default:
		return nil // upstream subscription not answered yet
	}
}

// serveFetchFromCache answers the FETCH from an active track's cache and
// reports whether it did. The range is resolved against the largest location
// the relay has seen, and only a fully covered range qualifies.
func (h *Handler) serveFetchFromCache(r *moqtransport.FetchRequest) bool {
	rt := h.activeTrack(r.Namespace(), r.Track())
	if rt == nil {
		return false
	}
	rng, ok := r.Range(rt.snapshotLargest())
	if !ok {
		return false // empty here; the upstream may have more
	}
	objects, covered := rt.collectRange(rng)
	if !covered {
		return false
	}
	response, err := r.Accept()
	if err != nil {
		slog.Error("failed to accept fetch", "error", err)
		return true
	}
	if writeCached(response, objects, r.GroupOrder()) {
		slog.Info("served fetch from cache", "namespace", r.Namespace(), "track", r.Track(),
			"objects", len(objects), "range", rng)
	}
	return true
}

// writeCached writes cached objects, in ascending order, to a FETCH or fill
// response in the requested group order and closes it. It reports whether it
// got that far.
func writeCached(response *moqtransport.FetchResponse, objects []*moqtransport.Object,
	order moqtransport.GroupOrder) bool {
	if order == moqtransport.GroupOrderDescending {
		reorderGroupsDescending(objects)
	}
	for _, obj := range objects {
		if err := response.WriteObject(*obj); err != nil {
			slog.Error("failed to write cached object", "error", err)
			response.Reset(moqtransport.StreamErrorInternal)
			return false
		}
	}
	if err := response.Close(); err != nil {
		slog.Debug("failed to close response", "error", err)
		return false
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
// response through.
func (h *Handler) proxyFetch(r *moqtransport.FetchRequest, upstream *moqtransport.Session) {
	ctx, cancel := h.upstreamContext(r.Context())
	fs, err := upstream.Fetch(ctx, r.Namespace(), r.Track(), r.Filter(),
		moqtransport.WithFetchGroupOrder(r.GroupOrder()))
	if err != nil {
		err = h.upstreamError(ctx, err)
	}
	cancel()
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
		"filter", r.Filter())
	pumpFetch(r.Context(), fs, response)
}

// pumpFetch copies an upstream FETCH response into a downstream FETCH or
// fill response -- the most lossless path in moqtransport: group, subgroup,
// object, priority, properties, the datagram bit and End of Range markers all
// survive. The downstream ends the way the upstream did: closed when it
// completed, reset when it broke off or the downstream went away.
func pumpFetch(ctx context.Context, fs *moqtransport.FetchStream, response *moqtransport.FetchResponse) {
	for {
		fo, err := fs.ReadObject(ctx)
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
