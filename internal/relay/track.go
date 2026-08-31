package relay

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/Eyevinn/moqtransport"
)

// errNoSubscribers is the cancel cause of a linger teardown: the last
// subscriber left and none arrived within the linger.
var errNoSubscribers = errors.New("no subscribers left")

// trackKey identifies a track across sessions.
type trackKey struct {
	ns    nsKey
	track string
}

// publishEnd is a PUBLISH_DONE to propagate downstream.
type publishEnd struct {
	code   moqtransport.PublishDoneCode
	reason string
}

// relayTrack is the shared forwarding state for one (namespace, track): one
// upstream subscription fanned out to any number of downstream subscribers
// through a small cache of recent groups.
type relayTrack struct {
	h         *Handler
	key       trackKey
	namespace []string
	track     string
	upstream  *moqtransport.Session
	ctx       context.Context
	cancel    context.CancelCauseFunc

	// ready is closed once the upstream SUBSCRIBE is answered; err and
	// remote are set before that and immutable afterwards.
	ready  chan struct{}
	err    error
	remote *moqtransport.RemoteTrack

	mu          sync.Mutex
	subs        map[*subscriber]struct{}
	pending     int // reservations from acquire() not yet attached or released
	cache       *groupCache
	largest     moqtransport.Location
	haveLargest bool
	lingerTimer *time.Timer
	tornDown    bool
}

func newRelayTrack(h *Handler, key trackKey, ann *announcement) *relayTrack {
	ctx, cancel := context.WithCancelCause(ann.session.Context())
	return &relayTrack{
		h:         h,
		key:       key,
		namespace: ann.namespace,
		track:     key.track,
		upstream:  ann.session,
		ctx:       ctx,
		cancel:    cancel,
		ready:     make(chan struct{}),
		subs:      make(map[*subscriber]struct{}),
		cache:     newGroupCache(h.CacheGroups),
	}
}

// trackFor returns the relay track for the subscription, creating it (and
// with it the single upstream subscription) when it is the first. The
// returned track is acquired: the caller must attach a subscriber or release.
func (h *Handler) trackFor(ann *announcement, namespace []string, track string) *relayTrack {
	key := trackKey{ns: keyForNamespace(namespace), track: track}
	for {
		h.mu.Lock()
		rt, ok := h.tracks[key]
		if !ok {
			rt = newRelayTrack(h, key, ann)
			h.tracks[key] = rt
			h.mu.Unlock()
			go rt.run()
			if rt.acquire() {
				return rt
			}
			continue
		}
		h.mu.Unlock()
		if rt.acquire() {
			return rt
		}
		// Torn down between lookup and acquire; a fresh track replaces it.
	}
}

func (h *Handler) removeTrack(key trackKey, rt *relayTrack) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.tracks[key] == rt {
		delete(h.tracks, key)
	}
}

// acquire reserves the track for a subscriber about to attach, stopping a
// pending linger teardown. It fails when the track is already torn down.
func (rt *relayTrack) acquire() bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.tornDown {
		return false
	}
	if rt.lingerTimer != nil {
		rt.lingerTimer.Stop()
		rt.lingerTimer = nil
	}
	rt.pending++
	return true
}

// release undoes acquire without attaching (the accept failed or the
// upstream answer was an error).
func (rt *relayTrack) release() {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.pending--
	rt.maybeLingerLocked()
}

// attach turns a reservation into a live subscriber and returns the cached
// objects of the newest group, the group-aligned join point. Registration
// and snapshot happen under one lock with dispatch, so the backlog and the
// queue never overlap and never leave a gap.
func (rt *relayTrack) attach(s *subscriber) []*moqtransport.Object {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.pending--
	rt.subs[s] = struct{}{}
	return rt.cache.newestGroupObjects()
}

func (rt *relayTrack) detach(s *subscriber) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	delete(rt.subs, s)
	rt.maybeLingerLocked()
}

// maybeLingerLocked starts the linger teardown timer when nothing uses the
// track any more. Called with rt.mu held.
func (rt *relayTrack) maybeLingerLocked() {
	if rt.tornDown || rt.pending > 0 || len(rt.subs) > 0 || rt.lingerTimer != nil {
		return
	}
	rt.lingerTimer = time.AfterFunc(rt.h.Linger, rt.lingerFire)
}

func (rt *relayTrack) lingerFire() {
	rt.mu.Lock()
	if rt.tornDown || rt.pending > 0 || len(rt.subs) > 0 {
		rt.mu.Unlock()
		return
	}
	rt.tornDown = true
	rt.mu.Unlock()
	slog.Info("last subscriber left, unsubscribing upstream",
		"namespace", rt.namespace, "track", rt.track)
	rt.cancel(errNoSubscribers)
	rt.h.removeTrack(rt.key, rt)
}

// run subscribes upstream and fans every received object out to the
// subscribers and into the cache. It is the only writer of rt.remote/rt.err.
func (rt *relayTrack) run() {
	remote, err := rt.upstream.Subscribe(rt.ctx, rt.namespace, rt.track)
	if err != nil {
		rt.err = err
		rt.mu.Lock()
		rt.tornDown = true
		rt.mu.Unlock()
		close(rt.ready)
		rt.h.removeTrack(rt.key, rt)
		return
	}
	rt.remote = remote
	if largest, ok := remote.LargestObject(); ok {
		rt.mu.Lock()
		rt.largest, rt.haveLargest = largest, true
		rt.mu.Unlock()
	}
	close(rt.ready)
	slog.Info("subscribed upstream", "namespace", rt.namespace, "track", rt.track)
	defer func() {
		if err := remote.Close(); err != nil {
			slog.Debug("failed to close upstream subscription", "error", err)
		}
	}()

	for {
		obj, err := remote.ReadObject(rt.ctx)
		if err != nil {
			rt.finish()
			return
		}
		rt.dispatch(obj)
	}
}

// dispatch caches the object, advances the largest location, and enqueues it
// to every subscriber. Enqueueing never blocks: a subscriber whose queue is
// full goes "behind" and is resynced at a later group boundary.
func (rt *relayTrack) dispatch(obj *moqtransport.Object) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	rt.cache.add(obj)
	loc := moqtransport.Location{Group: obj.GroupID, Object: obj.ObjectID}
	if !rt.haveLargest || locationLess(rt.largest, loc) {
		rt.largest, rt.haveLargest = loc, true
	}
	for s := range rt.subs {
		s.enqueue(obj)
	}
}

// finish ends the track: it propagates the upstream PUBLISH_DONE (or the
// loss of the upstream) to every subscriber and removes the track. A linger
// teardown arrives here too, with no subscribers left to notify.
func (rt *relayTrack) finish() {
	end := publishEnd{code: moqtransport.PublishDoneInternalError, reason: "upstream ended"}
	if done, ok := rt.remote.PublishDone(); ok {
		end.code, end.reason = done.Code, done.Reason
	}
	rt.mu.Lock()
	rt.tornDown = true
	subs := make([]*subscriber, 0, len(rt.subs))
	for s := range rt.subs {
		subs = append(subs, s)
	}
	rt.mu.Unlock()

	if context.Cause(rt.ctx) != errNoSubscribers && len(subs) > 0 {
		slog.Info("upstream subscription ended", "namespace", rt.namespace,
			"track", rt.track, "code", end.code, "reason", end.reason)
		for _, s := range subs {
			s.end(end)
		}
	}
	rt.h.removeTrack(rt.key, rt)
	rt.cancel(nil)
}

// snapshotLargest returns the largest location seen, for SUBSCRIBE_OK.
func (rt *relayTrack) snapshotLargest() (moqtransport.Location, bool) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	return rt.largest, rt.haveLargest
}

// locationLess reports whether a precedes b in (group, object) order.
func locationLess(a, b moqtransport.Location) bool {
	if a.Group != b.Group {
		return a.Group < b.Group
	}
	return a.Object < b.Object
}

// groupCache keeps the objects of the last maxGroups groups of a track, in
// arrival order per group. A group counts as complete once a newer group has
// started -- the same inference the forwarding uses, since moqtransport does
// not surface subgroup ends (Eyevinn/moqtransport#19).
type groupCache struct {
	maxGroups int
	groups    map[uint64]*cachedGroup
	order     []uint64 // cached group IDs, ascending
}

type cachedGroup struct {
	objects   []*moqtransport.Object
	maxObject uint64
	complete  bool
}

func newGroupCache(maxGroups int) *groupCache {
	return &groupCache{
		maxGroups: maxGroups,
		groups:    make(map[uint64]*cachedGroup),
	}
}

func (c *groupCache) add(obj *moqtransport.Object) {
	cg, ok := c.groups[obj.GroupID]
	if !ok {
		cg = &cachedGroup{}
		c.groups[obj.GroupID] = cg
		c.order = append(c.order, obj.GroupID)
		slices.Sort(c.order)
		// A newer group starting is the signal that older ones are complete.
		for id, g := range c.groups {
			if id < c.order[len(c.order)-1] {
				g.complete = true
			}
		}
		for len(c.order) > c.maxGroups {
			delete(c.groups, c.order[0])
			c.order = c.order[1:]
		}
	}
	cg.objects = append(cg.objects, obj)
	if obj.ObjectID > cg.maxObject {
		cg.maxObject = obj.ObjectID
	}
}

// newestGroupObjects returns a copy of the newest cached group's objects:
// the group-aligned join point for a new subscriber.
func (c *groupCache) newestGroupObjects() []*moqtransport.Object {
	if len(c.order) == 0 {
		return nil
	}
	objects := c.groups[c.order[len(c.order)-1]].objects
	out := make([]*moqtransport.Object, len(objects))
	copy(out, objects)
	return out
}

// collectRange returns the cached objects of a FETCH range in ascending
// (group, object) order, and whether the cache fully covers the range. Only
// a fully covered range is served from cache; anything else is proxied.
func (c *groupCache) collectRange(start, end moqtransport.Location) ([]*moqtransport.Object, bool) {
	if len(c.order) == 0 || end.Group < start.Group {
		return nil, false
	}
	// More groups than the cache can hold cannot be covered; this also
	// bounds the loop against absurd ranges.
	if end.Group-start.Group >= uint64(c.maxGroups) {
		return nil, false
	}
	newest := c.order[len(c.order)-1]
	for g := start.Group; g <= end.Group; g++ {
		cg, ok := c.groups[g]
		if !ok {
			return nil, false
		}
		if cg.complete {
			continue
		}
		// The newest group is still growing: it covers the range only when
		// the range ends inside it, below what has arrived.
		if g != newest || g != end.Group || end.Object == 0 || end.Object-1 > cg.maxObject {
			return nil, false
		}
	}
	var out []*moqtransport.Object
	for _, g := range c.order {
		if g < start.Group || g > end.Group {
			continue
		}
		for _, obj := range c.groups[g].objects {
			loc := moqtransport.Location{Group: obj.GroupID, Object: obj.ObjectID}
			if locationInFetchRange(loc, start, end) {
				out = append(out, obj)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].GroupID != out[j].GroupID {
			return out[i].GroupID < out[j].GroupID
		}
		return out[i].ObjectID < out[j].ObjectID
	})
	return out, true
}

// locationInFetchRange reports whether loc falls within a FETCH's requested
// [start, end) range. An EndObject of 0 means "to the end of EndGroup" per
// the FETCH semantics (draft-ietf-moq-transport §9.16.3).
func locationInFetchRange(loc, start, end moqtransport.Location) bool {
	if locationLess(loc, start) {
		return false
	}
	if end.Object == 0 {
		return loc.Group <= end.Group
	}
	return locationLess(loc, end)
}
