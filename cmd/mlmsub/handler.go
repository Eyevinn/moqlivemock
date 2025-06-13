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
	quic         bool
	addr         string
	namespace    []string
	catalog      *internal.Catalog
	mux          *cmafMux
	outs         map[string]io.Writer
	logfh        io.Writer
	videoname    string
	audioname    string
	endAfter     int
	switchTracks bool

	// Track subscription completion for graceful shutdown
	tracksDone   chan string     // Channel to signal when a track is done
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

	// Declare track variables at function scope
	videoTrack := ""
	audioTrack := ""

	if h.switchTracks {
		// Run track switching scenario
		err := h.runTrackSwitching(ctx, session, conn)
		if err != nil {
			slog.Error("failed to run track switching", "error", err)
			err = conn.CloseWithError(0, "internal error")
			if err != nil {
				slog.Error("failed to close connection", "error", err)
			}
			return
		}
	} else {
		// Original behavior - subscribe to single tracks
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
	}

	// For non-switching mode, handle graceful shutdown with end-after
	if !h.switchTracks {
		// Initialize tracking for graceful shutdown when using end-after
		if h.endAfter > 0 {
			h.tracksDone = make(chan string, 2) // Buffer for up to 2 tracks
			h.activeTracks = make(map[string]bool)

			// Track which tracks are active (videoTrack and audioTrack are defined in this scope)
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
	} else {
		// For switching mode, just wait for context cancellation (switching handles its own timing)
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

				// Send SUBSCRIBE_UPDATE if targetEndGroup is set and not yet sent
				if targetEndGroup != nil && !updateSent {
					endGroupToSend := *targetEndGroup //

					slog.Info("reached target end group, sending SUBSCRIBE_UPDATE",
						"track", trackname,
						"currentGroupID", o.GroupID,
						"targetEndGroup", *targetEndGroup,
						"endGroupToSend", endGroupToSend)

					go h.sendSubscribeUpdate(ctx, s, rs, trackname, endGroupToSend)
					updateSent = true
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
		SubscriberPriority: 128,  // Default priority
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

// runTrackSwitching implements the track switching scenario by discovering tracks from catalog
func (h *moqHandler) runTrackSwitching(ctx context.Context, session *moqtransport.Session, conn moqtransport.Connection) error {
	// Discover tracks from catalog
	var videoTracks []*internal.Track
	var audioTracks []*internal.Track

	for _, track := range h.catalog.Tracks {
		if strings.HasPrefix(track.MimeType, "video") {
			videoTracks = append(videoTracks, &track)
		} else if strings.HasPrefix(track.MimeType, "audio") {
			audioTracks = append(audioTracks, &track)
		}
	}

	slog.Info("discovered tracks for switching",
		"videoTracks", len(videoTracks),
		"audioTracks", len(audioTracks))

	// Log available tracks
	for _, track := range videoTracks {
		slog.Info("available video track", "name", track.Name, "mimeType", track.MimeType)
	}
	for _, track := range audioTracks {
		slog.Info("available audio track", "name", track.Name, "mimeType", track.MimeType)
	}

	if len(videoTracks) == 0 && len(audioTracks) == 0 {
		return fmt.Errorf("no video or audio tracks found in catalog")
	}

	// Initialize mux with first track init data ONLY (for seamless switching)
	if h.mux != nil {
		if len(videoTracks) > 0 {
			err := h.mux.addInit(videoTracks[0].InitData, "video")
			if err != nil {
				slog.Error("failed to add video init data", "track", videoTracks[0].Name, "error", err)
			} else {
				slog.Info("added video init data for seamless switching",
					"sourceTrack", videoTracks[0].Name,
					"note", "all video tracks will use this same init segment")
			}
		}
		if len(audioTracks) > 0 {
			err := h.mux.addInit(audioTracks[0].InitData, "audio")
			if err != nil {
				slog.Error("failed to add audio init data", "track", audioTracks[0].Name, "error", err)
			} else {
				slog.Info("added audio init data for seamless switching",
					"sourceTrack", audioTracks[0].Name,
					"note", "all audio tracks will use this same init segment")
			}
		}
	}

	// Start with both video and audio subscriptions immediately
	var currentVideoSubscription *moqtransport.RemoteTrack
	var currentAudioSubscription *moqtransport.RemoteTrack

	// Start initial video track
	if len(videoTracks) > 0 {
		slog.Info("starting initial video track", "track", videoTracks[0].Name)
		newSub, err := h.switchToTrack(ctx, session, videoTracks[0].Name, "video", nil)
		if err != nil {
			return fmt.Errorf("failed to start initial video track %s: %w", videoTracks[0].Name, err)
		}
		currentVideoSubscription = newSub
	}

	// Start initial audio track
	if len(audioTracks) > 0 {
		slog.Info("starting initial audio track", "track", audioTracks[0].Name)
		newSub, err := h.switchToTrack(ctx, session, audioTracks[0].Name, "audio", nil)
		if err != nil {
			return fmt.Errorf("failed to start initial audio track %s: %w", audioTracks[0].Name, err)
		}
		currentAudioSubscription = newSub
	}

	// Wait a bit to receive some initial content
	slog.Info("waiting 3 seconds to receive initial content from both tracks")
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(3 * time.Second):
		// Continue to switching
	}

	// Video track switching sequence (start from second track since first is already active)
	for i := 1; i < len(videoTracks); i++ {
		track := videoTracks[i]
		slog.Info("switching to video track", "track", track.Name, "step", i+1, "of", len(videoTracks))

		newSub, err := h.switchToTrack(ctx, session, track.Name, "video", currentVideoSubscription)
		if err != nil {
			return fmt.Errorf("failed to switch to video track %s: %w", track.Name, err)
		}
		currentVideoSubscription = newSub

		// Wait 5 seconds before switching to next track (except for the last one)
		if i < len(videoTracks)-1 {
			slog.Info("waiting 3 seconds before next video switch", "currentTrack", track.Name)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(3 * time.Second):
				// Continue to next track
			}
		}
	}

	// Audio track switching sequence (start from second track since first is already active)
	for i := 1; i < len(audioTracks); i++ {
		track := audioTracks[i]
		slog.Info("switching to audio track", "track", track.Name, "step", i+1, "of", len(audioTracks))

		newSub, err := h.switchToTrack(ctx, session, track.Name, "audio", currentAudioSubscription)
		if err != nil {
			return fmt.Errorf("failed to switch to audio track %s: %w", track.Name, err)
		}
		currentAudioSubscription = newSub

		// Wait 5 seconds before switching to next track (except for the last one)
		if i < len(audioTracks)-1 {
			slog.Info("waiting 5 seconds before next audio switch", "currentTrack", track.Name)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Second):
				// Continue to next track
			}
		}
	}

	// Wait additional 5 seconds to receive some content from the final track
	slog.Info("waiting 5 seconds to receive content from final tracks")
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(5 * time.Second):
		// End switching scenario
	}

	slog.Info("track switching scenario completed successfully")
	return nil
}

// switchToTrack switches from oldSub to a new track, handling the seamless handoff
func (h *moqHandler) switchToTrack(ctx context.Context, session *moqtransport.Session,
	trackName, mediaType string, oldSub *moqtransport.RemoteTrack) (*moqtransport.RemoteTrack, error) {

	// 1. Subscribe to new track with "next group" filter
	slog.Info("subscribing to new track", "track", trackName, "mediaType", mediaType)
	newSub, err := session.Subscribe(ctx, h.namespace, trackName, "")
	if err != nil {
		return nil, fmt.Errorf("failed to subscribe to track %s: %w", trackName, err)
	}

	// Start reading from the new subscription
	go h.readTrackWithSwitching(ctx, newSub, trackName, mediaType, oldSub, session)

	return newSub, nil
}

// readTrackWithSwitching reads from a track subscription and handles switching logic
func (h *moqHandler) readTrackWithSwitching(ctx context.Context, rs *moqtransport.RemoteTrack,
	trackName, mediaType string, oldSub *moqtransport.RemoteTrack, session *moqtransport.Session) {

	var firstGroup *uint64
	var shouldEndOldSub bool

	for {
		o, err := rs.ReadObject(ctx)
		if err != nil {
			if err == io.EOF {
				slog.Info("reached end of track", "track", trackName)
				return
			}
			// Check if this is a SUBSCRIBE_DONE (expected end)
			if strings.Contains(err.Error(), "subscribe done") {
				slog.Info("subscription ended via SUBSCRIBE_DONE", "track", trackName)
				return
			}
			slog.Error("error reading object", "track", trackName, "error", err)
			return
		}

		// Track first group for handoff calculation
		if o.ObjectID == 0 {
			slog.Info("group start", "track", trackName, "groupID", o.GroupID, "payloadLength", len(o.Payload))

			if firstGroup == nil {
				firstGroup = &o.GroupID
				slog.Info("recorded first group for new track", "track", trackName, "groupID", o.GroupID)

				// If we have an old subscription, end it at this group + 1 since end group is exclusive
				if oldSub != nil && !shouldEndOldSub {
					shouldEndOldSub = true
					endGroupID := o.GroupID + 1

					slog.Info("ending old subscription", "newTrack", trackName, "endGroupID", endGroupID)
					go h.sendSubscribeUpdateForSwitching(ctx, session, oldSub, endGroupID)
				}
			}
		} else {
			slog.Debug("object", "track", trackName, "objectID", o.ObjectID, "groupID", o.GroupID, "payloadLength", len(o.Payload))
		}

		// Write media data (seamless switching - no new init segments)
		if h.mux != nil {
			err = h.mux.muxSample(o.Payload, mediaType)
			if err != nil {
				slog.Error("failed to mux sample", "track", trackName, "error", err)
				return
			}
			// Log seamless switching at first object of each group
			if o.ObjectID == 0 {
				slog.Debug("seamless track switch in mux output",
					"track", trackName,
					"mediaType", mediaType,
					"groupID", o.GroupID,
					"note", "using existing init segment")
			}
		}
		if h.outs[mediaType] != nil {
			_, err = h.outs[mediaType].Write(o.Payload)
			if err != nil {
				slog.Error("failed to write sample", "track", trackName, "error", err)
				return
			}
		}
	}
}

// sendSubscribeUpdateForSwitching sends SUBSCRIBE_UPDATE to end an old subscription during switching
func (h *moqHandler) sendSubscribeUpdateForSwitching(ctx context.Context, session *moqtransport.Session,
	rs *moqtransport.RemoteTrack, endGroupID uint64) {

	slog.Info("sending SUBSCRIBE_UPDATE to end old subscription during switch",
		"requestID", rs.RequestID(),
		"endGroupID", endGroupID)

	err := session.UpdateSubscription(ctx, rs.RequestID(), &moqtransport.SubscribeUpdateOptions{
		StartLocation: moqtransport.Location{
			Group:  0,
			Object: 0,
		},
		EndGroup:           endGroupID,
		SubscriberPriority: 128,
		Forward:            true,
		Parameters:         nil,
	})

	if err != nil {
		slog.Error("failed to send SUBSCRIBE_UPDATE for switching", "requestID", rs.RequestID(), "error", err)
		return
	}

	slog.Info("sent SUBSCRIBE_UPDATE for switching successfully", "requestID", rs.RequestID(), "endGroupID", endGroupID)
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
