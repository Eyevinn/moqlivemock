package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/Eyevinn/moqlivemock/internal"
	"github.com/mengelbart/moqtransport"
	"github.com/mengelbart/moqtransport/quicmoq"
	"github.com/mengelbart/moqtransport/webtransportmoq"
	"github.com/mengelbart/qlog"
	"github.com/mengelbart/qlog/moqt"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"github.com/quic-go/webtransport-go"
)

const (
	mediaPriority = 128
)

type moqHandler struct {
	addr      string
	tlsConfig *tls.Config
	namespace []string
	asset     *internal.Asset
	catalog   *internal.Catalog
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
			err := w.Reject(0, fmt.Sprintf("%s doesn't take announcements", appName))
			if err != nil {
				log.Printf("failed to reject announcement: %v", err)
			}
			return
		case moqtransport.MessageSubscribe:
			if !tupleEqual(r.Namespace, h.namespace) {
				log.Printf("got unexpected subscription namespace: %v, expected %v", r.Namespace, h.namespace)
				err := w.Reject(0, fmt.Sprintf("%s doesn't take subscriptions", appName))
				if err != nil {
					log.Printf("failed to reject subscription: %v", err)
				}
				return
			}
			if r.Track == "catalog" {
				err := w.Accept()
				if err != nil {
					log.Printf("failed to accept subscription: %v", err)
					return
				}
				publisher, ok := w.(moqtransport.Publisher)
				if !ok {
					log.Printf("subscription response writer does not implement publisher?")
				}
				sg, err := publisher.OpenSubgroup(0, 0, 0)
				if err != nil {
					log.Printf("failed to open subgroup: %v", err)
					return
				}
				json, err := json.Marshal(h.catalog)
				if err != nil {
					log.Printf("failed to marshal catalog: %v", err)
					return
				}
				_, err = sg.WriteObject(0, json)
				if err != nil {
					log.Printf("failed to write catalog: %v", err)
					return
				}
				err = sg.Close()
				if err != nil {
					log.Printf("failed to close subgroup: %v", err)
					return
				}
				return
			}
			for _, track := range h.catalog.Tracks {
				if r.Track == track.Name {
					err := w.Accept()
					if err != nil {
						log.Printf("failed to accept subscription: %v", err)
						return
					}
					publisher, ok := w.(moqtransport.Publisher)
					if !ok {
						log.Printf("subscription response writer does not implement publisher?")
					}
					log.Printf("got subscription for %s", track.Name)
					go publishTrack(context.TODO(), publisher, h.asset, track.Name)
					return
				}
			}
			// If we get here, the track was not found
			err := w.Reject(moqtransport.ErrorCodeSubscribeTrackDoesNotExist, "unknown track")
			if err != nil {
				log.Printf("failed to reject subscription: %v", err)
			}
			// case moqtransport.MessageUnsubscribe:
			// Need to handle unsubscribe
			// For nice switching, it should be possible to unsubscrit to one video track
			// and subscribe to another, and the publisher should go on until the end
			// of the current MoQ group, and start the new track on the next MoQ group.
			// This is not currently implemented.
		}
	})
}

func publishTrack(ctx context.Context, publisher moqtransport.Publisher, asset *internal.Asset, trackName string) {
	contentTrack := asset.GetTrackByName(trackName)
	if contentTrack == nil {
		log.Printf("track %s not found", trackName)
		return
	}
	now := time.Now().UnixMilli()
	currGroupNr := internal.CurrMoQGroupNr(contentTrack, uint64(now), internal.MoqGroupDurMS)
	groupNr := currGroupNr + 1 // Start stream on next group
	log.Printf("publishing %s on group %d", trackName, groupNr)
	for {
		if ctx.Err() != nil {
			return
		}
		sg, err := publisher.OpenSubgroup(groupNr, 0, mediaPriority)
		if err != nil {
			log.Printf("failed to open subgroup: %v", err)
			return
		}
		mg := internal.GenMoQGroup(contentTrack, groupNr, internal.MoqGroupDurMS)
		log.Printf("writing MoQ group %d, %d objects", groupNr, len(mg.MoQObjects))
		err = internal.WriteMoQGroup(ctx, contentTrack, mg, sg.WriteObject)
		if err != nil {
			log.Printf("failed to write MoQ group: %v", err)
			return
		}
		err = sg.Close()
		if err != nil {
			log.Printf("failed to close subgroup: %v", err)
			return
		}
		log.Printf("published MoQ group %d, %d objects", groupNr, len(mg.MoQObjects))
		groupNr++
	}
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
