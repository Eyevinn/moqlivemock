package sub

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/Eyevinn/moqlivemock/internal"
	"github.com/Eyevinn/moqtransport"
	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/mengelbart/qlog"
	"github.com/mengelbart/qlog/moqt"
)

const (
	initialMaxRequestID = 64
)

// Handler handles MoQ subscriber sessions. It subscribes to a catalog,
// selects tracks, and reads media data.
type Handler struct {
	Namespace []string
	Outs      map[string]io.Writer
	Logfh     io.Writer
	VideoName string
	AudioName string
	SubsName  string

	catalog *internal.Catalog
	mux     *CmafMux
	cenc    *CENC
}

// RunWithConn sets up the mux (if Outs["mux"] is set) and runs the subscriber
// session on the given connection.
func (h *Handler) RunWithConn(ctx context.Context, conn moqtransport.Connection) error {
	if h.Outs["mux"] != nil {
		h.mux = NewCmafMux(h.Outs["mux"])
	}
	h.handle(ctx, conn)
	<-ctx.Done()
	slog.Info("end of RunWithConn")
	return ctx.Err()
}

func (h *Handler) getHandler() moqtransport.Handler {
	return moqtransport.HandlerFunc(func(w moqtransport.ResponseWriter, r *moqtransport.Message) {
		switch r.Method {
		case moqtransport.MessageAnnounce:
			if !tupleEqual(r.Namespace, h.Namespace) {
				slog.Warn("got unexpected announcement namespace",
					"received", r.Namespace,
					"expected", h.Namespace)
				err := w.Reject(0, "non-matching namespace")
				if err != nil {
					slog.Error("failed to reject announcement", "error", err)
				}
				return
			}
			err := w.Accept()
			if err != nil {
				slog.Error("failed to accept announcement", "error", err)
				return
			}
		}
	})
}

func (h *Handler) getSubscribeHandler() moqtransport.SubscribeHandler {
	return moqtransport.SubscribeHandlerFunc(
		func(w *moqtransport.SubscribeResponseWriter, m *moqtransport.SubscribeMessage) {
			err := w.Reject(moqtransport.ErrorCodeSubscribeTrackDoesNotExist, "endpoint does not publish any tracks")
			if err != nil {
				slog.Error("failed to reject subscription", "error", err)
			}
		})
}

func (h *Handler) handle(ctx context.Context, conn moqtransport.Connection) {
	session := &moqtransport.Session{
		Handler:             h.getHandler(),
		SubscribeHandler:    h.getSubscribeHandler(),
		InitialMaxRequestID: initialMaxRequestID,
		Qlogger:             qlog.NewQLOGHandler(h.Logfh, "MoQ QLOG", "MoQ QLOG", conn.Perspective().String(), moqt.Schema),
	}
	err := session.Run(conn)
	if err != nil {
		slog.Error("MoQ Session initialization failed", "error", err)
		err = conn.CloseWithError(0, "session initialization error")
		if err != nil {
			slog.Error("failed to close connection", "error", err)
		}
		return
	}
	err = h.subscribeToCatalog(ctx, session, h.Namespace)
	if err != nil {
		slog.Error("failed to subscribe to catalog", "error", err)
		err = conn.CloseWithError(0, "internal error")
		if err != nil {
			slog.Error("failed to close connection", "error", err)
		}
		return
	}
	videoTrack := ""
	audioTrack := ""
	subsTrack := ""
	for _, track := range h.catalog.Tracks {
		// If track is encrypted, the InitData needs to be adjusted
		if len(track.ContentProtectionRefIDs) > 0 {
			if h.cenc == nil {
				h.cenc = &CENC{
					DecryptInfo: make(map[string]mp4.DecryptInfo),
				}
			}
			err = h.decryptInit(track)
			if err != nil {
				slog.Error("failed to decrypt init data", "error", err)
			}
		}
		// Select video track
		if strings.HasPrefix(track.MimeType, "video") {
			if h.VideoName != "" {
				if videoTrack == "" && strings.Contains(track.Name, h.VideoName) {
					videoTrack = track.Name
					slog.Info("selected video track based on substring match", "trackName", track.Name, "substring", h.VideoName)
				}
			} else if videoTrack == "" {
				videoTrack = track.Name
			}

			if videoTrack == track.Name {
				if h.Outs["video"] != nil {
					err = unpackWrite(track.InitData, h.Outs["video"])
					if err != nil {
						slog.Error("failed to write init data", "error", err)
					}
				}
				if h.mux != nil {
					err = h.mux.AddInit(track.InitData, "video")
					if err != nil {
						slog.Error("failed to add init data", "error", err)
					}
				}
			}
		}

		// Select audio track
		if strings.HasPrefix(track.MimeType, "audio") {
			if h.AudioName != "" {
				if audioTrack == "" && strings.Contains(track.Name, h.AudioName) {
					audioTrack = track.Name
					slog.Info("selected audio track based on substring match", "trackName", track.Name, "substring", h.AudioName)
				}
			} else if audioTrack == "" {
				audioTrack = track.Name
			}

			if audioTrack == track.Name {
				if h.Outs["audio"] != nil {
					err = unpackWrite(track.InitData, h.Outs["audio"])
					if err != nil {
						slog.Error("failed to write init data", "error", err)
					}
				}
				if h.mux != nil {
					err = h.mux.AddInit(track.InitData, "audio")
					if err != nil {
						slog.Error("failed to add init data", "error", err)
					}
				}
			}
		}

		// Select subtitle track (wvtt or stpp codec)
		if track.MimeType == "application/mp4" {
			if h.SubsName != "" {
				if subsTrack == "" && strings.Contains(track.Name, h.SubsName) {
					subsTrack = track.Name
					slog.Info("selected subtitle track based on substring match", "trackName", track.Name, "substring", h.SubsName)
				}
			} else if subsTrack == "" && h.Outs["subs"] != nil {
				subsTrack = track.Name
			}

			if subsTrack == track.Name && h.Outs["subs"] != nil {
				err = unpackWrite(track.InitData, h.Outs["subs"])
				if err != nil {
					slog.Error("failed to write subtitle init data", "error", err)
				}
			}
		}
	}
	if videoTrack != "" {
		_, err := h.subscribeAndRead(ctx, session, h.Namespace, videoTrack, "video")
		if err != nil {
			slog.Error("failed to subscribe to video track", "error", err)
			err = conn.CloseWithError(0, "internal error")
			if err != nil {
				slog.Error("failed to close connection", "error", err)
			}
			return
		}
	}
	if audioTrack != "" {
		_, err := h.subscribeAndRead(ctx, session, h.Namespace, audioTrack, "audio")
		if err != nil {
			slog.Error("failed to subscribe to audio track", "error", err)
			err = conn.CloseWithError(0, "internal error")
			if err != nil {
				slog.Error("failed to close connection", "error", err)
			}
			return
		}
	}
	if subsTrack != "" {
		_, err := h.subscribeAndRead(ctx, session, h.Namespace, subsTrack, "subs")
		if err != nil {
			slog.Error("failed to subscribe to subtitle track", "error", err)
			err = conn.CloseWithError(0, "internal error")
			if err != nil {
				slog.Error("failed to close connection", "error", err)
			}
			return
		}
	}
	if audioTrack == "" && videoTrack == "" && subsTrack == "" {
		slog.Error("no matching tracks found")
		err = conn.CloseWithError(0, "no matching tracks found")
		if err != nil {
			slog.Error("failed to close connection", "error", err)
		}
		return
	}
	<-ctx.Done()
}

func (h *Handler) subscribeToCatalog(ctx context.Context, s *moqtransport.Session, namespace []string) error {
	rs, err := s.Subscribe(ctx, namespace, "catalog")
	if err != nil {
		return err
	}
	o, err := rs.ReadObject(ctx)
	if err != nil {
		rs.Close()
		if err == io.EOF {
			return nil
		}
		return err
	}

	err = json.Unmarshal(o.Payload, &h.catalog)
	if err != nil {
		rs.Close()
		return err
	}
	slog.Info("received catalog",
		"groupID", o.GroupID,
		"subGroupID", o.SubGroupID,
		"payloadLength", len(o.Payload),
	)
	if slog.Default().Enabled(context.Background(), slog.LevelInfo) {
		fmt.Fprintf(os.Stderr, "catalog: %s\n", h.catalog.String())
	}
	if h.Outs["catalog"] != nil {
		indented, err := json.MarshalIndent(h.catalog, "", "  ")
		if err != nil {
			slog.Error("failed to marshal catalog", "error", err)
		} else {
			_, err = h.Outs["catalog"].Write(indented)
			if err != nil {
				slog.Error("failed to write catalog", "error", err)
			}
			_, err = h.Outs["catalog"].Write([]byte("\n"))
			if err != nil {
				slog.Error("failed to write catalog newline", "error", err)
			}
		}
	}

	// Continue reading catalog updates in background
	go func() {
		defer rs.Close()
		for {
			o, err := rs.ReadObject(ctx)
			if err != nil {
				if err != io.EOF {
					slog.Debug("catalog subscription ended", "error", err)
				}
				return
			}
			var cat internal.Catalog
			err = json.Unmarshal(o.Payload, &cat)
			if err != nil {
				slog.Error("failed to unmarshal catalog update", "error", err)
				continue
			}
			h.catalog = &cat
			slog.Info("received catalog update",
				"groupID", o.GroupID,
				"subGroupID", o.SubGroupID,
				"payloadLength", len(o.Payload),
			)
			if slog.Default().Enabled(context.Background(), slog.LevelInfo) {
				fmt.Fprintf(os.Stderr, "catalog update: %s\n", h.catalog.String())
			}
			if h.Outs["catalog"] != nil {
				indented, err := json.MarshalIndent(&cat, "", "  ")
				if err != nil {
					slog.Error("failed to marshal catalog update", "error", err)
				} else {
					_, err = h.Outs["catalog"].Write(indented)
					if err != nil {
						slog.Error("failed to write catalog update", "error", err)
					}
					_, err = h.Outs["catalog"].Write([]byte("\n"))
					if err != nil {
						slog.Error("failed to write catalog update newline", "error", err)
					}
				}
			}
		}
	}()

	return nil
}

func (h *Handler) subscribeAndRead(ctx context.Context, s *moqtransport.Session, namespace []string,
	trackname, mediaType string) (close func() error, err error) {
	rs, err := s.Subscribe(ctx, namespace, trackname)
	if err != nil {
		return nil, err
	}
	track := h.catalog.GetTrackByName(trackname)
	if track == nil {
		return nil, fmt.Errorf("track %s not found", trackname)
	}
	go func() {
		for {
			o, err := rs.ReadObject(ctx)
			if err != nil {
				if err == io.EOF {
					slog.Info("got last object")
					return
				}
				return
			}
			if o.ObjectID == 0 {
				slog.Info("group start", "track", trackname, "groupID", o.GroupID, "payloadLength", len(o.Payload))
			} else {
				slog.Debug("object",
					"track", trackname,
					"objectID", o.ObjectID,
					"groupID", o.GroupID,
					"payloadLength", len(o.Payload))
			}
			if h.cenc != nil {
				o.Payload, err = h.decryptPayload(o.Payload, trackname)
				if err != nil {
					slog.Error("failed to decrypt payload", "error", err)
					return
				}
			}

			if h.mux != nil {
				err = h.mux.MuxSample(o.Payload, mediaType)
				if err != nil {
					slog.Error("failed to mux sample", "error", err)
					return
				}
			}
			if h.Outs[mediaType] != nil {
				_, err = h.Outs[mediaType].Write(o.Payload)
				if err != nil {
					slog.Error("failed to write sample", "error", err)
					return
				}
			}
		}
	}()
	cleanup := func() error {
		slog.Info("cleanup: closing subscription to track", "namespace", namespace, "trackname", trackname)
		return rs.Close()
	}
	return cleanup, nil
}

func unpackWrite(initData string, w io.Writer) error {
	initBytes, err := base64.StdEncoding.DecodeString(initData)
	if err != nil {
		return err
	}
	_, err = w.Write(initBytes)
	return err
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
