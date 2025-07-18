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
	logger        *slog.Logger
	track         *ContentTrack
	asset         *Asset
	mediaType     MediaType
	subscriptions map[SubscriptionID]*Subscription
	groupGen      *GroupGenerator
	failedSubs    map[SubscriptionID]bool // Track subscriptions that have failed

	// Publishing state
	ctx           context.Context
	cancel        context.CancelFunc
	started       bool
	currentGroup  uint64
	currentObject uint64
	groupCh       chan uint64 // Channel to receive new group notifications
}

// NewTrackPublisher creates a new track publisher for the given content track
func NewTrackPublisher(
	logger *slog.Logger,
	asset *Asset,
	track *ContentTrack,
	groupGen *GroupGenerator,
) (*ConcreteTrackPublisher, error) {
	var mediaType MediaType
	switch track.ContentType {
	case "video":
		mediaType = VIDEO
	case "audio":
		mediaType = AUDIO
	default:
		return nil, fmt.Errorf("unsupported content type: %s", track.ContentType)
	}
	logger = logger.With("track", track.Name, "asset", asset.Name)

	return &ConcreteTrackPublisher{
		logger:        logger,
		track:         track,
		asset:         asset,
		mediaType:     mediaType,
		subscriptions: make(map[SubscriptionID]*Subscription),
		groupGen:      groupGen,
		groupCh:       make(chan uint64, 10),         // Buffered channel for group notifications
		failedSubs:    make(map[SubscriptionID]bool), // Track failed subscriptions
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

	subID := SubscriptionID{
		SessionID: sub.SessionID,
		RequestID: sub.RequestID,
	}
	tp.subscriptions[subID] = sub
	tp.logger.Info("added subscription",
		"sessionID", sub.SessionID,
		"subscriptionID", subID.String(),
		"totalSubscribers", len(tp.subscriptions))
	return nil
}

// RemoveSubscription removes a subscription from this track
func (tp *ConcreteTrackPublisher) RemoveSubscription(subID SubscriptionID) error {
	tp.mu.Lock()
	defer tp.mu.Unlock()

	sub, exists := tp.subscriptions[subID]
	if !exists {
		return ErrSubscriptionNotFound
	}

	delete(tp.subscriptions, subID)
	// Immediately mark as failed to suppress any ongoing goroutine error logging
	tp.failedSubs[subID] = true
	close(sub.Done)
	tp.logger.Info("removed subscription",
		"sessionID", sub.SessionID,
		"subscriptionID", subID.String(),
		"totalSubscribers", len(tp.subscriptions))

	// Log when track has no more subscribers
	if len(tp.subscriptions) == 0 {
		tp.logger.Info("track has no remaining subscribers")
	}

	return nil
}

// UpdateSubscription updates an existing subscription
func (tp *ConcreteTrackPublisher) UpdateSubscription(subID SubscriptionID, update SubscriptionUpdate) error {
	tp.mu.Lock()
	defer tp.mu.Unlock()

	sub, exists := tp.subscriptions[subID]
	if !exists {
		return ErrSubscriptionNotFound
	}

	// Update end group if specified
	if update.EndGroup != nil {
		sub.EndGroup = update.EndGroup
		tp.logger.Info("updated subscription end group",
			"sessionID", sub.SessionID,
			"subscriptionID", subID.String(),
			"endGroup", *update.EndGroup)
	}

	// Update priority if specified
	if update.Priority != nil {
		sub.Priority = *update.Priority
		tp.logger.Info("updated subscription priority",
			"sessionID", sub.SessionID,
			"subscriptionID", subID.String(),
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
		tp.logger.Info("track status",
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

	tp.logger.Info("track status",
		"totalSubscribers", len(tp.subscriptions),
		"activeSubscribers", activeCount,
		"staleSubscribers", staleCount,
		"currentGroup", tp.currentGroup,
		"bitrate", tp.track.SampleBitrate)

	if staleCount > 0 {
		tp.logger.Warn("detected stale subscribers", "staleCount", staleCount)
	}
}

// CleanupStaleSubscribers removes subscriptions that haven't received data for too long
func (tp *ConcreteTrackPublisher) CleanupStaleSubscribers() {
	tp.mu.Lock()
	defer tp.mu.Unlock()

	staleThreshold := uint64(10) // Remove if 10+ groups behind
	var staleIDs []SubscriptionID

	for subID, sub := range tp.subscriptions {
		// Consider a subscription stale if it's far behind current group
		if sub.LastGroupSent > 0 && tp.currentGroup-sub.LastGroupSent > staleThreshold {
			staleIDs = append(staleIDs, subID)
		}
	}

	// Remove stale subscriptions
	for _, subID := range staleIDs {
		sub := tp.subscriptions[subID]
		delete(tp.subscriptions, subID)
		tp.failedSubs[subID] = true // Mark as failed to prevent further error logging
		close(sub.Done)

		tp.logger.Warn("removed stale subscription",
			"subscriptionID", subID.String(),
			"lastGroupSent", sub.LastGroupSent,
			"currentGroup", tp.currentGroup,
			"groupsBehind", tp.currentGroup-sub.LastGroupSent)
	}

	if len(staleIDs) > 0 {
		tp.logger.Info("cleanup complete",
			"removedStaleSubscribers", len(staleIDs),
			"remainingSubscribers", len(tp.subscriptions))
	}

	// Also clean up failed subscriptions that are no longer in subscriptions map
	// to prevent memory leak
	for subID := range tp.failedSubs {
		if _, exists := tp.subscriptions[subID]; !exists {
			// This failed subscription has already been removed, safe to clean up
			delete(tp.failedSubs, subID)
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
		tp.logger.Warn("dropped group notification, channel full",
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
	for subID, sub := range tp.subscriptions {
		// Skip subscriptions that have already failed
		if tp.failedSubs[subID] {
			continue
		}

		// Check if subscription should continue
		if sub.EndGroup != nil && groupNr >= *sub.EndGroup {
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

	mg := GenMoQGroup(tp.track, groupNr, tp.track.SampleBatch, MoqGroupDurMS)

	// Publish to active subscriptions
	for _, sub := range activeSubs {
		go tp.publishGroupToSubscription(sub, mg)
	}
}

// publishGroupToSubscription publishes a group to a specific subscription
func (tp *ConcreteTrackPublisher) publishGroupToSubscription(sub *Subscription, mg *MoQGroup) {
	// Quick check if subscription has already failed or been removed
	subID := SubscriptionID{SessionID: sub.SessionID, RequestID: sub.RequestID}
	tp.mu.RLock()
	if tp.failedSubs[subID] {
		tp.mu.RUnlock()
		return // Already marked as failed, skip silently
	}
	// Also check if subscription still exists (may have been removed)
	_, stillExists := tp.subscriptions[subID]
	if !stillExists {
		tp.mu.RUnlock()
		// Mark as failed to prevent any other goroutines from trying
		tp.mu.Lock()
		tp.failedSubs[subID] = true
		tp.mu.Unlock()
		return // Subscription removed, skip silently
	}
	tp.mu.RUnlock()

	sg, err := sub.Publisher.OpenSubgroup(mg.groupNr, 0, sub.Priority)
	if err != nil {
		// Only log if this is the first error for this subscription
		tp.mu.RLock()
		alreadyFailed := tp.failedSubs[subID]
		tp.mu.RUnlock()

		if !alreadyFailed {
			tp.logger.Error("failed to open subgroup - subscriber may have vanished",
				"group", mg.groupNr,
				"subscriptionID", subID.String(),
				"error", err)
		}
		// Remove the problematic subscription
		tp.handleSubscriberError(sub, "failed to open subgroup", err)
		return
	}

	// Check again before proceeding with write - subscription might have been removed
	tp.mu.RLock()
	if tp.failedSubs[subID] {
		tp.mu.RUnlock()
		sg.Close()
		return // Already marked as failed, abort
	}
	_, stillExists = tp.subscriptions[subID]
	if !stillExists {
		tp.mu.RUnlock()
		sg.Close()
		tp.mu.Lock()
		tp.failedSubs[subID] = true
		tp.mu.Unlock()
		return // Subscription removed, abort
	}
	tp.mu.RUnlock()

	// Generate and write MoQ group
	logger := tp.logger.With("sessionID", sub.SessionID)
	err = WriteMoQGroup(tp.ctx, logger, tp.track, mg, sg.WriteObject)
	if err != nil {
		// Only log if this is the first error for this subscription
		tp.mu.RLock()
		alreadyFailed := tp.failedSubs[subID]
		tp.mu.RUnlock()

		if !alreadyFailed {
			logger.Error("failed to write MoQ group - subscriber may have vanished",
				"error", err)
		}
		// Try to close subgroup before removing subscription
		sg.Close()
		tp.handleSubscriberError(sub, "failed to write MoQ group", err)
		return
	}

	// Check one more time before closing - subscription might have been removed during write
	tp.mu.RLock()
	if tp.failedSubs[subID] {
		tp.mu.RUnlock()
		sg.Close()
		return // Already marked as failed, abort
	}
	_, stillExists = tp.subscriptions[subID]
	if !stillExists {
		tp.mu.RUnlock()
		sg.Close()
		tp.mu.Lock()
		tp.failedSubs[subID] = true
		tp.mu.Unlock()
		return // Subscription removed, abort
	}
	tp.mu.RUnlock()

	err = sg.Close()
	if err != nil {
		// Only log if this is the first error for this subscription
		tp.mu.RLock()
		alreadyFailed := tp.failedSubs[subID]
		tp.mu.RUnlock()

		if !alreadyFailed {
			tp.logger.Error("failed to close subgroup - subscriber may have vanished",
				"group", mg.groupNr,
				"subscriptionID", subID.String(),
				"error", err)
		}
		tp.handleSubscriberError(sub, "failed to close subgroup", err)
		return
	}

	tp.mu.Lock()
	sub.LastGroupSent = mg.groupNr
	sub.LastObjectSent = uint64(len(mg.MoQObjects) - 1)
	tp.mu.Unlock()

	tp.logger.Debug("published group to subscription",
		"group", mg.groupNr,
		"subscriptionID", subID.String())
}

// sendSubscribeDone sends a SUBSCRIBE_DONE message and cleans up the subscription
func (tp *ConcreteTrackPublisher) sendSubscribeDone(sub *Subscription) {
	subID := SubscriptionID{SessionID: sub.SessionID, RequestID: sub.RequestID}
	tp.logger.Info("subscription ended normally",
		"sessionID", sub.SessionID,
		"subscriptionID", subID.String(),
		"finalGroup", sub.LastGroupSent)

	// Send SUBSCRIBE_DONE with success status
	err := sub.Publisher.CloseWithError(0, "Subscription completed successfully")
	if err != nil {
		tp.logger.Error("failed to send SUBSCRIBE_DONE",
			"subscriptionID", subID.String(),
			"error", err)
	}

	// Remove subscription
	if err := tp.RemoveSubscription(subID); err != nil {
		tp.logger.Error("failed to remove subscription after SUBSCRIBE_DONE",
			"subscriptionID", subID.String(),
			"error", err)
	}
}

// handleSubscriberError handles errors that indicate a subscriber has vanished
func (tp *ConcreteTrackPublisher) handleSubscriberError(sub *Subscription, operation string, err error) {
	subID := SubscriptionID{SessionID: sub.SessionID, RequestID: sub.RequestID}
	tp.mu.Lock()
	// Check if we've already marked this subscription as failed
	if tp.failedSubs[subID] {
		tp.mu.Unlock()
		return // Already handled, avoid duplicate logging
	}

	// Mark subscription as failed to prevent further attempts
	tp.failedSubs[subID] = true

	// Check if subscription still exists before logging
	_, stillExists := tp.subscriptions[subID]
	tp.mu.Unlock()

	if stillExists {
		tp.logger.Warn("subscriber appears to have vanished - removing subscription",
			"sessionID", sub.SessionID,
			"subscriptionID", subID.String(),
			"operation", operation,
			"error", err)

		// Remove the problematic subscription (reuse existing subID variable)
		// subID already defined above
		if err := tp.RemoveSubscription(subID); err != nil {
			tp.logger.Error("failed to remove problematic subscription",
				"subscriptionID", subID.String(),
				"error", err)
		}
	}
}
