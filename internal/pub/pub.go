package pub

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/Eyevinn/moqlivemock/internal"
	"github.com/Eyevinn/moqtransport"
	"github.com/mengelbart/qlog"
)

const (
	MediaPriority = 128
)

// NamespaceEntry pairs an announcement namespace with its catalog.
type NamespaceEntry struct {
	Namespace []string
	Catalog   *internal.Catalog
	Packaging string // "cmaf", "loc", or "moqmi"
	// MoqMITracks, when Packaging == "moqmi", maps moqmi-convention track names
	// (e.g. "video0", "audio0") to asset track names. moqmi has no catalog, so
	// this map provides the server-side binding to real asset tracks.
	MoqMITracks MoqMITrackMap
}

// Handler handles MoQ publisher sessions. It serves catalogs and publishes
// media tracks (video, audio, subtitles) to subscribers across multiple namespaces.
type Handler struct {
	Namespaces []NamespaceEntry
	Asset      *internal.Asset
	Logfh      io.Writer
}

// Handle runs a MoQ session on the given connection, announces all namespaces,
// and serves subscriptions. The context controls the lifetime of publishing goroutines.
func (h *Handler) Handle(ctx context.Context, conn moqtransport.Connection) {
	session := &moqtransport.Session{
		PublishNamespaceHandler: h.getPublishNamespaceHandler(),
		SubscribeHandler:        h.getSubscribeHandler(ctx),
		FetchHandler:            h.getFetchHandler(),
		Implementation:          "Eyevinn/moqlivemock",
		Qlogger: qlog.NewQLOGHandler(h.Logfh, "MoQ QLOG", "MoQ QLOG",
			conn.Perspective().String(), moqtransport.QlogSchema),
	}
	slog.Info("starting MoQ session", "perspective", conn.Perspective())
	if err := session.Run(ctx, conn); err != nil {
		slog.Error("MoQ Session initialization failed", "error", err)
		if err := conn.CloseWithError(0, "session initialization error"); err != nil {
			slog.Error("failed to close connection", "error", err)
		}
		return
	}
	slog.Info("MoQ session established", "version", session.Version())

	// An announcement lasts as long as the stream carrying it, so the handles
	// are held until the session ends rather than dropped. Closing one is what
	// draft-18 uses in place of UNANNOUNCE.
	announce := func(namespace []string) *moqtransport.NamespacePublication {
		publication, err := session.PublishNamespace(ctx, namespace)
		if err != nil {
			slog.Error("failed to announce namespace", "namespace", namespace, "error", err)
			return nil
		}
		slog.Info("namespace announced successfully", "namespace", namespace)
		return publication
	}

	var publications []*moqtransport.NamespacePublication
	defer func() {
		for _, p := range publications {
			_ = p.Close()
		}
	}()

	for _, ns := range h.Namespaces {
		slog.Info("announcing namespace", "namespace", ns.Namespace)
		if p := announce(ns.Namespace); p != nil {
			publications = append(publications, p)
		} else {
			return
		}
	}
	// Announce interop test namespace for moq-interop-runner compatibility
	slog.Info("announcing interop namespace", "namespace", interopNamespace)
	if p := announce(interopNamespace); p != nil {
		publications = append(publications, p)
	}

	// Block until the context is cancelled or the session ends.
	select {
	case <-ctx.Done():
	case <-session.Context().Done():
		slog.Info("MoQ session ended", "reason", context.Cause(session.Context()))
	}
}

// interopNamespace is the namespace used by the moq-interop-runner test cases.
var interopNamespace = []string{"moq-test", "interop"}

func isInteropNamespace(ns []string) bool {
	return tupleEqual(ns, interopNamespace)
}

func (h *Handler) getPublishNamespaceHandler() moqtransport.PublishNamespaceHandler {
	return moqtransport.PublishNamespaceHandlerFunc(func(r *moqtransport.PublishNamespaceRequest) {
		if isInteropNamespace(r.Namespace()) {
			slog.Info("accepting interop announcement", "namespace", r.Namespace())
			if err := r.Accept(); err != nil {
				slog.Error("failed to accept interop announcement", "error", err)
			}
			return
		}
		slog.Warn("got unexpected announcement", "namespace", r.Namespace())
		if err := r.Reject(moqtransport.RequestErrorUninterested,
			"publisher doesn't take announcements"); err != nil {
			slog.Error("failed to reject announcement", "error", err)
		}
	})
}

// findNamespace returns the NamespaceEntry matching the given namespace tuple, or nil.
func (h *Handler) findNamespace(ns []string) *NamespaceEntry {
	for i := range h.Namespaces {
		if tupleEqual(ns, h.Namespaces[i].Namespace) {
			return &h.Namespaces[i]
		}
	}
	return nil
}

// locationLess reports whether a precedes b in (group, object) order.
func locationLess(a, b moqtransport.Location) bool {
	if a.Group != b.Group {
		return a.Group < b.Group
	}
	return a.Object < b.Object
}

// locationInFetchRange reports whether loc falls within a FETCH's requested
// [start, end) range. An EndObject of 0 means "to the end of EndGroup" per the
// FETCH semantics (draft-ietf-moq-transport §9.16.3).
func locationInFetchRange(loc, start, end moqtransport.Location) bool {
	if locationLess(loc, start) {
		return false
	}
	if end.Object == 0 {
		return loc.Group <= end.Group
	}
	return locationLess(loc, end)
}

func (h *Handler) getFetchHandler() moqtransport.FetchHandler {
	return moqtransport.FetchHandlerFunc(func(r *moqtransport.FetchRequest) {
		reject := func(reason string) {
			if err := r.Reject(moqtransport.RequestErrorDoesNotExist, reason); err != nil {
				slog.Error("failed to reject fetch", "error", err, "reason", reason)
			}
		}
		nsEntry := h.findNamespace(r.Namespace())
		if nsEntry == nil {
			slog.Warn("fetch: unknown namespace", "received", r.Namespace())
			reject("non-matching namespace")
			return
		}
		if nsEntry.Packaging == "moqmi" {
			reject("moq-mi has no catalog")
			return
		}
		if r.Track() != "catalog" {
			reject("only catalog is fetchable")
			return
		}

		response, err := r.Accept()
		if err != nil {
			slog.Error("failed to accept fetch", "error", err)
			return
		}
		// The catalog is a single object at {group:0, object:0}. Honor the
		// requested range -- already resolved against the subscription for a
		// joining fetch -- and serve the object only when it covers {0,0}; any
		// other range yields an empty response, which is a FETCH_HEADER and a
		// FIN and is a meaningful answer in itself.
		start, end := r.Range()
		catalogLoc := moqtransport.Location{Group: 0, Object: 0}
		if locationInFetchRange(catalogLoc, start, end) {
			catalogJSON, err := json.Marshal(nsEntry.Catalog)
			if err != nil {
				slog.Error("failed to marshal catalog", "error", err)
				return
			}
			if err := response.WriteObject(moqtransport.Object{
				GroupID:  0,
				ObjectID: 0,
				Priority: MediaPriority,
				Payload:  catalogJSON,
			}); err != nil {
				slog.Error("failed to write catalog via fetch", "error", err)
				response.Reset(moqtransport.StreamErrorInternal)
				return
			}
			joining, isJoining := r.Joining()
			slog.Info("served catalog via FETCH", "namespace", r.Namespace(),
				"joining", isJoining, "joiningRequestID", joining, "start", start, "end", end)
		} else {
			slog.Info("FETCH range excludes catalog object {0,0}; empty response",
				"namespace", r.Namespace(), "start", start, "end", end)
		}
		if err := response.Close(); err != nil {
			slog.Error("failed to close fetch stream", "error", err)
		}
	})
}

func (h *Handler) getSubscribeHandler(ctx context.Context) moqtransport.SubscribeHandler {
	return moqtransport.SubscribeHandlerFunc(func(r *moqtransport.SubscribeRequest) {
		namespace, track := r.Namespace(), r.Track()

		reject := func(reason string) {
			if err := r.Reject(moqtransport.RequestErrorDoesNotExist, reason); err != nil {
				slog.Error("failed to reject subscription", "error", err, "reason", reason)
			}
		}
		accept := func(opts ...moqtransport.SubscribeOkOption) *moqtransport.Subscription {
			subscription, err := r.Accept(opts...)
			if err != nil {
				slog.Error("failed to accept subscription", "error", err,
					"namespace", namespace, "track", track)
				return nil
			}
			return subscription
		}

		// Accept interop test subscriptions (control-plane only, no media).
		if isInteropNamespace(namespace) {
			slog.Info("accepting interop subscription", "namespace", namespace, "track", track)
			accept()
			return
		}
		nsEntry := h.findNamespace(namespace)
		if nsEntry == nil {
			slog.Warn("got unexpected subscription namespace", "received", namespace)
			reject("non-matching namespace")
			return
		}

		// moq-mi namespaces are catalogless and use fixed convention track names.
		if nsEntry.Packaging == "moqmi" {
			if track == "catalog" {
				reject("moq-mi has no catalog")
				return
			}
			assetTrack := ResolveMoqMITrack(nsEntry.MoqMITracks, track)
			if assetTrack == "" {
				reject("unknown moq-mi track")
				return
			}
			subscription := accept()
			if subscription == nil {
				return
			}
			slog.Info("got moq-mi subscription", "track", track,
				"assetTrack", assetTrack, "namespace", namespace)
			go PublishMoqMITrack(ctx, subscription, h.Asset, assetTrack, track)
			return
		}

		if track == "catalog" {
			// Advertise the catalog's largest location so subscribers can
			// resolve a relative Joining FETCH (offset 0) against this
			// subscription per MSF draft-01 §5. The catalog is a single full
			// object at {group:0, object:0}. We still write object 0 on the
			// subscription below for backward compatibility with
			// subscribe-only clients; joining clients dedupe it against the
			// FETCH (objects <= largest are skipped on the subscription).
			subscription := accept(moqtransport.WithLargestObject(
				moqtransport.Location{Group: 0, Object: 0}))
			if subscription == nil {
				return
			}
			if err := h.writeCatalog(subscription, nsEntry); err != nil {
				slog.Error("failed to publish catalog", "error", err, "namespace", namespace)
			}
			return
		}

		// Check for subtitle tracks first.
		if st := h.Asset.GetSubtitleTrackByName(track); st != nil {
			subscription := accept()
			if subscription == nil {
				return
			}
			slog.Info("got subtitle subscription", "track", st.Name, "namespace", namespace)
			go PublishSubtitleTrack(ctx, subscription, st)
			return
		}

		// Check for video/audio tracks in this namespace's catalog.
		for _, catalogTrack := range nsEntry.Catalog.Tracks {
			if track != catalogTrack.Name {
				continue
			}
			subscription := accept()
			if subscription == nil {
				return
			}
			slog.Info("got subscription", "track", catalogTrack.Name, "namespace", namespace,
				"packaging", nsEntry.Packaging)
			if nsEntry.Packaging == "loc" {
				go PublishLOCTrack(ctx, subscription, h.Asset, catalogTrack.Name)
			} else {
				go PublishTrack(ctx, subscription, h.Asset, catalogTrack.Name, catalogTrack.Packaging)
			}
			return
		}
		reject("unknown track")
	})
}

// writeCatalog publishes the catalog as a single object at {0, 0}.
func (h *Handler) writeCatalog(subscription *moqtransport.Subscription, nsEntry *NamespaceEntry) error {
	catalogJSON, err := json.Marshal(nsEntry.Catalog)
	if err != nil {
		return fmt.Errorf("marshalling catalog: %w", err)
	}
	sg, err := subscription.OpenSubgroup(0, 0, MediaPriority, moqtransport.WithEndOfGroup())
	if err != nil {
		return fmt.Errorf("opening subgroup: %w", err)
	}
	if _, err := sg.WriteObject(0, catalogJSON); err != nil {
		sg.Reset(moqtransport.StreamErrorInternal)
		return fmt.Errorf("writing catalog: %w", err)
	}
	return sg.Close()
}

// PublishTrack publishes media track data in MoQ groups, pacing delivery to wall-clock time.
func PublishTrack(ctx context.Context, publisher *moqtransport.Subscription,
	asset *internal.Asset, trackName, packaging string) {

	// LOCMAF variant tracks in a unified CMSF catalog are named
	// <contentTrack>_locmaf; strip the suffix to find the content track.
	assetTrackName := strings.TrimSuffix(trackName, internal.LocmafTrackSuffix)
	ct := asset.GetTrackByName(assetTrackName)
	if ct == nil {
		slog.Error("track not found", "track", trackName)
		return
	}
	now := time.Now().UnixMilli()
	currGroupNr := internal.CurrMoQGroupNr(ct, uint64(now), internal.MoqGroupDurMS)
	groupNr := currGroupNr + 1 // Start stream on next group
	slog.Info("publishing track", "track", trackName, "group", groupNr)
	for {
		if ctx.Err() != nil {
			return
		}
		sg, err := publisher.OpenSubgroup(groupNr, 0, MediaPriority)
		if err != nil {
			slog.Error("failed to open subgroup", "error", err)
			return
		}
		mg, err := internal.GenMoQGroup(ct, groupNr, ct.SampleBatch, internal.MoqGroupDurMS, packaging)
		if err != nil {
			slog.Error("failed to generate MoQ group", "track", ct.Name, "group", groupNr, "error", err)
			return
		}
		slog.Info("writing MoQ group", "track", ct.Name, "group", groupNr, "objects", len(mg.MoQObjects))
		err = internal.WriteMoQGroup(ctx, ct, mg, sg.WriteObject)
		if err != nil {
			slog.Error("failed to write MoQ group", "error", err)
			return
		}
		err = sg.Close()
		if err != nil {
			slog.Error("failed to close subgroup", "error", err)
			return
		}
		slog.Debug("published MoQ group", "track", ct.Name, "group", groupNr, "objects", len(mg.MoQObjects))
		groupNr++
	}
}

// LOC Object Property IDs from draft-ietf-moq-loc-04 §2.3.1.
const (
	// locPropTimestamp is the LOC Timestamp property: microseconds since the
	// Unix epoch when no Timescale property is present.
	//
	// The codepoint has moved twice. draft-ietf-moq-loc-03 moved it from 0x06
	// to 0x0A because MOQT's Properties registry allocates 0x06 to
	// SUBGROUP_DELIVERY_TIMEOUT, which is Track scope only, so a 0x06 Object
	// Property is a malformed track from draft-18 onwards. draft-04 then moved
	// it again to 0x10, publishing a settled registry table where -03 still
	// carried "IANA, please assign" on its neighbours.
	locPropTimestamp = 0x10
)

// PublishLOCTrack publishes LOC media track data (one raw frame per object) in MoQ groups,
// pacing delivery to wall-clock time. Each object carries a LOC Timestamp property
// (draft-ietf-moq-loc-03 §2.3.1.1) with the sample presentation time in microseconds
// since the Unix epoch.
func PublishLOCTrack(ctx context.Context, publisher *moqtransport.Subscription,
	asset *internal.Asset, trackName string) {
	ct := asset.GetTrackByName(trackName)
	if ct == nil {
		slog.Error("track not found", "track", trackName)
		return
	}
	timebase := uint64(ct.TimeScale)
	sampleDur := uint64(ct.SampleDur)
	if timebase == 0 || sampleDur == 0 {
		slog.Error("LOC: invalid track timing", "track", trackName, "timescale", timebase, "sampleDur", sampleDur)
		return
	}

	var videoConfig []byte
	switch sd := ct.SpecData.(type) {
	case *internal.AVCData:
		videoConfig = sd.GenLOCVideoConfig()
	case *internal.HEVCData:
		videoConfig = sd.GenLOCVideoConfig()
	case *internal.AV1Data:
		// nil when keyframes already carry the sequence header OBU in-band
		// (SVT-AV1/ffmpeg), otherwise the sequence header OBU to prepend.
		videoConfig = sd.GenLOCVideoConfig()
	}

	// Optional CTA-608 caption injection (no-op unless -cc608 installed an
	// enabled generator on this video track).
	captioner := newVideoCaptioner(ct)

	now := time.Now().UnixMilli()
	currGroupNr := internal.CurrMoQGroupNr(ct, uint64(now), internal.MoqGroupDurMS)
	groupNr := currGroupNr + 1 // Start stream on next group
	slog.Info("publishing LOC track", "track", trackName, "group", groupNr)
	for {
		if ctx.Err() != nil {
			return
		}
		// Every object on this stream carries a LOC Timestamp property, and
		// draft-18 puts the PROPERTIES bit in the subgroup header rather than
		// per object, so it has to be declared when the stream is opened.
		sg, err := publisher.OpenSubgroup(groupNr, 0, MediaPriority,
			moqtransport.WithObjectProperties())
		if err != nil {
			slog.Error("failed to open subgroup", "error", err)
			return
		}
		startNr, endNr := internal.CalcLOCGroupRange(ct, groupNr, internal.MoqGroupDurMS)
		// A LOC group is one wall-clock second, so groupNr is the caption clock
		// anchor in whole seconds. ccSched is nil (no splicing) when captions
		// are off.
		ccSched := captioner.schedule(int64(groupNr), startNr, endNr)
		slog.Info("writing LOC group", "track", ct.Name, "group", groupNr, "objects", endNr-startNr)
		objectID := uint64(0)
		for sampleNr := startNr; sampleNr < endNr; sampleNr++ {
			if ctx.Err() != nil {
				_ = sg.Close()
				return
			}
			_, origNr := ct.CalcSample(sampleNr)
			sample := ct.Samples[origNr]
			// Splice the frame's CTA-608 envelope before the (optional)
			// videoConfig prepend, so the decoder configuration (AVC/HEVC
			// parameter sets, AV1 sequence header OBU) stays ahead of the
			// captions, which in turn stay ahead of the picture data.
			data := captioner.spliceFrame(sample.Data, ccSched, sampleNr, startNr)

			sampleTime := sampleNr * sampleDur
			objTimeMS := int64(sampleTime * 1000 / timebase)
			waitMS := objTimeMS - time.Now().UnixMilli()
			if waitMS > 0 {
				select {
				case <-ctx.Done():
					_ = sg.Close()
					return
				case <-time.After(time.Duration(waitMS) * time.Millisecond):
				}
			}

			var payload []byte
			if videoConfig != nil && sample.IsSync() {
				payload = make([]byte, 0, len(videoConfig)+len(data))
				payload = append(payload, videoConfig...)
				payload = append(payload, data...)
			} else {
				payload = data
			}

			// Compute sampleTime * 1_000_000 / timebase without uint64 overflow.
			// sampleTime can reach ~1.8e15 for wall-clock-anchored live streams, so a
			// naive multiply overflows; split into quotient and fractional microseconds.
			timestampUs := (sampleTime/timebase)*1_000_000 + (sampleTime%timebase)*1_000_000/timebase
			properties := moqtransport.KVPList{
				{Type: locPropTimestamp, ValueVarInt: timestampUs},
			}
			if _, err := sg.WriteObjectWithProperties(objectID, properties, payload); err != nil {
				slog.Error("failed to write LOC object", "track", ct.Name, "group", groupNr,
					"object", objectID, "error", err)
				_ = sg.Close()
				return
			}
			objectID++
		}
		if err := sg.Close(); err != nil {
			slog.Error("failed to close subgroup", "error", err)
			return
		}
		slog.Debug("published LOC group", "track", ct.Name, "group", groupNr, "objects", objectID)
		groupNr++
	}
}

// PublishSubtitleTrack publishes subtitle track data in MoQ groups, pacing delivery to wall-clock time.
func PublishSubtitleTrack(ctx context.Context, publisher *moqtransport.Subscription, st *internal.SubtitleTrack) {
	now := time.Now().UnixMilli()
	currGroupNr := internal.CurrSubtitleGroupNr(uint64(now), internal.MoqGroupDurMS)
	groupNr := currGroupNr + 1 // Start stream on next group
	slog.Info("publishing subtitle track", "track", st.Name, "group", groupNr)

	for {
		if ctx.Err() != nil {
			return
		}

		sg, err := publisher.OpenSubgroup(groupNr, 0, MediaPriority)
		if err != nil {
			slog.Error("failed to open subgroup for subtitle", "error", err)
			return
		}

		mg, err := internal.GenSubtitleGroup(st, groupNr, internal.MoqGroupDurMS)
		if err != nil {
			slog.Error("failed to generate subtitle group", "error", err)
			return
		}

		slog.Info("writing MoQ subtitle group", "track", st.Name, "group", groupNr, "objects", len(mg.MoQObjects))

		// Subtitle groups have 1 object - write it with proper timing
		err = WriteSubtitleGroup(ctx, mg, groupNr, sg.WriteObject)
		if err != nil {
			slog.Error("failed to write subtitle MoQ group", "error", err)
			return
		}

		err = sg.Close()
		if err != nil {
			slog.Error("failed to close subtitle subgroup", "error", err)
			return
		}

		slog.Debug("published subtitle MoQ group", "track", st.Name, "group", groupNr)
		groupNr++
	}
}

// WriteSubtitleGroup writes subtitle objects with appropriate timing.
func WriteSubtitleGroup(ctx context.Context, moq *internal.MoQGroup, groupNr uint64, cb internal.ObjectWriter) error {
	// Calculate when this group should be sent (at the start of the group)
	groupStartTimeMS := int64(groupNr * uint64(internal.MoqGroupDurMS))

	for nr, moqObj := range moq.MoQObjects {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		now := time.Now().UnixMilli()
		waitTime := groupStartTimeMS - now

		if waitTime <= 0 {
			// Already past time, send immediately
			_, err := cb(uint64(nr), moqObj)
			if err != nil {
				return err
			}
			continue
		}

		// Wait until the start of the group period
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(waitTime) * time.Millisecond):
			_, err := cb(uint64(nr), moqObj)
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func tupleEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i, t := range a {
		if t != b[i] {
			return false
		}
	}
	return true
}
