package pub

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"time"

	"github.com/Eyevinn/moqlivemock/internal"
	"github.com/Eyevinn/moqtransport"
	"github.com/mengelbart/qlog"
	"github.com/mengelbart/qlog/moqt"
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
		Handler:             h.getHandler(),
		SubscribeHandler:    h.getSubscribeHandler(ctx),
		FetchHandler:        h.getFetchHandler(),
		InitialMaxRequestID: 100,
		Qlogger:             qlog.NewQLOGHandler(h.Logfh, "MoQ QLOG", "MoQ QLOG", conn.Perspective().String(), moqt.Schema),
	}
	slog.Info("starting MoQ session", "perspective", conn.Perspective())
	err := session.Run(conn)
	if err != nil {
		slog.Error("MoQ Session initialization failed", "error", err)
		err = conn.CloseWithError(0, "session initialization error")
		if err != nil {
			slog.Error("failed to close connection", "error", err)
		}
		return
	}
	for _, ns := range h.Namespaces {
		slog.Info("announcing namespace", "namespace", ns.Namespace)
		if err := session.Announce(ctx, ns.Namespace); err != nil {
			slog.Error("failed to announce namespace", "namespace", ns.Namespace, "error", err)
			return
		}
		slog.Info("namespace announced successfully", "namespace", ns.Namespace)
	}
	// Announce interop test namespace for moq-interop-runner compatibility
	slog.Info("announcing interop namespace", "namespace", interopNamespace)
	if err := session.Announce(ctx, interopNamespace); err != nil {
		slog.Warn("failed to announce interop namespace", "error", err)
	}
	// Block until context is cancelled to keep the session alive
	<-ctx.Done()
}

// interopNamespace is the namespace used by the moq-interop-runner test cases.
var interopNamespace = []string{"moq-test", "interop"}

func isInteropNamespace(ns []string) bool {
	return tupleEqual(ns, interopNamespace)
}

func (h *Handler) getHandler() moqtransport.Handler {
	return moqtransport.HandlerFunc(func(w moqtransport.ResponseWriter, r *moqtransport.Message) {
		switch r.Method {
		case moqtransport.MessageAnnounce:
			if isInteropNamespace(r.Namespace) {
				slog.Info("accepting interop announcement", "namespace", r.Namespace)
				if err := w.Accept(); err != nil {
					slog.Error("failed to accept interop announcement", "error", err)
				}
				return
			}
			slog.Warn("got unexpected announcement", "namespace", r.Namespace)
			err := w.Reject(0, "publisher doesn't take announcements")
			if err != nil {
				slog.Error("failed to reject announcement", "error", err)
			}
			return
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

func (h *Handler) getFetchHandler() moqtransport.FetchHandler {
	return moqtransport.FetchHandlerFunc(
		func(w *moqtransport.FetchResponseWriter, m *moqtransport.FetchMessage) {
			nsEntry := h.findNamespace(m.Namespace)
			if nsEntry == nil {
				slog.Warn("fetch: unknown namespace", "received", m.Namespace)
				err := w.Reject(uint64(moqtransport.ErrorCodeFetchTrackDoesNotExist), "non-matching namespace")
				if err != nil {
					slog.Error("failed to reject fetch", "error", err)
				}
				return
			}
			if nsEntry.Packaging == "moqmi" {
				err := w.Reject(uint64(moqtransport.ErrorCodeFetchTrackDoesNotExist),
					"moq-mi has no catalog")
				if err != nil {
					slog.Error("failed to reject moq-mi fetch", "error", err)
				}
				return
			}
			if m.Track != "catalog" {
				err := w.Reject(uint64(moqtransport.ErrorCodeFetchTrackDoesNotExist), "only catalog is fetchable")
				if err != nil {
					slog.Error("failed to reject fetch", "error", err)
				}
				return
			}
			err := w.Accept()
			if err != nil {
				slog.Error("failed to accept fetch", "error", err)
				return
			}
			fs, err := w.FetchStream()
			if err != nil {
				slog.Error("failed to get fetch stream", "error", err)
				return
			}
			catalogJSON, err := json.Marshal(nsEntry.Catalog)
			if err != nil {
				slog.Error("failed to marshal catalog", "error", err)
				return
			}
			_, err = fs.WriteObject(0, 0, 0, 0, catalogJSON)
			if err != nil {
				slog.Error("failed to write catalog via fetch", "error", err)
				return
			}
			err = fs.Close()
			if err != nil {
				slog.Error("failed to close fetch stream", "error", err)
				return
			}
			slog.Info("served catalog via FETCH", "namespace", m.Namespace)
		})
}

func (h *Handler) getSubscribeHandler(ctx context.Context) moqtransport.SubscribeHandler {
	return moqtransport.SubscribeHandlerFunc(
		func(w *moqtransport.SubscribeResponseWriter, m *moqtransport.SubscribeMessage) {
			// Accept interop test subscriptions (control-plane only, no media)
			if isInteropNamespace(m.Namespace) {
				slog.Info("accepting interop subscription", "namespace", m.Namespace, "track", m.Track)
				if err := w.Accept(); err != nil {
					slog.Error("failed to accept interop subscription", "error", err)
				}
				return
			}
			nsEntry := h.findNamespace(m.Namespace)
			if nsEntry == nil {
				slog.Warn("got unexpected subscription namespace", "received", m.Namespace)
				err := w.Reject(0, "non-matching namespace")
				if err != nil {
					slog.Error("failed to reject subscription", "error", err)
				}
				return
			}
			// moq-mi namespaces are catalogless and use fixed convention track names.
			if nsEntry.Packaging == "moqmi" {
				if m.Track == "catalog" {
					err := w.Reject(moqtransport.ErrorCodeSubscribeTrackDoesNotExist,
						"moq-mi has no catalog")
					if err != nil {
						slog.Error("failed to reject catalog subscription for moq-mi", "error", err)
					}
					return
				}
				assetTrack := ResolveMoqMITrack(nsEntry.MoqMITracks, m.Track)
				if assetTrack == "" {
					err := w.Reject(moqtransport.ErrorCodeSubscribeTrackDoesNotExist,
						"unknown moq-mi track")
					if err != nil {
						slog.Error("failed to reject moq-mi subscription", "error", err)
					}
					return
				}
				if err := w.Accept(); err != nil {
					slog.Error("failed to accept moq-mi subscription", "error", err)
					return
				}
				slog.Info("got moq-mi subscription", "track", m.Track,
					"assetTrack", assetTrack, "namespace", m.Namespace)
				go PublishMoqMITrack(ctx, w, h.Asset, assetTrack, m.Track)
				return
			}
			if m.Track == "catalog" {
				err := w.Accept()
				if err != nil {
					slog.Error("failed to accept subscription", "error", err)
					return
				}
				sg, err := w.OpenSubgroup(0, 0, 0)
				if err != nil {
					slog.Error("failed to open subgroup", "error", err)
					return
				}
				json, err := json.Marshal(nsEntry.Catalog)
				if err != nil {
					slog.Error("failed to marshal catalog", "error", err)
					return
				}
				_, err = sg.WriteObject(0, json)
				if err != nil {
					slog.Error("failed to write catalog", "error", err)
					return
				}
				err = sg.Close()
				if err != nil {
					slog.Error("failed to close subgroup", "error", err)
					return
				}
				return
			}
			// Check for subtitle tracks first
			if st := h.Asset.GetSubtitleTrackByName(m.Track); st != nil {
				err := w.Accept()
				if err != nil {
					slog.Error("failed to accept subscription", "error", err)
					return
				}
				slog.Info("got subtitle subscription", "track", st.Name, "namespace", m.Namespace)
				go PublishSubtitleTrack(ctx, w, st)
				return
			}

			// Check for video/audio tracks in this namespace's catalog
			for _, track := range nsEntry.Catalog.Tracks {
				if m.Track == track.Name {
					err := w.Accept()
					if err != nil {
						slog.Error("failed to accept subscription", "error", err)
						return
					}
					slog.Info("got subscription", "track", track.Name, "namespace", m.Namespace,
						"packaging", nsEntry.Packaging)
					if nsEntry.Packaging == "loc" {
						go PublishLOCTrack(ctx, w, h.Asset, track.Name)
					} else {
						go PublishTrack(ctx, w, h.Asset, track.Name)
					}
					return
				}
			}
			// If we get here, the track was not found
			err := w.Reject(moqtransport.ErrorCodeSubscribeTrackDoesNotExist, "unknown track")
			if err != nil {
				slog.Error("failed to reject subscription", "error", err)
			}
		})
}

// PublishTrack publishes media track data in MoQ groups, pacing delivery to wall-clock time.
func PublishTrack(ctx context.Context, publisher moqtransport.Publisher, asset *internal.Asset, trackName string) {
	ct := asset.GetTrackByName(trackName)
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
		mg, err := internal.GenMoQGroup(ct, groupNr, ct.SampleBatch, internal.MoqGroupDurMS)
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

// LOC extension header property IDs from draft-ietf-moq-loc-02 §2.3.1.
const (
	locPropTimestamp = 0x06 // vi64: microseconds since Unix epoch when no Timescale is present
)

// PublishLOCTrack publishes LOC media track data (one raw frame per object) in MoQ groups,
// pacing delivery to wall-clock time. Each object carries a LOC Timestamp property
// (draft-ietf-moq-loc-02 §2.3.1.1) with the sample presentation time in microseconds
// since the Unix epoch.
func PublishLOCTrack(ctx context.Context, publisher moqtransport.Publisher, asset *internal.Asset, trackName string) {
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
	if avcData, ok := ct.SpecData.(*internal.AVCData); ok {
		videoConfig = avcData.GenLOCVideoConfig()
	}

	now := time.Now().UnixMilli()
	currGroupNr := internal.CurrMoQGroupNr(ct, uint64(now), internal.MoqGroupDurMS)
	groupNr := currGroupNr + 1 // Start stream on next group
	slog.Info("publishing LOC track", "track", trackName, "group", groupNr)
	for {
		if ctx.Err() != nil {
			return
		}
		sg, err := publisher.OpenSubgroup(groupNr, 0, MediaPriority)
		if err != nil {
			slog.Error("failed to open subgroup", "error", err)
			return
		}
		startNr, endNr := internal.CalcLOCGroupRange(ct, groupNr, internal.MoqGroupDurMS)
		slog.Info("writing LOC group", "track", ct.Name, "group", groupNr, "objects", endNr-startNr)
		objectID := uint64(0)
		for sampleNr := startNr; sampleNr < endNr; sampleNr++ {
			if ctx.Err() != nil {
				_ = sg.Close()
				return
			}
			_, origNr := ct.CalcSample(sampleNr)
			sample := ct.Samples[origNr]

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
				payload = make([]byte, 0, len(videoConfig)+len(sample.Data))
				payload = append(payload, videoConfig...)
				payload = append(payload, sample.Data...)
			} else {
				payload = sample.Data
			}

			// Compute sampleTime * 1_000_000 / timebase without uint64 overflow.
			// sampleTime can reach ~1.8e15 for wall-clock-anchored live streams, so a
			// naive multiply overflows; split into quotient and fractional microseconds.
			timestampUs := (sampleTime/timebase)*1_000_000 + (sampleTime%timebase)*1_000_000/timebase
			headers := moqtransport.KVPList{
				{Type: locPropTimestamp, ValueVarInt: timestampUs},
			}
			if _, err := sg.WriteObjectWithHeaders(objectID, headers, payload); err != nil {
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
func PublishSubtitleTrack(ctx context.Context, publisher moqtransport.Publisher, st *internal.SubtitleTrack) {
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
