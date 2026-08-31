package relay

import (
	"context"
	"errors"
	"log/slog"

	"github.com/Eyevinn/moqtransport"
)

// sgKey identifies a subgroup within a track.
type sgKey struct {
	group    uint64
	subgroup uint64
}

// forward answers a downstream SUBSCRIBE by subscribing upstream on the
// announcing session and relaying the answer and every object. It blocks
// until the subscription ends from either side, so it runs on the subscribe
// handler's goroutine.
func forward(r *moqtransport.SubscribeRequest, upstream *moqtransport.Session) {
	namespace, track := r.Namespace(), r.Track()
	// The request context ends when the subscriber unsubscribes or its
	// session dies; letting it bound the upstream subscription propagates
	// the unsubscribe.
	ctx := r.Context()

	remote, err := upstream.Subscribe(ctx, namespace, track)
	if err != nil {
		code := moqtransport.RequestErrorDoesNotExist
		reason := "upstream subscribe failed"
		var reqErr *moqtransport.RequestError
		if errors.As(err, &reqErr) {
			code, reason = reqErr.Code, reqErr.Reason
		}
		slog.Info("upstream rejected subscription", "namespace", namespace, "track", track,
			"code", code, "reason", reason)
		if err := r.Reject(code, reason); err != nil {
			slog.Error("failed to reject subscription", "error", err)
		}
		return
	}
	defer func() {
		if err := remote.Close(); err != nil {
			slog.Debug("failed to close upstream subscription", "error", err)
		}
	}()

	var okOpts []moqtransport.SubscribeOkOption
	if largest, ok := remote.LargestObject(); ok {
		okOpts = append(okOpts, moqtransport.WithLargestObject(largest))
	}
	if props := remote.TrackProperties(); len(props) > 0 {
		okOpts = append(okOpts, moqtransport.WithTrackProperties(props))
	}
	subscription, err := r.Accept(okOpts...)
	if err != nil {
		slog.Error("failed to accept subscription", "error", err,
			"namespace", namespace, "track", track)
		return
	}
	slog.Info("forwarding subscription", "namespace", namespace, "track", track)
	relayObjects(ctx, remote, subscription)
}

// relayObjects reads objects from the upstream track and re-emits them on the
// downstream subscription, reconstructing subgroups from the (group,
// subgroup) IDs.
//
// moqtransport does not surface the upstream subgroup streams' FIN or RESET,
// so subgroup ends are inferred: when a newer group starts, every subgroup of
// an older group is closed. That matches live media with ascending groups; a
// subgroup of the newest group stays open until the subscription ends.
func relayObjects(ctx context.Context, remote *moqtransport.RemoteTrack,
	subscription *moqtransport.Subscription) {

	open := make(map[sgKey]*moqtransport.Subgroup)
	var newestGroup uint64
	haveGroup := false

	closeGroupsBelow := func(group uint64) {
		for key, sg := range open {
			if key.group < group {
				if err := sg.Close(); err != nil {
					slog.Debug("failed to close subgroup", "error", err)
				}
				delete(open, key)
			}
		}
	}
	closeAll := func() {
		for key, sg := range open {
			if err := sg.Close(); err != nil {
				slog.Debug("failed to close subgroup", "error", err)
			}
			delete(open, key)
		}
	}

	for {
		obj, err := remote.ReadObject(ctx)
		if err != nil {
			closeAll()
			if ctx.Err() != nil {
				// The downstream subscriber went away; there is nobody to
				// send PUBLISH_DONE to.
				slog.Info("subscriber left, unsubscribing upstream",
					"namespace", remote.Namespace(), "track", remote.Track())
				return
			}
			// Upstream ended: propagate PUBLISH_DONE, or report the loss.
			code := moqtransport.PublishDoneInternalError
			reason := "upstream ended"
			if done, ok := remote.PublishDone(); ok {
				code, reason = done.Code, done.Reason
			}
			slog.Info("upstream subscription ended", "namespace", remote.Namespace(),
				"track", remote.Track(), "code", code, "reason", reason, "readError", err)
			if err := subscription.Close(code, reason); err != nil {
				slog.Debug("failed to close subscription", "error", err)
			}
			return
		}

		if obj.ForwardingPreference == moqtransport.ObjectForwardingPreferenceDatagram {
			if err := subscription.SendDatagram(*obj); err != nil {
				slog.Debug("failed to forward datagram", "error", err)
			}
			continue
		}

		if !haveGroup || obj.GroupID > newestGroup {
			closeGroupsBelow(obj.GroupID)
			newestGroup = obj.GroupID
			haveGroup = true
		}

		key := sgKey{group: obj.GroupID, subgroup: obj.SubgroupID}
		sg, ok := open[key]
		if !ok {
			// The PROPERTIES bit is per stream, so the subgroup's first
			// forwarded object decides it. moqlivemock never mixes objects
			// with and without properties within a subgroup.
			var opts []moqtransport.SubgroupOption
			if len(obj.Properties) > 0 {
				opts = append(opts, moqtransport.WithObjectProperties())
			}
			sg, err = subscription.OpenSubgroup(obj.GroupID, obj.SubgroupID, obj.Priority, opts...)
			if err != nil {
				slog.Info("failed to open downstream subgroup, stopping forwarding",
					"error", err, "track", remote.Track())
				closeAll()
				return
			}
			open[key] = sg
		}

		switch {
		case obj.Status != moqtransport.ObjectStatusNormal:
			err = sg.WriteStatus(obj.ObjectID, obj.Status)
		default:
			_, err = sg.WriteObjectWithProperties(obj.ObjectID, obj.Properties, obj.Payload)
		}
		if err != nil {
			slog.Info("failed to write object downstream, stopping forwarding",
				"error", err, "track", remote.Track(),
				"groupID", obj.GroupID, "objectID", obj.ObjectID)
			closeAll()
			return
		}
		slog.Debug("forwarded object", "track", remote.Track(),
			"groupID", obj.GroupID, "subgroupID", obj.SubgroupID,
			"objectID", obj.ObjectID, "payloadLength", len(obj.Payload))
	}
}
