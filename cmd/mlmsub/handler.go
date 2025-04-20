package main

import (
	"context"
	"io"
	"log"
	"os"
	"time"

	"github.com/mengelbart/moqtransport"
	"github.com/mengelbart/qlog"
	"github.com/mengelbart/qlog/moqt"
)

type moqHandler struct {
	quic      bool
	addr      string
	namespace []string
	trackname string
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
	close, err := h.subscribeAndRead(ctx, session, h.namespace, h.trackname)
	if err != nil {
		log.Printf("failed to subscribe to track: %v", err)
		err = conn.CloseWithError(0, "internal error")
		if err != nil {
			log.Printf("failed to close connection: %v", err)
		}
		return
	}
	select {
	case <-ctx.Done():
		log.Printf("ctx done")
	case <-time.After(4 * time.Second):
		if close != nil {
			err := close()
			if err != nil {
				log.Printf("failed to close subscription in cleanup: %v", err)
			}
		}
	}
	<-ctx.Done()
}

func (h *moqHandler) subscribeAndRead(ctx context.Context, s *moqtransport.Session, namespace []string, trackname string) (close func() error, err error) {
	rs, err := s.Subscribe(ctx, namespace, trackname, "")
	if err != nil {
		return nil, err
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
			log.Printf("got object %v/%v/%v of length %v: %v\n", o.ObjectID, o.GroupID, o.SubGroupID,
				len(o.Payload), string(o.Payload))
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
