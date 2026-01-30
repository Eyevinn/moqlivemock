package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/Eyevinn/moqlivemock/internal"
	"github.com/Eyevinn/mp4ff/bits"
	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/mengelbart/moqtransport"
	"github.com/mengelbart/qlog"
	"github.com/mengelbart/qlog/moqt"
)

const (
	initialMaxRequestID = 64
)

type CENC struct {
	Key         []byte
	DecryptInfo map[string]mp4.DecryptInfo // keyed by track name
}

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
	subsname  string
	cenc      *CENC
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
		}
	})
}

func (h *moqHandler) getSubscribeHandler() moqtransport.SubscribeHandler {
	return moqtransport.SubscribeHandlerFunc(
		func(w *moqtransport.SubscribeResponseWriter, m *moqtransport.SubscribeMessage) {
			err := w.Reject(moqtransport.ErrorCodeSubscribeTrackDoesNotExist, "endpoint does not publish any tracks")
			if err != nil {
				slog.Error("failed to reject subscription", "error", err)
			}
		})
}

func (h *moqHandler) handle(ctx context.Context, conn moqtransport.Connection) {
	session := &moqtransport.Session{
		Handler:             h.getHandler(),
		SubscribeHandler:    h.getSubscribeHandler(),
		InitialMaxRequestID: initialMaxRequestID,
		Qlogger:             qlog.NewQLOGHandler(h.logfh, "MoQ QLOG", "MoQ QLOG", conn.Perspective().String(), moqt.Schema),
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
	subsTrack := ""
	for _, track := range h.catalog.Tracks {
		//If track is encrypted, the InitData needs to be adjusted
		if h.cenc != nil {
			track.InitData, err = h.decryptInit(track.InitData, track.Name)
			if err != nil {
				slog.Error("failed to decrypt init data", "error", err)
			}
		}
		// Select video track
		if strings.HasPrefix(track.MimeType, "video") {
			// If videoname is specified, match it as a substring of the track name
			if h.videoname != "" {
				if videoTrack == "" && strings.Contains(track.Name, h.videoname) {
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
				if audioTrack == "" && strings.Contains(track.Name, h.audioname) {
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

		// Select subtitle track (wvtt or stpp codec)
		isSubtitle := track.Codec == "wvtt" || strings.HasPrefix(track.Codec, "stpp")
		if isSubtitle {
			// If subsname is specified, match it as a substring of the track name
			if h.subsname != "" {
				if subsTrack == "" && strings.Contains(track.Name, h.subsname) {
					subsTrack = track.Name
					slog.Info("selected subtitle track based on substring match", "trackName", track.Name, "substring", h.subsname)
				}
			} else if subsTrack == "" && h.outs["subs"] != nil {
				// If no subsname specified but subs output requested, use the first subtitle track
				subsTrack = track.Name
			}

			// Initialize subtitle track if it's the selected one
			if subsTrack == track.Name && h.outs["subs"] != nil {
				err = unpackWrite(track.InitData, h.outs["subs"])
				if err != nil {
					slog.Error("failed to write subtitle init data", "error", err)
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
	if subsTrack != "" {
		_, err := h.subscribeAndRead(ctx, session, h.namespace, subsTrack, "subs")
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

func (h *moqHandler) subscribeToCatalog(ctx context.Context, s *moqtransport.Session, namespace []string) error {
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
	if h.outs["catalog"] != nil {
		indented, err := json.MarshalIndent(h.catalog, "", "  ")
		if err != nil {
			slog.Error("failed to marshal catalog", "error", err)
		} else {
			_, err = h.outs["catalog"].Write(indented)
			if err != nil {
				slog.Error("failed to write catalog", "error", err)
			}
			_, err = h.outs["catalog"].Write([]byte("\n"))
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
			if h.outs["catalog"] != nil {
				indented, err := json.MarshalIndent(&cat, "", "  ")
				if err != nil {
					slog.Error("failed to marshal catalog update", "error", err)
				} else {
					_, err = h.outs["catalog"].Write(indented)
					if err != nil {
						slog.Error("failed to write catalog update", "error", err)
					}
					_, err = h.outs["catalog"].Write([]byte("\n"))
					if err != nil {
						slog.Error("failed to write catalog update newline", "error", err)
					}
				}
			}
		}
	}()

	return nil
}

func (h *moqHandler) subscribeAndRead(ctx context.Context, s *moqtransport.Session, namespace []string,
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
				}
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

func (h *moqHandler) decryptInit(initData string, trackName string) (string, error) {
	initDataBytes, err := base64.StdEncoding.DecodeString(initData)
	if err != nil {
		return "", err
	}
	sr := bits.NewFixedSliceReader(initDataBytes)
	f, err := mp4.DecodeFileSR(sr)
	if err != nil {
		return "", err
	}
	if f.Init == nil {
		return "", fmt.Errorf("no init segment in initData")
	}
	decryptInfo, err := mp4.DecryptInit(f.Init)
	if err != nil {
		return "", fmt.Errorf("unable to decrypt init")
	}
	h.cenc.DecryptInfo[trackName] = decryptInfo
	sw := bits.NewFixedSliceWriter(int(f.Init.Size()))
	err = f.Init.EncodeSW(sw)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(sw.Bytes()), nil
}

func (h *moqHandler) decryptPayload(payload []byte, trackName string) ([]byte, error) {
	decryptInfo, ok := h.cenc.DecryptInfo[trackName]
	if !ok {
		return nil, fmt.Errorf("no decrypt info for track %s", trackName)
	}

	bytesReader := bytes.NewReader(payload)
	var pos uint64 = 0
	moofBox, err := mp4.DecodeBox(pos, bytesReader)
	if err != nil {
		return nil, fmt.Errorf("unable to decode moof: %w", err)
	}
	moof, ok := moofBox.(*mp4.MoofBox)
	if !ok {
		return nil, fmt.Errorf("expected moof box, got %T", moofBox)
	}
	pos += moof.Size()
	mdatBox, err := mp4.DecodeBox(pos, bytesReader)
	if err != nil {
		return nil, fmt.Errorf("unable to decode mdat: %w", err)
	}
	mdat, ok := mdatBox.(*mp4.MdatBox)
	if !ok {
		return nil, fmt.Errorf("expected mdat box, got %T", mdatBox)
	}

	decodedFrag := mp4.NewFragment()
	decodedFrag.AddChild(moof)
	decodedFrag.AddChild(mdat)

	err = mp4.DecryptFragment(decodedFrag, decryptInfo, h.cenc.Key)
	if err != nil {
		return nil, fmt.Errorf("unable to decrypt fragment: %w", err)
	}
	encSize := decodedFrag.Size()
	encSw := bits.NewFixedSliceWriter(int(encSize))
	err = decodedFrag.EncodeSW(encSw)
	if err != nil {
		return nil, fmt.Errorf("unable to encode decrypted fragment: %w", err)
	}
	return encSw.Bytes(), nil
}
