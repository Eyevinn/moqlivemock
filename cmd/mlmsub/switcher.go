package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/Eyevinn/moqlivemock/internal"
	"github.com/mengelbart/moqtransport"
)

// Switch represents an active track switch
type Switch struct {
	FromTrack    string
	ToTrack      string
	MediaType    string
	StartTime    time.Time
	FirstGroup   *uint64 // First group received from new track
	OldEndGroup  *uint64 // End group sent to old track
	State        SwitchState
}

// SwitchState represents the state of a track switch
type SwitchState int

const (
	SwitchPending SwitchState = iota  // Switch initiated, waiting for new track's first group
	SwitchActive                      // New track first group received, old track being ended
	SwitchCompleted                   // Switch completed successfully
)

// trackSwitcher implements TrackSwitcher interface
type trackSwitcher struct {
	subscriptionMgr SubscriptionManager
	activeSwitches  map[string]*Switch // mediaType -> active switch
	logger          *slog.Logger
	mu              sync.RWMutex
}

// NewTrackSwitcher creates a new track switcher
func NewTrackSwitcher(subscriptionMgr SubscriptionManager) TrackSwitcher {
	return &trackSwitcher{
		subscriptionMgr: subscriptionMgr,
		activeSwitches:  make(map[string]*Switch),
		logger:          slog.Default(),
	}
}

// InitiateSwitch starts a seamless track switch
func (ts *trackSwitcher) InitiateSwitch(fromTrack, toTrack string, mediaType string) error {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	
	ts.logger.Info("initiating track switch",
		"fromTrack", fromTrack,
		"toTrack", toTrack,
		"mediaType", mediaType)
	
	// Log current switch state for debugging
	if existingSwitch, exists := ts.activeSwitches[mediaType]; exists {
		ts.logger.Debug("existing switch found",
			"mediaType", mediaType,
			"existingFrom", existingSwitch.FromTrack,
			"existingTo", existingSwitch.ToTrack,
			"existingState", existingSwitch.State,
			"existingDuration", time.Since(existingSwitch.StartTime))
	}
	
	// Check if there's already an active switch for this media type
	if existingSwitch, exists := ts.activeSwitches[mediaType]; exists {
		if existingSwitch.State != SwitchCompleted {
			ts.logger.Warn("switch already in progress, canceling previous switch",
				"mediaType", mediaType,
				"previousFrom", existingSwitch.FromTrack,
				"previousTo", existingSwitch.ToTrack,
				"previousState", existingSwitch.State,
				"newFrom", fromTrack,
				"newTo", toTrack)
			// Mark previous switch as completed and proceed with new switch
			existingSwitch.State = SwitchCompleted
		}
	}
	
	// Create new switch
	switchObj := &Switch{
		FromTrack: fromTrack,
		ToTrack:   toTrack,
		MediaType: mediaType,
		StartTime: time.Now(),
		State:     SwitchPending,
	}
	
	ts.activeSwitches[mediaType] = switchObj
	
	// Subscribe to new track
	newSub, err := ts.subscriptionMgr.Subscribe(context.Background(), toTrack, "")
	if err != nil {
		delete(ts.activeSwitches, mediaType)
		return fmt.Errorf("failed to subscribe to new track %s: %w", toTrack, err)
	}
	
	// Send SUBSCRIBE_UPDATE immediately if we have largest location from SUBSCRIBE_OK
	if largestLoc := newSub.RemoteTrack.LargestLocation(); largestLoc != nil {
		// largestGroup+1 is the first group that will be received from new track
		firstNewGroup := largestLoc.Group + 1
		
		ts.logger.Info("using largest location for immediate SUBSCRIBE_UPDATE",
			"fromTrack", fromTrack,
			"toTrack", toTrack,
			"mediaType", mediaType,
			"largestGroup", largestLoc.Group,
			"firstNewGroup", firstNewGroup)
			
		// Record the switch state
		switchObj.FirstGroup = &firstNewGroup
		switchObj.State = SwitchActive
		switchObj.OldEndGroup = &firstNewGroup // Use same value since end group is exclusive
		
		// Send SUBSCRIBE_UPDATE to end old track at firstNewGroup (end group is exclusive)
		go ts.endOldTrack(switchObj, firstNewGroup)
	}
	
	ts.logger.Info("track switch initiated successfully",
		"fromTrack", fromTrack,
		"toTrack", toTrack,
		"mediaType", mediaType)
	
	return nil
}

// HandleGroupTransition handles group transitions during switching
func (ts *trackSwitcher) HandleGroupTransition(obj MediaObject) SwitchAction {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	
	// Only process group starts (ObjectID == 0)
	if !obj.IsNewGroup {
		return ContinueReading
	}
	
	// Check if there's an active switch for this media type
	switchObj, exists := ts.activeSwitches[obj.MediaType]
	if !exists {
		return ContinueReading // No active switch
	}
	
	ts.logger.Debug("handling group transition during switch",
		"trackName", obj.TrackName,
		"mediaType", obj.MediaType,
		"groupID", obj.GroupID,
		"switchState", switchObj.State)
	
	// Handle based on switch state and track
	switch switchObj.State {
	case SwitchPending:
		// Waiting for new track's first group
		if obj.TrackName == switchObj.ToTrack {
			return ts.handleNewTrackFirstGroup(switchObj, obj)
		}
		
	case SwitchActive:
		// Switch is active, handle ongoing objects
		if obj.TrackName == switchObj.ToTrack {
			// Check if enough time has passed to consider switch completed
			if time.Since(switchObj.StartTime) > 10*time.Second {
				ts.logger.Info("completing switch due to timeout",
					"fromTrack", switchObj.FromTrack,
					"toTrack", switchObj.ToTrack,
					"mediaType", obj.MediaType,
					"duration", time.Since(switchObj.StartTime))
				switchObj.State = SwitchCompleted
			}
			return PreferNewTrack // Always prefer new track
		} else if obj.TrackName == switchObj.FromTrack {
			// Check if old track should be ended
			if switchObj.OldEndGroup != nil && obj.GroupID >= *switchObj.OldEndGroup {
				ts.logger.Info("old track reached end group, completing switch",
					"fromTrack", switchObj.FromTrack,
					"toTrack", switchObj.ToTrack,
					"mediaType", obj.MediaType,
					"endGroup", *switchObj.OldEndGroup)
				
				switchObj.State = SwitchCompleted
				return EndOldTrack
			}
		}
		
	case SwitchCompleted:
		// Switch completed, only accept new track
		if obj.TrackName == switchObj.ToTrack {
			return PreferNewTrack
		} else {
			return EndOldTrack // Drop old track objects
		}
	}
	
	return ContinueReading
}

// handleNewTrackFirstGroup handles the first group from the new track
func (ts *trackSwitcher) handleNewTrackFirstGroup(switchObj *Switch, obj MediaObject) SwitchAction {
	ts.logger.Info("received first group from new track",
		"fromTrack", switchObj.FromTrack,
		"toTrack", switchObj.ToTrack,
		"mediaType", obj.MediaType,
		"firstGroupID", obj.GroupID)
	
	// Record first group
	switchObj.FirstGroup = &obj.GroupID
	switchObj.State = SwitchActive
	
	// Calculate end group for old track (first group + 1, since end group is exclusive)
	endGroup := obj.GroupID + 1
	switchObj.OldEndGroup = &endGroup
	
	// Send SUBSCRIBE_UPDATE to end old track
	go ts.endOldTrack(switchObj, endGroup)
	
	return PreferNewTrack
}

// endOldTrack sends SUBSCRIBE_UPDATE to end the old track
func (ts *trackSwitcher) endOldTrack(switchObj *Switch, endGroup uint64) {
	ts.logger.Info("ending old track subscription",
		"fromTrack", switchObj.FromTrack,
		"toTrack", switchObj.ToTrack,
		"mediaType", switchObj.MediaType,
		"endGroup", endGroup)
	
	// Find the old track subscription by track name
	oldSub := ts.subscriptionMgr.FindSubscriptionByTrackName(switchObj.FromTrack)
	if oldSub == nil {
		ts.logger.Error("could not find subscription for old track",
			"trackName", switchObj.FromTrack)
		return
	}
	
	// Send SUBSCRIBE_UPDATE to end the old track
	err := ts.subscriptionMgr.UpdateSubscription(oldSub, endGroup)
	if err != nil {
		ts.logger.Error("failed to send SUBSCRIBE_UPDATE for old track",
			"trackName", switchObj.FromTrack,
			"endGroup", endGroup,
			"error", err)
		return
	}
	
	ts.logger.Info("sent SUBSCRIBE_UPDATE to end old track",
		"fromTrack", switchObj.FromTrack,
		"toTrack", switchObj.ToTrack,
		"mediaType", switchObj.MediaType,
		"endGroup", endGroup)
	
	// Mark switch as completed since SUBSCRIBE_UPDATE was sent successfully
	// The old track will end naturally, but we can start new switches now
	ts.mu.Lock()
	if ts.activeSwitches[switchObj.MediaType] == switchObj {
		switchObj.State = SwitchCompleted
		ts.logger.Info("marked switch as completed after sending SUBSCRIBE_UPDATE",
			"fromTrack", switchObj.FromTrack,
			"toTrack", switchObj.ToTrack,
			"mediaType", switchObj.MediaType)
	}
	ts.mu.Unlock()
}

// Close closes the track switcher
func (ts *trackSwitcher) Close() {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	
	ts.logger.Info("closing track switcher")
	
	// Mark all switches as completed
	for mediaType, switchObj := range ts.activeSwitches {
		if switchObj.State != SwitchCompleted {
			ts.logger.Info("completing pending switch during close",
				"mediaType", mediaType,
				"fromTrack", switchObj.FromTrack,
				"toTrack", switchObj.ToTrack)
			switchObj.State = SwitchCompleted
		}
	}
}

// GetActiveSwitch returns the active switch for a media type
func (ts *trackSwitcher) GetActiveSwitch(mediaType string) *Switch {
	ts.mu.RLock()
	defer ts.mu.RUnlock()
	
	return ts.activeSwitches[mediaType]
}

// SwitchingClient extends SimpleClient with track switching capabilities
type SwitchingClient struct {
	*SimpleClient
	trackSwitcher TrackSwitcher
	catalog       *internal.Catalog
}

// NewSwitchingClient creates a client with track switching capabilities
func NewSwitchingClient(namespace []string, muxout, videoout, audioout io.Writer) *SwitchingClient {
	simpleClient := NewSimpleClient(namespace, muxout, videoout, audioout)
	
	return &SwitchingClient{
		SimpleClient: simpleClient,
	}
}

// RunTrackSwitching runs the track switching scenario
func (sc *SwitchingClient) RunTrackSwitching(ctx context.Context, session *moqtransport.Session) error {
	sc.logger.Info("starting track switching test")
	
	// Initialize components
	if err := sc.initializeComponents(session); err != nil {
		return fmt.Errorf("failed to initialize components: %w", err)
	}
	defer sc.cleanup()
	
	// Create track switcher and integrate with router
	sc.trackSwitcher = NewTrackSwitcher(sc.subscriptionMgr)
	defer sc.trackSwitcher.Close()
	
	// Register track switcher with media router
	sc.mediaRouter.SetTrackSwitcher(sc.trackSwitcher)
	
	// Subscribe to catalog and discover tracks
	catalog, err := sc.subscribeToCatalog(ctx, session)
	if err != nil {
		return fmt.Errorf("failed to subscribe to catalog: %w", err)
	}
	sc.catalog = catalog
	
	// Discover available tracks
	var videoTracks, audioTracks []*internal.Track
	for _, track := range catalog.Tracks {
		if isVideoTrack(&track) {
			videoTracks = append(videoTracks, &track)
		} else if isAudioTrack(&track) {
			audioTracks = append(audioTracks, &track)
		}
	}
	
	sc.logger.Info("discovered tracks for switching",
		"videoTracks", len(videoTracks),
		"audioTracks", len(audioTracks))
	
	if len(videoTracks) == 0 && len(audioTracks) == 0 {
		return fmt.Errorf("no video or audio tracks found for switching")
	}
	
	// Initialize mux with first track init data (for seamless switching)
	if err := sc.initializeForSwitching(videoTracks, audioTracks); err != nil {
		return fmt.Errorf("failed to initialize for switching: %w", err)
	}
	
	// Start initial tracks
	if err := sc.startInitialTracks(ctx, videoTracks, audioTracks); err != nil {
		return fmt.Errorf("failed to start initial tracks: %w", err)
	}
	
	// Wait for initial content
	sc.logger.Info("waiting 1 seconds for initial content")
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(1 * time.Second):
	}
	
	// Execute switching sequence
	if err := sc.executeSwitchingSequence(ctx, videoTracks, audioTracks); err != nil {
		return fmt.Errorf("switching sequence failed: %w", err)
	}
	
	// Wait for context cancellation
	<-ctx.Done()
	return ctx.Err()
}

// initializeForSwitching initializes mux with first track init data for seamless switching
func (sc *SwitchingClient) initializeForSwitching(videoTracks, audioTracks []*internal.Track) error {
	// Use first track init data for seamless switching (all tracks of same type share init segment)
	if len(videoTracks) > 0 {
		if err := sc.addInitToMux(videoTracks[0].InitData, "video"); err != nil {
			return fmt.Errorf("failed to add video init for switching: %w", err)
		}
		sc.logger.Info("added video init data for seamless switching",
			"sourceTrack", videoTracks[0].Name,
			"note", "all video tracks will use this same init segment")
	}
	
	if len(audioTracks) > 0 {
		if err := sc.addInitToMux(audioTracks[0].InitData, "audio"); err != nil {
			return fmt.Errorf("failed to add audio init for switching: %w", err)
		}
		sc.logger.Info("added audio init data for seamless switching",
			"sourceTrack", audioTracks[0].Name,
			"note", "all audio tracks will use this same init segment")
	}
	
	return nil
}

// startInitialTracks starts the initial video and audio tracks
func (sc *SwitchingClient) startInitialTracks(ctx context.Context, videoTracks, audioTracks []*internal.Track) error {
	// Start initial video track
	if len(videoTracks) > 0 {
		sc.logger.Info("starting initial video track", "track", videoTracks[0].Name)
		_, err := sc.subscriptionMgr.Subscribe(ctx, videoTracks[0].Name, "")
		if err != nil {
			return fmt.Errorf("failed to start initial video track: %w", err)
		}
	}
	
	// Start initial audio track
	if len(audioTracks) > 0 {
		sc.logger.Info("starting initial audio track", "track", audioTracks[0].Name)
		_, err := sc.subscriptionMgr.Subscribe(ctx, audioTracks[0].Name, "")
		if err != nil {
			return fmt.Errorf("failed to start initial audio track: %w", err)
		}
	}
	
	return nil
}

// executeSwitchingSequence executes the track switching sequence
func (sc *SwitchingClient) executeSwitchingSequence(ctx context.Context, videoTracks, audioTracks []*internal.Track) error {
	// Video track switching (skip first track since it's already active)
	for i := 1; i < len(videoTracks); i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second): // Wait between switches
		}
		
		fromTrack := videoTracks[i-1].Name
		toTrack := videoTracks[i].Name
		
		sc.logger.Info("switching video track",
			"from", fromTrack,
			"to", toTrack,
			"step", i+1, "of", len(videoTracks))
		
		err := sc.trackSwitcher.InitiateSwitch(fromTrack, toTrack, "video")
		if err != nil {
			sc.logger.Error("failed to switch video track", "error", err)
			continue
		}
	}
	
	// Audio track switching (skip first track since it's already active)
	for i := 1; i < len(audioTracks); i++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second): // Wait between switches
		}
		
		fromTrack := audioTracks[i-1].Name
		toTrack := audioTracks[i].Name
		
		sc.logger.Info("switching audio track",
			"from", fromTrack,
			"to", toTrack,
			"step", i+1, "of", len(audioTracks))
		
		err := sc.trackSwitcher.InitiateSwitch(fromTrack, toTrack, "audio")
		if err != nil {
			sc.logger.Error("failed to switch audio track", "error", err)
			continue
		}
	}
	
	sc.logger.Info("switching sequence completed successfully")
	return nil
}