package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log"
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
}

func (h *moqHandler) runClient(ctx context.Context, wt bool) error {
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
	h.handle(ctx, conn)
	<-ctx.Done()
	log.Printf("end of runClient")
	return ctx.Err()
}

func (h *moqHandler) getHandler() moqtransport.Handler {
	return moqtransport.HandlerFunc(func(w moqtransport.ResponseWriter, r *moqtransport.Message) {
		switch r.Method {
		case moqtransport.MessageAnnounce:
			if !tupleEqual(r.Namespace, h.namespace) {
				log.Printf("got unexpected announcement namespace: %v, expected %v", r.Namespace, h.namespace)
				err := w.Reject(0, "non-matching namespace")
				if err != nil {
					log.Printf("failed to reject announcement: %v", err)
				}
				return
			}
			err := w.Accept()
			if err != nil {
				log.Printf("failed to accept announcement: %v", err)
				return
			}
		case moqtransport.MessageSubscribe:
			err := w.Reject(moqtransport.ErrorCodeSubscribeTrackDoesNotExist, "endpoint does not publish any tracks")
			if err != nil {
				log.Printf("failed to reject subscription: %v", err)
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
		Qlogger: qlog.NewQLOGHandler(os.Stdout, "MoQ QLOG", "MoQ QLOG", conn.Perspective().String(), moqt.Schema),
		Session: session,
	}
	err := transport.Run()
	if err != nil {
		log.Printf("MoQ Session initialization failed: %v", err)
		err = conn.CloseWithError(0, "session initialization error")
		if err != nil {
			log.Printf("failed to close connection: %v", err)
		}
		return
	}
	err = h.subscribeToCatalog(ctx, session, h.namespace)
	if err != nil {
		log.Printf("failed to subscribe to catalog: %v", err)
		err = conn.CloseWithError(0, "internal error")
		if err != nil {
			log.Printf("failed to close connection: %v", err)
		}
		return
	}
	videoTrack := ""
	audioTrack := ""
	for _, track := range h.catalog.Tracks {
		if videoTrack == "" {
			if strings.HasPrefix(track.MimeType, "video") {
				videoTrack = track.Name
			}
		}
		if audioTrack == "" {
			if strings.HasPrefix(track.MimeType, "audio") {
				audioTrack = track.Name
			}
		}
	}
	if videoTrack != "" {
		_, err := h.subscribeAndRead(ctx, session, h.namespace, videoTrack)
		if err != nil {
			log.Printf("failed to subscribe to video track: %v", err)
			err = conn.CloseWithError(0, "internal error")
			if err != nil {
				log.Printf("failed to close connection: %v", err)
			}
			return
		}
	}
	if audioTrack != "" {
		_, err := h.subscribeAndRead(ctx, session, h.namespace, audioTrack)
		if err != nil {
			log.Printf("failed to subscribe to audio track: %v", err)
			err = conn.CloseWithError(0, "internal error")
			if err != nil {
				log.Printf("failed to close connection: %v", err)
			}
			return
		}
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
	log.Printf("got catalog %v/%v/%v of length %v\n", o.ObjectID, o.GroupID, o.SubGroupID,
		len(o.Payload))
	return nil
}

func (h *moqHandler) subscribeAndRead(ctx context.Context, s *moqtransport.Session, namespace []string,
	trackname string) (close func() error, err error) {
	rs, err := s.Subscribe(ctx, namespace, trackname, "")
	if err != nil {
		return nil, err
	}
	track := h.catalog.GetTrackByName(trackname)
	if track == nil {
		return nil, fmt.Errorf("track %s not found", trackname)
	}
	outName := trackname + ".mp4"
	out, err := os.Create(outName)
	if err != nil {
		return nil, err
	}
	initData := track.InitData
	if initData != "" {
		initBytes, err := base64.StdEncoding.DecodeString(initData)
		if err != nil {
			return nil, err
		}
		_, err = out.Write(initBytes)
		if err != nil {
			return nil, err
		}
	}
	go func() {
		for {
			o, err := rs.ReadObject(ctx)
			if err != nil {
				if err == io.EOF {
					log.Printf("got last object")
					return
				}
				return
			}
			log.Printf("got object %v/%v/%v of length %v\n", o.ObjectID, o.GroupID, o.SubGroupID,
				len(o.Payload))
			_, err = out.Write(o.Payload)
			if err != nil {
				return
			}
		}
	}()
	cleanup := func() error {
		log.Printf("cleanup: closing subscription to track %v/%v", namespace, trackname)
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
