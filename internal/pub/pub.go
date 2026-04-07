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

// Handler handles MoQ publisher sessions. It serves a catalog and publishes
// media tracks (video, audio, subtitles) to subscribers.
type Handler struct {
	Namespace []string
	Asset     *internal.Asset
	Catalog   *internal.Catalog
	Logfh     io.Writer
}

// Handle runs a MoQ session on the given connection, announces the namespace,
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
	slog.Info("MoQ session established, announcing namespace", "namespace", h.Namespace)
	if err := session.Announce(ctx, h.Namespace); err != nil {
		slog.Error("failed to announce namespace", "namespace", h.Namespace, "error", err)
		return
	}
	slog.Info("namespace announced successfully", "namespace", h.Namespace)
	// Block until context is cancelled to keep the session alive
	<-ctx.Done()
}

func (h *Handler) getHandler() moqtransport.Handler {
	return moqtransport.HandlerFunc(func(w moqtransport.ResponseWriter, r *moqtransport.Message) {
		switch r.Method {
		case moqtransport.MessageAnnounce:
			slog.Warn("got unexpected announcement", "namespace", r.Namespace)
			err := w.Reject(0, "publisher doesn't take announcements")
			if err != nil {
				slog.Error("failed to reject announcement", "error", err)
			}
			return
		}
	})
}

func (h *Handler) getFetchHandler() moqtransport.FetchHandler {
	return moqtransport.FetchHandlerFunc(
		func(w *moqtransport.FetchResponseWriter, m *moqtransport.FetchMessage) {
			if !tupleEqual(m.Namespace, h.Namespace) {
				slog.Warn("fetch: unexpected namespace",
					"received", m.Namespace,
					"expected", h.Namespace)
				err := w.Reject(uint64(moqtransport.ErrorCodeFetchTrackDoesNotExist), "non-matching namespace")
				if err != nil {
					slog.Error("failed to reject fetch", "error", err)
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
			catalogJSON, err := json.Marshal(h.Catalog)
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
			slog.Info("served catalog via FETCH")
		})
}

func (h *Handler) getSubscribeHandler(ctx context.Context) moqtransport.SubscribeHandler {
	return moqtransport.SubscribeHandlerFunc(
		func(w *moqtransport.SubscribeResponseWriter, m *moqtransport.SubscribeMessage) {
			if !tupleEqual(m.Namespace, h.Namespace) {
				slog.Warn("got unexpected subscription namespace",
					"received", m.Namespace,
					"expected", h.Namespace)
				err := w.Reject(0, "non-matching namespace")
				if err != nil {
					slog.Error("failed to reject subscription", "error", err)
				}
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
				json, err := json.Marshal(h.Catalog)
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
				slog.Info("got subtitle subscription", "track", st.Name)
				go PublishSubtitleTrack(ctx, w, st)
				return
			}

			// Check for video/audio tracks
			for _, track := range h.Catalog.Tracks {
				if m.Track == track.Name {
					err := w.Accept()
					if err != nil {
						slog.Error("failed to accept subscription", "error", err)
						return
					}
					slog.Info("got subscription", "track", track.Name)
					go PublishTrack(ctx, w, h.Asset, track.Name)
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
