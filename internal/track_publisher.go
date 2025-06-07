package internal

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// ConcreteTrackPublisher implements TrackPublisher for content tracks
type ConcreteTrackPublisher struct {
	mu            sync.RWMutex
	track         *ContentTrack
	asset         *Asset
	mediaType     MediaType
	subscriptions map[uint64]*Subscription
	groupGen      *GroupGenerator
	failedSubs    map[uint64]bool  // Track subscriptions that have failed
	
	// Publishing state
	ctx           context.Context
	cancel        context.CancelFunc
	started       bool
	currentGroup  uint64
	currentObject uint64
	groupCh       chan uint64  // Channel to receive new group notifications
}

// NewTrackPublisher creates a new track publisher for the given content track
func NewTrackPublisher(asset *Asset, track *ContentTrack, groupGen *GroupGenerator) (*ConcreteTrackPublisher, error) {
	var mediaType MediaType
	switch track.ContentType {
	case "video":
		mediaType = VIDEO
	case "audio":
		mediaType = AUDIO
	default:
		return nil, fmt.Errorf("unsupported content type: %s", track.ContentType)
	}
	
	return &ConcreteTrackPublisher{
		track:         track,
		asset:         asset,
		mediaType:     mediaType,
		subscriptions: make(map[uint64]*Subscription),
		groupGen:      groupGen,
		groupCh:       make(chan uint64, 10), // Buffered channel for group notifications
		failedSubs:    make(map[uint64]bool),  // Track failed subscriptions
	}, nil
}

// Start begins publishing this track
func (tp *ConcreteTrackPublisher) Start(ctx context.Context) error {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	
	if tp.started {
		return ErrAlreadyStarted
	}
	
	tp.ctx, tp.cancel = context.WithCancel(ctx)
	tp.started = true
	
	// Initialize current group from group generator
	tp.currentGroup = tp.groupGen.GetCurrentGroup()
	
	go tp.publishLoop()
	return nil
}

// Stop stops publishing this track
func (tp *ConcreteTrackPublisher) Stop() error {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	
	if !tp.started {
		return ErrNotStarted
	}
	
	tp.cancel()
	tp.started = false
	
	// Close group notification channel
	close(tp.groupCh)
	
	// Close all subscription channels
	for _, sub := range tp.subscriptions {
		close(sub.Done)
	}
	
	return nil
}

// GetMediaType returns the media type of this track
func (tp *ConcreteTrackPublisher) GetMediaType() MediaType {
	return tp.mediaType
}

// GetTrackName returns the name of this track
func (tp *ConcreteTrackPublisher) GetTrackName() string {
	return tp.track.Name
}

// AddSubscription adds a new subscription to this track
func (tp *ConcreteTrackPublisher) AddSubscription(sub *Subscription) error {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	
	tp.subscriptions[sub.RequestID] = sub
	slog.Info("added subscription", 
		"track", tp.track.Name, 
		"requestID", sub.RequestID,
		"totalSubscribers", len(tp.subscriptions))
	return nil
}

// RemoveSubscription removes a subscription from this track
func (tp *ConcreteTrackPublisher) RemoveSubscription(requestID uint64) error {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	
	sub, exists := tp.subscriptions[requestID]
	if !exists {
		return ErrSubscriptionNotFound
	}
	
	delete(tp.subscriptions, requestID)
	// Immediately mark as failed to suppress any ongoing goroutine error logging
	tp.failedSubs[requestID] = true
	close(sub.Done)
	slog.Info("removed subscription", 
		"track", tp.track.Name, 
		"requestID", requestID,
		"totalSubscribers", len(tp.subscriptions))
	
	// Log when track has no more subscribers
	if len(tp.subscriptions) == 0 {
		slog.Warn("track has no remaining subscribers", "track", tp.track.Name)
	}
	
	return nil
}

// UpdateSubscription updates an existing subscription
func (tp *ConcreteTrackPublisher) UpdateSubscription(requestID uint64, update SubscriptionUpdate) error {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	
	sub, exists := tp.subscriptions[requestID]
	if !exists {
		return ErrSubscriptionNotFound
	}
	
	// Update end group if specified
	if update.EndGroup != nil {
		sub.EndGroup = update.EndGroup
		slog.Info("updated subscription end group", 
			"track", tp.track.Name, 
			"requestID", requestID, 
			"endGroup", *update.EndGroup)
	}
	
	// Update priority if specified
	if update.Priority != nil {
		sub.Priority = *update.Priority
		slog.Info("updated subscription priority", 
			"track", tp.track.Name, 
			"requestID", requestID, 
			"priority", *update.Priority)
	}
	
	return nil
}

// GetCurrentGroup returns the current group number
func (tp *ConcreteTrackPublisher) GetCurrentGroup() uint64 {
	tp.mu.RLock()
	defer tp.mu.RUnlock()
	return tp.currentGroup
}

// GetLargestLocation returns the largest group and object location
func (tp *ConcreteTrackPublisher) GetLargestLocation() (group uint64, object uint64) {
	tp.mu.RLock()
	defer tp.mu.RUnlock()
	
	// Calculate largest object within the current group
	now := time.Now().UnixMilli()
	largestLoc := GetLargestObject(tp.track, uint64(now), MoqGroupDurMS)
	
	return largestLoc.Group, largestLoc.Object
}

// GetTrackStatus returns current status information
func (tp *ConcreteTrackPublisher) GetTrackStatus() TrackStatus {
	tp.mu.RLock()
	defer tp.mu.RUnlock()
	
	return TrackStatus{
		MediaType:       tp.mediaType,
		CurrentGroup:    tp.currentGroup,
		CurrentObject:   tp.currentObject,
		SubscriberCount: len(tp.subscriptions),
		Bitrate:         uint64(tp.track.SampleBitrate),
		IsLive:          tp.started,
	}
}

// LogSubscriberStats logs current subscriber statistics for this track
func (tp *ConcreteTrackPublisher) LogSubscriberStats() {
	tp.mu.RLock()
	defer tp.mu.RUnlock()
	
	if len(tp.subscriptions) == 0 {
		slog.Info("track status", 
			"track", tp.track.Name,
			"subscribers", 0,
			"status", "no_subscribers")
		return
	}
	
	// Count active vs stale subscriptions and mark stale ones as failed immediately
	activeCount := 0
	staleCount := 0
	for requestID, sub := range tp.subscriptions {
		if sub.LastGroupSent > 0 && tp.currentGroup-sub.LastGroupSent > 5 {
			staleCount++
			// Immediately mark as failed to prevent any further error logging
			tp.failedSubs[requestID] = true
		} else {
			activeCount++
		}
	}
	
	slog.Info("track status", 
		"track", tp.track.Name,
		"totalSubscribers", len(tp.subscriptions),
		"activeSubscribers", activeCount,
		"staleSubscribers", staleCount,
		"currentGroup", tp.currentGroup,
		"bitrate", tp.track.SampleBitrate)
	
	if staleCount > 0 {
		slog.Warn("detected stale subscribers", 
			"track", tp.track.Name,
			"staleCount", staleCount)
	}
}

// CleanupStaleSubscribers removes subscriptions that haven't received data for too long
func (tp *ConcreteTrackPublisher) CleanupStaleSubscribers() {
	tp.mu.Lock()
	defer tp.mu.Unlock()
	
	staleThreshold := uint64(10) // Remove if 10+ groups behind
	var staleRequestIDs []uint64
	
	for requestID, sub := range tp.subscriptions {
		// Consider a subscription stale if it's far behind current group
		if sub.LastGroupSent > 0 && tp.currentGroup-sub.LastGroupSent > staleThreshold {
			staleRequestIDs = append(staleRequestIDs, requestID)
		}
	}
	
	// Remove stale subscriptions
	for _, requestID := range staleRequestIDs {
		sub := tp.subscriptions[requestID]
		delete(tp.subscriptions, requestID)
		tp.failedSubs[requestID] = true  // Mark as failed to prevent further error logging
		close(sub.Done)
		
		slog.Warn("removed stale subscription", 
			"track", tp.track.Name,
			"requestID", requestID,
			"lastGroupSent", sub.LastGroupSent,
			"currentGroup", tp.currentGroup,
			"groupsBehind", tp.currentGroup-sub.LastGroupSent)
	}
	
	if len(staleRequestIDs) > 0 {
		slog.Info("cleanup complete", 
			"track", tp.track.Name,
			"removedStaleSubscribers", len(staleRequestIDs),
			"remainingSubscribers", len(tp.subscriptions))
	}
	
	// Also clean up failed subscriptions that are no longer in subscriptions map
	// to prevent memory leak
	for requestID := range tp.failedSubs {
		if _, exists := tp.subscriptions[requestID]; !exists {
			// This failed subscription has already been removed, safe to clean up
			delete(tp.failedSubs, requestID)
		}
	}
}

// publishLoop is the main publishing loop for this track
func (tp *ConcreteTrackPublisher) publishLoop() {
	for {
		select {
		case <-tp.ctx.Done():
			return
		case groupNr := <-tp.groupCh:
			// Publish the specified group
			tp.publishGroup(groupNr)
		}
	}
}

// onNewGroup is called by GroupGenerator when a new group becomes available
func (tp *ConcreteTrackPublisher) onNewGroup(groupNr uint64) {
	select {
	case tp.groupCh <- groupNr:
		// Successfully queued group for publishing
	default:
		// Channel full, skip this group (shouldn't happen with buffered channel)
		slog.Warn("dropped group notification, channel full", 
			"track", tp.track.Name, 
			"group", groupNr)
	}
}

// publishGroup publishes a specific group to all subscriptions
func (tp *ConcreteTrackPublisher) publishGroup(groupNr uint64) {
	tp.mu.Lock()
	
	// Skip if we've already published this group or if it's older
	if groupNr <= tp.currentGroup {
		tp.mu.Unlock()
		return
	}
	
	tp.currentGroup = groupNr
	
	// Copy subscriptions to avoid holding lock during publishing
	activeSubs := make([]*Subscription, 0, len(tp.subscriptions))
	for _, sub := range tp.subscriptions {
		// Skip subscriptions that have already failed
		if tp.failedSubs[sub.RequestID] {
			continue
		}
		
		// Check if subscription should continue
		if sub.EndGroup != nil && groupNr > *sub.EndGroup {
			// Send SUBSCRIBE_DONE and mark for removal
			go tp.sendSubscribeDone(sub)
			continue
		}
		
		// Only include subscriptions that should receive this group
		if groupNr >= sub.StartGroup {
			activeSubs = append(activeSubs, sub)
		}
	}
	
	tp.mu.Unlock()
	
	// Publish to active subscriptions
	for _, sub := range activeSubs {
		go tp.publishGroupToSubscription(sub, groupNr)
	}
}

// publishGroupToSubscription publishes a group to a specific subscription
func (tp *ConcreteTrackPublisher) publishGroupToSubscription(sub *Subscription, groupNr uint64) {
	// Quick check if subscription has already failed or been removed
	tp.mu.RLock()
	if tp.failedSubs[sub.RequestID] {
		tp.mu.RUnlock()
		return // Already marked as failed, skip silently
	}
	// Also check if subscription still exists (may have been removed)
	_, stillExists := tp.subscriptions[sub.RequestID]
	if !stillExists {
		tp.mu.RUnlock()
		// Mark as failed to prevent any other goroutines from trying
		tp.mu.Lock()
		tp.failedSubs[sub.RequestID] = true
		tp.mu.Unlock()
		return // Subscription removed, skip silently
	}
	tp.mu.RUnlock()
	
	sg, err := sub.Publisher.OpenSubgroup(groupNr, 0, sub.Priority)
	if err != nil {
		// Only log if this is the first error for this subscription
		tp.mu.RLock()
		alreadyFailed := tp.failedSubs[sub.RequestID]
		tp.mu.RUnlock()
		
		if !alreadyFailed {
			slog.Error("failed to open subgroup - subscriber may have vanished", 
				"track", tp.track.Name, 
				"group", groupNr, 
				"requestID", sub.RequestID,
				"error", err)
		}
		// Remove the problematic subscription
		tp.handleSubscriberError(sub, "failed to open subgroup", err)
		return
	}
	
	// Check again before proceeding with write - subscription might have been removed
	tp.mu.RLock()
	if tp.failedSubs[sub.RequestID] {
		tp.mu.RUnlock()
		sg.Close()
		return // Already marked as failed, abort
	}
	_, stillExists = tp.subscriptions[sub.RequestID]
	if !stillExists {
		tp.mu.RUnlock()
		sg.Close()
		tp.mu.Lock()
		tp.failedSubs[sub.RequestID] = true
		tp.mu.Unlock()
		return // Subscription removed, abort
	}
	tp.mu.RUnlock()
	
	// Generate and write MoQ group
	mg := GenMoQGroup(tp.track, groupNr, tp.track.SampleBatch, MoqGroupDurMS)
	err = WriteMoQGroup(tp.ctx, tp.track, mg, sg.WriteObject)
	if err != nil {
		// Only log if this is the first error for this subscription
		tp.mu.RLock()
		alreadyFailed := tp.failedSubs[sub.RequestID]
		tp.mu.RUnlock()
		
		if !alreadyFailed {
			slog.Error("failed to write MoQ group - subscriber may have vanished", 
				"track", tp.track.Name, 
				"group", groupNr, 
				"requestID", sub.RequestID,
				"error", err)
		}
		// Try to close subgroup before removing subscription
		sg.Close()
		tp.handleSubscriberError(sub, "failed to write MoQ group", err)
		return
	}
	
	// Check one more time before closing - subscription might have been removed during write
	tp.mu.RLock()
	if tp.failedSubs[sub.RequestID] {
		tp.mu.RUnlock()
		sg.Close()
		return // Already marked as failed, abort
	}
	_, stillExists = tp.subscriptions[sub.RequestID]
	if !stillExists {
		tp.mu.RUnlock()
		sg.Close()
		tp.mu.Lock()
		tp.failedSubs[sub.RequestID] = true
		tp.mu.Unlock()
		return // Subscription removed, abort
	}
	tp.mu.RUnlock()
	
	err = sg.Close()
	if err != nil {
		// Only log if this is the first error for this subscription
		tp.mu.RLock()
		alreadyFailed := tp.failedSubs[sub.RequestID]
		tp.mu.RUnlock()
		
		if !alreadyFailed {
			slog.Error("failed to close subgroup - subscriber may have vanished", 
				"track", tp.track.Name, 
				"group", groupNr, 
				"requestID", sub.RequestID,
				"error", err)
		}
		tp.handleSubscriberError(sub, "failed to close subgroup", err)
		return
	}
	
	tp.mu.Lock()
	sub.LastGroupSent = groupNr
	sub.LastObjectSent = uint64(len(mg.MoQObjects) - 1)
	tp.mu.Unlock()
	
	slog.Debug("published group to subscription", 
		"track", tp.track.Name, 
		"group", groupNr, 
		"requestID", sub.RequestID)
}

// sendSubscribeDone sends a SUBSCRIBE_DONE message and cleans up the subscription
func (tp *ConcreteTrackPublisher) sendSubscribeDone(sub *Subscription) {
	// TODO: Implement SUBSCRIBE_DONE message sending
	// This will be implemented in Phase 2 when we add control message support
	
	slog.Info("subscription ended normally", 
		"track", tp.track.Name, 
		"requestID", sub.RequestID, 
		"finalGroup", sub.LastGroupSent)
	
	// Remove subscription
	tp.RemoveSubscription(sub.RequestID)
}

// handleSubscriberError handles errors that indicate a subscriber has vanished
func (tp *ConcreteTrackPublisher) handleSubscriberError(sub *Subscription, operation string, err error) {
	tp.mu.Lock()
	// Check if we've already marked this subscription as failed
	if tp.failedSubs[sub.RequestID] {
		tp.mu.Unlock()
		return // Already handled, avoid duplicate logging
	}
	
	// Mark subscription as failed to prevent further attempts
	tp.failedSubs[sub.RequestID] = true
	
	// Check if subscription still exists before logging
	_, stillExists := tp.subscriptions[sub.RequestID]
	tp.mu.Unlock()
	
	if stillExists {
		slog.Warn("subscriber appears to have vanished - removing subscription",
			"track", tp.track.Name,
			"requestID", sub.RequestID,
			"operation", operation,
			"error", err)
		
		// Remove the problematic subscription
		tp.RemoveSubscription(sub.RequestID)
	}
}