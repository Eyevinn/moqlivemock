package relay

import (
	"errors"
	"log/slog"

	"github.com/Eyevinn/moqtransport"
)

// sgKey identifies a subgroup within a track.
type sgKey struct {
	group    uint64
	subgroup uint64
}

// fwdItem is one entry on a subscriber's queue.
type fwdItem struct {
	obj *moqtransport.Object
	// resync says the subscriber fell behind and objects were dropped: the
	// writer resets its open subgroups before continuing at a group boundary.
	resync bool
}

// subscriber is one downstream subscription being fed from a relayTrack. The
// dispatch loop enqueues (never blocking) and the subscriber's own writer
// goroutine drains; behind/behindGroup are guarded by the track's mutex.
type subscriber struct {
	sub   *moqtransport.Subscription
	queue chan fwdItem
	done  chan publishEnd // buffered 1: the end always gets through

	// Guarded by relayTrack.mu, mutated only by dispatch.
	behind      bool
	behindGroup uint64

	// Writer-goroutine state.
	open        map[sgKey]*moqtransport.Subgroup
	newestGroup uint64
	haveGroup   bool
}

func newSubscriber(sub *moqtransport.Subscription, queueLen int) *subscriber {
	return &subscriber{
		sub:   sub,
		queue: make(chan fwdItem, queueLen),
		done:  make(chan publishEnd, 1),
		open:  make(map[sgKey]*moqtransport.Subgroup),
	}
}

// enqueue hands an object to the subscriber without ever blocking the
// dispatch loop. A full queue puts the subscriber "behind": objects are
// dropped until a later group starts (its first object, ID 0) and the queue
// has room again, at which point a resync item tells the writer to reset its
// open subgroups. Called with the track's mutex held.
func (s *subscriber) enqueue(obj *moqtransport.Object) {
	if s.behind {
		if obj.GroupID <= s.behindGroup || obj.ObjectID != 0 {
			return
		}
		select {
		case s.queue <- fwdItem{resync: true}:
			s.behind = false
		default:
			return
		}
	}
	select {
	case s.queue <- fwdItem{obj: obj}:
	default:
		s.behind = true
		s.behindGroup = obj.GroupID
	}
}

// end propagates the upstream PUBLISH_DONE. The channel is buffered and only
// ever written once, so this never blocks.
func (s *subscriber) end(e publishEnd) {
	select {
	case s.done <- e:
	default:
	}
}

// serveSubscriber accepts the downstream subscription and feeds it: first the
// cached newest group (the group-aligned join), then live from the queue. It
// blocks until the subscription ends from either side; the caller holds the
// track acquired.
func (rt *relayTrack) serveSubscriber(r *moqtransport.SubscribeRequest) {
	var opts []moqtransport.SubscribeOkOption
	if largest, ok := rt.snapshotLargest(); ok {
		opts = append(opts, moqtransport.WithLargestObject(largest))
	}
	if props := rt.remote.TrackProperties(); len(props) > 0 {
		opts = append(opts, moqtransport.WithTrackProperties(props))
	}
	subscription, err := r.Accept(opts...)
	if err != nil {
		slog.Error("failed to accept subscription", "error", err,
			"namespace", rt.namespace, "track", rt.track)
		rt.release()
		return
	}

	s := newSubscriber(subscription, rt.h.QueueLen)
	backlog := rt.attach(s)
	defer rt.detach(s)
	slog.Info("forwarding subscription", "namespace", rt.namespace, "track", rt.track,
		"backlogObjects", len(backlog))

	for _, obj := range backlog {
		if !s.writeObject(obj) {
			return
		}
	}
	for {
		select {
		case <-r.Context().Done():
			// The subscriber unsubscribed or its session died; nothing to
			// send it any more.
			s.closeAll()
			return
		case end := <-s.done:
			s.finish(end)
			return
		case item := <-s.queue:
			if !s.handle(item) {
				return
			}
		}
	}
}

func (s *subscriber) handle(item fwdItem) bool {
	if item.resync {
		slog.Info("subscriber fell behind, resetting open subgroups")
		s.resetAll(moqtransport.StreamErrorTooFarBehind)
		return true
	}
	return s.writeObject(item.obj)
}

// finish drains what was queued before the end, then closes with the
// propagated PUBLISH_DONE.
func (s *subscriber) finish(end publishEnd) {
	for {
		select {
		case item := <-s.queue:
			if !s.handle(item) {
				return
			}
		default:
			s.closeAll()
			if err := s.sub.Close(end.code, end.reason); err != nil {
				slog.Debug("failed to close subscription", "error", err)
			}
			return
		}
	}
}

// writeObject re-emits one object, reconstructing subgroups from the (group,
// subgroup) IDs. Subgroup ends are inferred: when a newer group starts, every
// subgroup of an older group is closed (Eyevinn/moqtransport#19 tracks
// surfacing the real FIN/RESET). It reports false when the downstream is
// gone and forwarding should stop.
func (s *subscriber) writeObject(obj *moqtransport.Object) bool {
	if obj.ForwardingPreference == moqtransport.ObjectForwardingPreferenceDatagram {
		if err := s.sub.SendDatagram(*obj); err != nil {
			slog.Debug("failed to forward datagram", "error", err)
		}
		return true
	}

	if !s.haveGroup || obj.GroupID > s.newestGroup {
		for key, sg := range s.open {
			if key.group < obj.GroupID {
				if err := sg.Close(); err != nil {
					slog.Debug("failed to close subgroup", "error", err)
				}
				delete(s.open, key)
			}
		}
		s.newestGroup = obj.GroupID
		s.haveGroup = true
	}

	key := sgKey{group: obj.GroupID, subgroup: obj.SubgroupID}
	sg, ok := s.open[key]
	if !ok {
		// The PROPERTIES bit is per stream, so the subgroup's first
		// forwarded object decides it. moqlivemock never mixes objects with
		// and without properties within a subgroup.
		var opts []moqtransport.SubgroupOption
		if len(obj.Properties) > 0 {
			opts = append(opts, moqtransport.WithObjectProperties())
		}
		var err error
		sg, err = s.sub.OpenSubgroup(obj.GroupID, obj.SubgroupID, obj.Priority, opts...)
		if err != nil {
			slog.Info("failed to open downstream subgroup, stopping forwarding", "error", err)
			s.closeAll()
			return false
		}
		s.open[key] = sg
	}

	var err error
	if obj.Status != moqtransport.ObjectStatusNormal {
		err = sg.WriteStatus(obj.ObjectID, obj.Status)
	} else {
		_, err = sg.WriteObjectWithProperties(obj.ObjectID, obj.Properties, obj.Payload)
	}
	if err != nil {
		slog.Info("failed to write object downstream, stopping forwarding", "error", err,
			"groupID", obj.GroupID, "objectID", obj.ObjectID)
		s.closeAll()
		return false
	}
	return true
}

func (s *subscriber) closeAll() {
	for key, sg := range s.open {
		if err := sg.Close(); err != nil {
			slog.Debug("failed to close subgroup", "error", err)
		}
		delete(s.open, key)
	}
}

func (s *subscriber) resetAll(code moqtransport.StreamErrorCode) {
	for key, sg := range s.open {
		sg.Reset(code)
		delete(s.open, key)
	}
}

// rejectWithUpstreamError mirrors an upstream subscribe failure onto the
// downstream request, preserving the code where one exists.
func rejectWithUpstreamError(r *moqtransport.SubscribeRequest, err error) {
	code := moqtransport.RequestErrorDoesNotExist
	reason := "upstream subscribe failed"
	var reqErr *moqtransport.RequestError
	if errors.As(err, &reqErr) {
		code, reason = reqErr.Code, reqErr.Reason
	}
	slog.Info("upstream rejected subscription", "namespace", r.Namespace(),
		"track", r.Track(), "code", code, "reason", reason)
	if err := r.Reject(code, reason); err != nil {
		slog.Error("failed to reject subscription", "error", err)
	}
}
