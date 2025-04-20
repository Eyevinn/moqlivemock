package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/mengelbart/moqtransport"
	"github.com/mengelbart/moqtransport/quicmoq"
	"github.com/mengelbart/moqtransport/webtransportmoq"
	"github.com/mengelbart/qlog"
	"github.com/mengelbart/qlog/moqt"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/webtransport-go"
)

type moqHandler struct {
	addr       string
	tlsConfig  *tls.Config
	namespace  []string
	trackname  string
	publishers chan moqtransport.Publisher
}

func (h *moqHandler) runServer(ctx context.Context) error {
	listener, err := quic.ListenAddr(h.addr, h.tlsConfig, &quic.Config{
		EnableDatagrams: true,
	})
	if err != nil {
		return err
	}
	wt := webtransport.Server{
		H3: http3.Server{
			Addr:      h.addr,
			TLSConfig: h.tlsConfig,
		},
	}
	go h.setupDateTrack()
	http.HandleFunc("/moq", func(w http.ResponseWriter, r *http.Request) {
		session, err := wt.Upgrade(w, r)
		if err != nil {
			log.Printf("upgrading to webtransport failed: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		h.handle(webtransportmoq.NewServer(session))
	})
	for {
		conn, err := listener.Accept(ctx)
		if err != nil {
			return err
		}
		if conn.ConnectionState().TLS.NegotiatedProtocol == "h3" {
			go serveQUICConn(&wt, conn)
		}
		if conn.ConnectionState().TLS.NegotiatedProtocol == "moq-00" {
			go h.handle(quicmoq.NewServer(conn))
		}
	}
}

func serveQUICConn(wt *webtransport.Server, conn quic.Connection) {
	err := wt.ServeQUICConn(conn)
	if err != nil {
		log.Printf("failed to serve QUIC connection: %v", err)
	}
}

func (h *moqHandler) getHandler() moqtransport.Handler {
	return moqtransport.HandlerFunc(func(w moqtransport.ResponseWriter, r *moqtransport.Message) {
		switch r.Method {
		case moqtransport.MessageAnnounce:
			log.Printf("got unexpected announcement: %v", r.Namespace)
			err := w.Reject(0, "date doesn't take announcements")
			if err != nil {
				log.Printf("failed to reject announcement: %v", err)
			}
			return
		case moqtransport.MessageSubscribe:
			if !tupleEqual(r.Namespace, h.namespace) || (r.Track != h.trackname) {
				err := w.Reject(moqtransport.ErrorCodeSubscribeTrackDoesNotExist, "unknown track")
				if err != nil {
					log.Printf("failed to reject subscription: %v", err)
				}
				return
			}
			err := w.Accept()
			if err != nil {
				log.Printf("failed to accept subscription: %v", err)
				return
			}
			publisher, ok := w.(moqtransport.Publisher)
			if !ok {
				log.Printf("subscription response writer does not implement publisher?")
			}
			h.publishers <- publisher
		}
	})
}

func (h *moqHandler) handle(conn moqtransport.Connection) {
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
	if err := session.Announce(context.Background(), h.namespace); err != nil {
		log.Printf("failed to announce namespace '%v': %v", h.namespace, err)
		return
	}
}

func (h *moqHandler) setupDateTrack() {
	publishers := []moqtransport.Publisher{}
	ticker := time.NewTicker(time.Second)
	groupID := 0
	for {
		select {
		case ts := <-ticker.C:
			for _, p := range publishers {
				sg, err := p.OpenSubgroup(uint64(groupID), 0, 0)
				if err != nil {
					log.Printf("failed to open new subgroup: %v", err)
					return
				}
				log.Printf("writing time to publisher %v", p)
				if _, err := sg.WriteObject(0, []byte(fmt.Sprintf("%v", ts))); err != nil {
					log.Printf("failed to write time to subgroup: %v", err)
				}
				sg.Close()
			}
		case publisher := <-h.publishers:
			log.Printf("got subscriber")
			publishers = append(publishers, publisher)
		}
		groupID++
	}
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
