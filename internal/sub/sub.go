package sub

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/Eyevinn/moqlivemock/internal"
	"github.com/Eyevinn/moqtransport"
	"github.com/Eyevinn/mp4ff/bits"
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
	Namespace    []string
	Outs         map[string]io.Writer
	Logfh        io.Writer
	VideoName    string
	AudioName    string
	SubsName     string
	UseFetch     bool
	AcceptAny    bool   // Accept any announced namespace
	Discover     bool   // Discovery mode: list namespaces and exit
	CatalogTrack string // Catalog track name (default "catalog")

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
	if h.CatalogTrack == "" {
		h.CatalogTrack = "catalog"
	}
	if h.Discover {
		return h.runDiscover(ctx, conn)
	}
	h.handle(ctx, conn)
	<-ctx.Done()
	slog.Info("end of RunWithConn")
	return ctx.Err()
}

func (h *Handler) runDiscover(ctx context.Context, conn moqtransport.Connection) error {
	session := &moqtransport.Session{
		Handler:             h.getHandler(),
		SubscribeHandler:    h.getSubscribeHandler(),
		InitialMaxRequestID: initialMaxRequestID,
		Qlogger:             qlog.NewQLOGHandler(h.Logfh, "MoQ QLOG", "MoQ QLOG", conn.Perspective().String(), moqt.Schema),
	}
	err := session.Run(conn)
	if err != nil {
		return fmt.Errorf("session init: %w", err)
	}
	slog.Info("connected, waiting for namespace announcements...")
	<-ctx.Done()
	return nil
}

func (h *Handler) getHandler() moqtransport.Handler {
	return moqtransport.HandlerFunc(func(w moqtransport.ResponseWriter, r *moqtransport.Message) {
		switch r.Method {
		case moqtransport.MessageAnnounce:
			if h.AcceptAny || h.Discover {
				slog.Info("discovered namespace", "namespace", r.Namespace)
				err := w.Accept()
				if err != nil {
					slog.Error("failed to accept announcement", "error", err)
				}
				return
			}
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

	if h.UseFetch {
		err = h.fetchCatalog(ctx, session, h.Namespace)
	} else {
		err = h.subscribeToCatalog(ctx, session, h.Namespace)
	}
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
	for i := range h.catalog.Tracks {
		track := &h.catalog.Tracks[i]
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
		if track.Role == "video" {
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
		if track.Role == "audio" {
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
		if track.Role == "subtitle" {
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
	rs, err := s.Subscribe(ctx, namespace, h.CatalogTrack)
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

	slog.Info("received catalog",
		"groupID", o.GroupID,
		"subGroupID", o.SubGroupID,
		"payloadLength", len(o.Payload),
	)
	slog.Debug("raw catalog payload", "data", string(o.Payload))
	err = json.Unmarshal(o.Payload, &h.catalog)
	if err != nil {
		slog.Warn("failed to parse catalog as CMSF, dumping raw", "error", err, "raw", string(o.Payload))
		rs.Close()
		// Still write raw catalog to output
		if h.Outs["catalog"] != nil {
			_, _ = h.Outs["catalog"].Write(o.Payload)
			_, _ = h.Outs["catalog"].Write([]byte("\n"))
		}
		return err
	}
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

func (h *Handler) fetchCatalog(ctx context.Context, s *moqtransport.Session, namespace []string) error {
	rt, err := s.Fetch(ctx, namespace, h.CatalogTrack)
	if err != nil {
		return err
	}
	defer rt.Close()

	o, err := rt.ReadObject(ctx)
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
	slog.Info("fetched catalog",
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
	var moov *mp4.MoovBox
	if track.Packaging == "compressed-cmaf" {
		if h.cenc != nil && h.cenc.ProtectedMoov != nil && h.cenc.ProtectedMoov[trackname] != nil {
			moov = h.cenc.ProtectedMoov[trackname]
		} else {
			initData, err := base64.StdEncoding.DecodeString(track.InitData)
			if err != nil {
				return nil, fmt.Errorf("failed to decode init data for track %s: %w", trackname, err)
			}
			f, err := mp4.DecodeFileSR(bits.NewFixedSliceReader(initData))
			if err != nil {
				return nil, fmt.Errorf("failed to parse init data for track %s: %w", trackname, err)
			}
			if f.Init == nil || f.Init.Moov == nil {
				return nil, fmt.Errorf("missing moov in init data for track %s", trackname)
			}
			moov = f.Init.Moov
		}
	}
	go func() {
		var deltaDecompressor internal.MoofDeltaDecompressor
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
				deltaDecompressor = internal.MoofDeltaDecompressor{}
				slog.Info("group start",
					"track", trackname,
					"groupID", o.GroupID,
					"subGroupID", o.SubGroupID,
					"payloadLength", len(o.Payload))
			} else {
				slog.Debug("object",
					"track", trackname,
					"objectID", o.ObjectID,
					"groupID", o.GroupID,
					"subGroupID", o.SubGroupID,
					"payloadLength", len(o.Payload))
			}

			if track.Packaging == "compressed-cmaf" {
				o.Payload, err = decompressCompressedCMAFObject(o.Payload, uint32(o.GroupID), moov, &deltaDecompressor)
				if err != nil {
					slog.Error("failed to decompress compressed CMAF object",
						"track", trackname,
						"groupID", o.GroupID,
						"objectID", o.ObjectID,
						"error", err)
					return
				}
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

func decompressCompressedCMAFObject(payload []byte, seqnum uint32,
	moov *mp4.MoovBox, decompressor *internal.MoofDeltaDecompressor) ([]byte, error) {

	headerID, n := binary.Varint(payload)
	if n <= 0 {
		return nil, fmt.Errorf("invalid compressed CMAF header")
	}
	pos := n

	locPayloadLength, n := binary.Varint(payload[pos:])
	if n <= 0 {
		return nil, fmt.Errorf("invalid compressed CMAF LOC payload length")
	}
	pos += n
	if locPayloadLength < 0 || pos+int(locPayloadLength) > len(payload) {
		return nil, fmt.Errorf("compressed CMAF LOC payload exceeds object length")
	}
	locPayload := payload[pos : pos+int(locPayloadLength)]
	mdatData := payload[pos+int(locPayloadLength):]

	moof, err := decompressor.DecompressMoof(headerID, locPayload, seqnum, moov)
	if err != nil {
		return nil, err
	}

	frag := mp4.NewFragment()
	frag.AddChild(moof)
	frag.AddChild(&mp4.MdatBox{Data: mdatData})

	sw := bits.NewFixedSliceWriter(int(frag.Size()))
	err = frag.EncodeSW(sw)
	if err != nil {
		return nil, fmt.Errorf("failed to encode decompressed fragment: %w", err)
	}

	return sw.Bytes(), nil
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
