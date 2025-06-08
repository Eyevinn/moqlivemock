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
	"time"

	"github.com/Eyevinn/moqlivemock/internal"
	"github.com/mengelbart/moqtransport"
	"github.com/mengelbart/qlog"
	"github.com/mengelbart/qlog/moqt"
)

const (
	initialMaxRequestID = 64
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
	endAfter  int
	
	// Track subscription completion for graceful shutdown
	tracksDone   chan string // Channel to signal when a track is done
	activeTracks map[string]bool // Track which tracks are active
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
	
	// Create cancellable context for graceful shutdown
	clientCtx, clientCancel := context.WithCancel(ctx)
	defer clientCancel()
	
	h.handle(clientCtx, conn, clientCancel)
	<-clientCtx.Done()
	slog.Info("end of runClient")
	return clientCtx.Err()
}

func (h *moqHandler) getHandler() moqtransport.Handler {
	return moqtransport.HandlerFunc(func(w moqtransport.ResponseWriter, r moqtransport.Message) {
		switch r.Method() {
		case moqtransport.MessageAnnounce:
			am, ok := r.(*moqtransport.AnnounceMessage)
			if !ok {
				slog.Error("failed to type assert AnnounceMessage")
				return
			}
			if !tupleEqual(am.Namespace, h.namespace) {
				slog.Warn("got unexpected announcement namespace",
					"received", am.Namespace,
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

func (h *moqHandler) handle(ctx context.Context, conn moqtransport.Connection, cancel context.CancelFunc) {
	session := moqtransport.NewSession(conn.Protocol(), conn.Perspective(), initialMaxRequestID)
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

	// Initialize tracking for graceful shutdown when using end-after
	if h.endAfter > 0 {
		h.tracksDone = make(chan string, 2) // Buffer for up to 2 tracks
		h.activeTracks = make(map[string]bool)
		
		// Track which tracks are active
		if videoTrack != "" {
			h.activeTracks["video"] = true
		}
		if audioTrack != "" {
			h.activeTracks["audio"] = true
		}
		
		slog.Info("tracking tracks for graceful shutdown", 
			"activeTracks", len(h.activeTracks),
			"endAfter", h.endAfter)
		
		// Wait for either context cancellation or all tracks to finish
		shutdownCh := make(chan struct{})
		go h.waitForTracksCompletion(ctx, conn, shutdownCh, cancel)
		
		// Wait for either context cancellation or graceful shutdown
		select {
		case <-ctx.Done():
			slog.Info("context cancelled")
		case <-shutdownCh:
			slog.Info("graceful shutdown completed")
		}
	} else {
		<-ctx.Done()
	}
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

	// Track first group time for end-after calculation
	var firstGroupTime *time.Time
	var targetEndGroup *uint64
	var updateSent bool

	go func() {
		for {
			o, err := rs.ReadObject(ctx)
			if err != nil {
				if err == io.EOF {
					slog.Info("got last object", "track", trackname)
					return
				}
				// Check if this is a SUBSCRIBE_DONE (expected end)
				if err.Error() == "subscribe done: status=0, reason='Subscription completed successfully'" {
					slog.Info("subscription ended normally via SUBSCRIBE_DONE", "track", trackname)
					
					// Signal that this track is done (for graceful shutdown)
					if h.endAfter > 0 && h.tracksDone != nil {
						select {
						case h.tracksDone <- mediaType:
							slog.Info("signaled track completion", "track", trackname, "mediaType", mediaType)
						default:
							// Channel full, ignore
						}
					}
					return
				}
				slog.Error("error reading object", "track", trackname, "error", err)
				return
			}
			
			// Track first group timing for end-after calculation
			if o.ObjectID == 0 {
				slog.Info("group start", "track", trackname, "groupID", o.GroupID, "payloadLength", len(o.Payload))
				
				// Record first group time and calculate target end group if needed
				if firstGroupTime == nil {
					now := time.Now()
					firstGroupTime = &now
					slog.Info("recorded first group time",
						"track", trackname, "groupID", o.GroupID, "time", now.Format("15:04:05.000"))
					
					// Calculate target end group if end-after is specified
					if h.endAfter > 0 {
						// Calculate which group should be the last one based on group count
						groupsToEnd := uint64(h.endAfter) // Direct group count
						calculatedEndGroup := o.GroupID + groupsToEnd
						targetEndGroup = &calculatedEndGroup
						
						slog.Info("calculated target end group", 
							"track", trackname,
							"firstGroupID", o.GroupID,
							"endAfterGroups", h.endAfter,
							"groupsToEnd", groupsToEnd,
							"targetEndGroup", *targetEndGroup)
					}
				}
				
				// Check if we've reached the target end group and should send SUBSCRIBE_UPDATE
				if targetEndGroup != nil && !updateSent && o.GroupID >= *targetEndGroup {
					updateSent = true
					endGroupToSend := *targetEndGroup + 1 // Send end_group + 1 as specified
					
					slog.Info("reached target end group, sending SUBSCRIBE_UPDATE",
						"track", trackname,
						"currentGroupID", o.GroupID,
						"targetEndGroup", *targetEndGroup,
						"endGroupToSend", endGroupToSend)
					
					go h.sendSubscribeUpdate(ctx, s, rs, trackname, endGroupToSend)
				}
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

// sendSubscribeUpdate sends SUBSCRIBE_UPDATE to end the subscription at the specified group
func (h *moqHandler) sendSubscribeUpdate(ctx context.Context, s *moqtransport.Session,
	rs *moqtransport.RemoteTrack, trackname string, endGroupID uint64) {
	slog.Info("sending SUBSCRIBE_UPDATE to end subscription",
		"track", trackname,
		"endGroupID", endGroupID)
	
	// Send SUBSCRIBE_UPDATE to end the subscription
	err := s.UpdateSubscription(ctx, rs.RequestID(), &moqtransport.SubscribeUpdateOptions{
		StartLocation: moqtransport.Location{
			Group:  0,
			Object: 0,
		},
		EndGroup:           endGroupID,
		SubscriberPriority: 128, // Default priority
		Forward:            true, // Enable media forwarding
		Parameters:         nil,
	})
	
	if err != nil {
		slog.Error("failed to send SUBSCRIBE_UPDATE", "track", trackname, "error", err)
		return
	}
	
	slog.Info("sent SUBSCRIBE_UPDATE successfully", "track", trackname, "endGroupID", endGroupID)
}

// waitForTracksCompletion waits for all active tracks to complete and then closes the connection gracefully
func (h *moqHandler) waitForTracksCompletion(ctx context.Context, conn moqtransport.Connection,
	shutdownCh chan struct{}, cancel context.CancelFunc) {
	completedTracks := make(map[string]bool)
	
	for {
		select {
		case <-ctx.Done():
			slog.Info("context cancelled while waiting for tracks to complete")
			return
		case mediaType := <-h.tracksDone:
			completedTracks[mediaType] = true
			slog.Info("track completed", 
				"mediaType", mediaType,
				"completedCount", len(completedTracks),
				"totalActive", len(h.activeTracks))
			
			// Check if all active tracks are done
			if len(completedTracks) >= len(h.activeTracks) {
				slog.Info("all tracks completed, closing connection gracefully")
				err := conn.CloseWithError(0, "all subscriptions completed successfully")
				if err != nil {
					slog.Error("failed to close connection gracefully", "error", err)
				}
				
				// Cancel the context to signal all goroutines to exit
				slog.Info("cancelling context for graceful shutdown")
				cancel()
				
				// Signal graceful shutdown completion
				close(shutdownCh)
				return
			}
		}
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
