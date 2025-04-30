package main

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
	"github.com/mengelbart/moqtransport"
	"github.com/mengelbart/qlog"
	"github.com/mengelbart/qlog/moqt"
)

type moqHandler struct {
	quic      bool
	addr      string
	namespace []string
	catalog   *internal.Catalog
	mux       *cmafMux
	outs      map[string]io.Writer
	logfh     io.Writer
	videoname string
	audioname string
}

func (h *moqHandler) runClient(ctx context.Context, wt bool, outs map[string]io.Writer) error {
	var conn moqtransport.Connection
	var err error
	if wt {
		conn, err = dialWebTransport(ctx, h.addr)
	} else {
		conn, err = dialQUIC(ctx, h.addr)
	}
	if err != nil {
		return err
	}
	h.outs = outs
	if outs["mux"] != nil {
		h.mux = newCmafMux(outs["mux"])
	}
	h.handle(ctx, conn)
	<-ctx.Done()
	slog.Info("end of runClient")
	return ctx.Err()
}

func (h *moqHandler) getHandler() moqtransport.Handler {
	return moqtransport.HandlerFunc(func(w moqtransport.ResponseWriter, r *moqtransport.Message) {
		switch r.Method {
		case moqtransport.MessageAnnounce:
			if !tupleEqual(r.Namespace, h.namespace) {
				slog.Warn("got unexpected announcement namespace",
					"received", r.Namespace,
					"expected", h.namespace)
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
		case moqtransport.MessageSubscribe:
			err := w.Reject(moqtransport.ErrorCodeSubscribeTrackDoesNotExist, "endpoint does not publish any tracks")
			if err != nil {
				slog.Error("failed to reject subscription", "error", err)
			}
			return
		}
	})
}

func (h *moqHandler) handle(ctx context.Context, conn moqtransport.Connection) {
	session := moqtransport.NewSession(conn.Protocol(), conn.Perspective(), 100)
	transport := &moqtransport.Transport{
		Conn:    conn,
		Handler: h.getHandler(),
		Qlogger: qlog.NewQLOGHandler(h.logfh, "MoQ QLOG", "MoQ QLOG", conn.Perspective().String(), moqt.Schema),
		Session: session,
	}
	err := transport.Run()
	if err != nil {
		slog.Error("MoQ Session initialization failed", "error", err)
		err = conn.CloseWithError(0, "session initialization error")
		if err != nil {
			slog.Error("failed to close connection", "error", err)
		}
		return
	}
	err = h.subscribeToCatalog(ctx, session, h.namespace)
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
	for _, track := range h.catalog.Tracks {
		// Select video track
		if strings.HasPrefix(track.MimeType, "video") {
			// If videoname is specified, match it as a substring of the track name
			if h.videoname != "" {
				if strings.Contains(track.Name, h.videoname) {
					videoTrack = track.Name
					slog.Info("selected video track based on substring match", "trackName", track.Name, "substring", h.videoname)
				}
			} else if videoTrack == "" {
				// If no videoname specified, use the first video track
				videoTrack = track.Name
			}

			// Initialize video track if it's the selected one
			if videoTrack == track.Name {
				if h.outs["video"] != nil {
					err = unpackWrite(track.InitData, h.outs["video"])
					if err != nil {
						slog.Error("failed to write init data", "error", err)
					}
				}
				if h.mux != nil {
					err = h.mux.addInit(track.InitData, "video")
					if err != nil {
						slog.Error("failed to add init data", "error", err)
					}
				}
			}
		}

		// Select audio track
		if strings.HasPrefix(track.MimeType, "audio") {
			// If audioname is specified, match it as a substring of the track name
			if h.audioname != "" {
				if strings.Contains(track.Name, h.audioname) {
					audioTrack = track.Name
					slog.Info("selected audio track based on substring match", "trackName", track.Name, "substring", h.audioname)
				}
			} else if audioTrack == "" {
				// If no audioname specified, use the first audio track
				audioTrack = track.Name
			}

			// Initialize audio track if it's the selected one
			if audioTrack == track.Name {
				if h.outs["audio"] != nil {
					err = unpackWrite(track.InitData, h.outs["audio"])
					if err != nil {
						slog.Error("failed to write init data", "error", err)
					}
				}
				if h.mux != nil {
					err = h.mux.addInit(track.InitData, "audio")
					if err != nil {
						slog.Error("failed to add init data", "error", err)
					}
				}
			}
		}
	}
	if videoTrack != "" {
		_, err := h.subscribeAndRead(ctx, session, h.namespace, videoTrack, "video")
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
		_, err := h.subscribeAndRead(ctx, session, h.namespace, audioTrack, "audio")
		if err != nil {
			slog.Error("failed to subscribe to audio track", "error", err)
			err = conn.CloseWithError(0, "internal error")
			if err != nil {
				slog.Error("failed to close connection", "error", err)
			}
			return
		}
	}
	if audioTrack == "" && videoTrack == "" {
		slog.Error("no matching tracks found")
		err = conn.CloseWithError(0, "no matching tracks found")
		if err != nil {
			slog.Error("failed to close connection", "error", err)
		}
		return
	}
	<-ctx.Done()
}

func (h *moqHandler) subscribeToCatalog(ctx context.Context, s *moqtransport.Session, namespace []string) error {
	rs, err := s.Subscribe(ctx, namespace, "catalog", "")
	if err != nil {
		return err
	}
	defer rs.Close()
	o, err := rs.ReadObject(ctx)
	if err != nil {
		if err == io.EOF {
			return nil
		}
		return err
	}

	err = json.Unmarshal(o.Payload, &h.catalog)
	if err != nil {
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
	return nil
}

func (h *moqHandler) subscribeAndRead(ctx context.Context, s *moqtransport.Session, namespace []string,
	trackname, mediaType string) (close func() error, err error) {
	rs, err := s.Subscribe(ctx, namespace, trackname, "")
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

			if h.mux != nil {
				err = h.mux.muxSample(o.Payload, mediaType)
				if err != nil {
					slog.Error("failed to mux sample", "error", err)
					return
				}
			}
			if h.outs[mediaType] != nil {
				_, err = h.outs[mediaType].Write(o.Payload)
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

func unpackWrite(initData string, w io.Writer) error {
	initBytes, err := base64.StdEncoding.DecodeString(initData)
	if err != nil {
		return err
	}
	_, err = w.Write(initBytes)
	if err != nil {
		return err
	}
	return nil
}
