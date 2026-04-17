package sub

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/Eyevinn/moqtransport"
	"github.com/Eyevinn/moqtransport/moqmi"
)

// IsMoqMINamespace reports whether the given namespace tuple identifies a
// moq-mi (MoQ Media Interop) namespace, by convention on the first segment.
func IsMoqMINamespace(ns []string) bool {
	if len(ns) == 0 {
		return false
	}
	return ns[0] == "moq-mi" || strings.HasPrefix(ns[0], "moq-mi/")
}

// handleMoqMI subscribes directly to a fixed set of moq-mi track names
// (video0, audio0) without fetching a catalog, parses the moq-mi extension
// headers on each object, and writes raw payloads to configured outputs.
func (h *Handler) handleMoqMI(ctx context.Context, conn moqtransport.Connection) {
	session, err := h.startSession(conn)
	if err != nil {
		slog.Error("moq-mi: session init failed", "error", err)
		_ = conn.CloseWithError(0, "session initialization error")
		return
	}

	slog.Info("moq-mi: subscribing to fixed track names", "namespace", h.Namespace)

	anySubscribed := false
	if h.Outs["video"] != nil || h.Outs["mux"] == nil {
		// Always subscribe to video0 if video output is requested OR if no
		// output is configured at all (so we still exercise the receive path).
		if _, err := h.subscribeMoqMI(ctx, session, "video0", "video"); err != nil {
			slog.Error("moq-mi: video0 subscribe failed", "error", err)
		} else {
			anySubscribed = true
		}
	}
	if h.Outs["audio"] != nil || h.Outs["mux"] == nil {
		if _, err := h.subscribeMoqMI(ctx, session, "audio0", "audio"); err != nil {
			slog.Error("moq-mi: audio0 subscribe failed", "error", err)
		} else {
			anySubscribed = true
		}
	}
	if !anySubscribed {
		slog.Error("moq-mi: no tracks subscribed")
		_ = conn.CloseWithError(0, "no tracks")
		return
	}
	<-ctx.Done()
}

// subscribeMoqMI subscribes to a moq-mi track and reads objects in a goroutine.
// Each object's extension headers are parsed and logged; the raw payload is
// written to h.Outs[mediaType] when configured.
func (h *Handler) subscribeMoqMI(ctx context.Context, s *moqtransport.Session,
	trackName, mediaType string) (func() error, error) {
	rs, err := s.Subscribe(ctx, h.Namespace, trackName)
	if err != nil {
		return nil, fmt.Errorf("subscribe %s: %w", trackName, err)
	}
	slog.Info("moq-mi: subscribed", "track", trackName, "mediaType", mediaType)
	out := h.Outs[mediaType]
	go func() {
		var lastSeq uint64
		var haveSeq bool
		for {
			o, err := rs.ReadObject(ctx)
			if err != nil {
				if err == io.EOF {
					slog.Info("moq-mi: read EOF", "track", trackName)
				} else {
					slog.Warn("moq-mi: read ended", "track", trackName, "error", err)
				}
				return
			}
			logMoqMIObject(trackName, o, &lastSeq, &haveSeq)
			if out != nil {
				if _, werr := out.Write(o.Payload); werr != nil {
					slog.Error("moq-mi: write payload failed", "track", trackName, "error", werr)
					return
				}
			}
		}
	}()
	return rs.Close, nil
}

// logMoqMIObject parses moqmi extension headers on a received object and logs
// the media type and per-codec metadata. seqState tracks the last SeqID seen so
// gaps can be flagged.
func logMoqMIObject(trackName string, o *moqtransport.Object, lastSeq *uint64, haveSeq *bool) {
	mt, ok := moqmi.MediaType(o.ExtensionHeaders)
	if !ok {
		slog.Warn("moq-mi: object missing media-type header",
			"track", trackName, "group", o.GroupID, "object", o.ObjectID)
		return
	}
	switch mt {
	case moqmi.MediaTypeVideoH264AVCC:
		meta, present, err := moqmi.ReadVideoMetadata(o.ExtensionHeaders)
		if err != nil || !present {
			slog.Warn("moq-mi: video metadata missing/invalid",
				"track", trackName, "err", err, "present", present)
			return
		}
		extradata, hasExtra := moqmi.ReadVideoExtradata(o.ExtensionHeaders)
		seqGap := seqDelta(meta.SeqID, lastSeq, haveSeq)
		slog.Info("moq-mi: video object",
			"track", trackName, "group", o.GroupID, "object", o.ObjectID,
			"seq", meta.SeqID, "pts", meta.PTS, "dts", meta.DTS,
			"timebase", meta.Timebase, "dur", meta.Duration,
			"wallclockMS", meta.WallclockMS,
			"payloadLen", len(o.Payload),
			"extradataLen", len(extradata), "hasExtradata", hasExtra,
			"seqGap", seqGap)
	case moqmi.MediaTypeAudioAACLC:
		meta, present, err := moqmi.ReadAudioAACMetadata(o.ExtensionHeaders)
		if err != nil || !present {
			slog.Warn("moq-mi: aac metadata missing/invalid",
				"track", trackName, "err", err, "present", present)
			return
		}
		seqGap := seqDelta(meta.SeqID, lastSeq, haveSeq)
		slog.Info("moq-mi: aac object",
			"track", trackName, "group", o.GroupID, "object", o.ObjectID,
			"seq", meta.SeqID, "pts", meta.PTS, "timebase", meta.Timebase,
			"sampleFreq", meta.SampleFreq, "channels", meta.NumChannels,
			"dur", meta.Duration, "wallclockMS", meta.WallclockMS,
			"payloadLen", len(o.Payload), "seqGap", seqGap)
	case moqmi.MediaTypeAudioOpus:
		meta, present, err := moqmi.ReadAudioOpusMetadata(o.ExtensionHeaders)
		if err != nil || !present {
			slog.Warn("moq-mi: opus metadata missing/invalid",
				"track", trackName, "err", err, "present", present)
			return
		}
		seqGap := seqDelta(meta.SeqID, lastSeq, haveSeq)
		slog.Info("moq-mi: opus object",
			"track", trackName, "group", o.GroupID, "object", o.ObjectID,
			"seq", meta.SeqID, "pts", meta.PTS, "timebase", meta.Timebase,
			"sampleFreq", meta.SampleFreq, "channels", meta.NumChannels,
			"dur", meta.Duration, "wallclockMS", meta.WallclockMS,
			"payloadLen", len(o.Payload), "seqGap", seqGap)
	case moqmi.MediaTypeUTF8Text:
		meta, present, err := moqmi.ReadTextMetadata(o.ExtensionHeaders)
		if err != nil || !present {
			slog.Warn("moq-mi: text metadata missing/invalid",
				"track", trackName, "err", err, "present", present)
			return
		}
		slog.Info("moq-mi: text object",
			"track", trackName, "group", o.GroupID, "object", o.ObjectID,
			"seq", meta.SeqID, "payloadLen", len(o.Payload))
	default:
		slog.Warn("moq-mi: unknown media type",
			"track", trackName, "mediaType", mt)
	}
}

// seqDelta returns the gap between a newly-observed seqID and the previous one.
// Returns 1 for the expected next value (no gap), 0 for the first sample.
func seqDelta(seq uint64, last *uint64, have *bool) uint64 {
	if !*have {
		*have = true
		*last = seq
		return 0
	}
	delta := seq - *last
	*last = seq
	return delta
}
