package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"

	"github.com/mengelbart/moqtransport"
)

// PublisherManager manages all track publishers and coordinates media-type synchronization
type PublisherManager struct {
	mu            sync.RWMutex
	logger        *slog.Logger // Logger for the publisher manager
	asset         *Asset
	catalog       *Catalog
	mediaSyncer   *MediaSyncer
	trackPubs     map[string]*ConcreteTrackPublisher
	subscriptions map[SubscriptionID]*Subscription
}

// NewPublisherManager creates a new publisher manager
func NewPublisherManager(logger *slog.Logger, asset *Asset, catalog *Catalog) *PublisherManager {
	return &PublisherManager{
		logger:        logger,
		asset:         asset,
		catalog:       catalog,
		mediaSyncer:   NewMediaSyncer(),
		trackPubs:     make(map[string]*ConcreteTrackPublisher),
		subscriptions: make(map[SubscriptionID]*Subscription),
	}
}

// Start initializes and starts all track publishers
func (pm *PublisherManager) Start(ctx context.Context) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	// Create track publishers for each track in the catalog
	for _, track := range pm.catalog.Tracks {
		if track.Name == "catalog" {
			continue // Skip catalog track
		}

		ct := pm.asset.GetTrackByName(track.Name)
		if ct == nil {
			pm.logger.Warn("track not found in asset", "track", track.Name)
			continue
		}

		// Determine media type and get appropriate group generator
		var groupGen *GroupGenerator
		switch ct.ContentType {
		case "video":
			groupGen = pm.mediaSyncer.videoGroupGen
		case "audio":
			groupGen = pm.mediaSyncer.audioGroupGen
		default:
			pm.logger.Warn("unsupported content type", "track", track.Name, "type", ct.ContentType)
			continue
		}

		// Create track publisher
		trackPub, err := NewTrackPublisher(pm.logger, pm.asset, ct, groupGen)
		if err != nil {
			return fmt.Errorf("failed to create track publisher for %s: %w", track.Name, err)
		}

		pm.trackPubs[track.Name] = trackPub

		// Register with media syncer
		err = pm.mediaSyncer.RegisterTrack(track.Name, trackPub)
		if err != nil {
			return fmt.Errorf("failed to register track %s: %w", track.Name, err)
		}

		pm.logger.Info("created track publisher", "track", track.Name, "type", ct.ContentType)
	}

	// Start media syncer (this starts the group generators)
	err := pm.mediaSyncer.Start(ctx)
	if err != nil {
		return fmt.Errorf("failed to start media syncer: %w", err)
	}

	// Start all track publishers
	for trackName, trackPub := range pm.trackPubs {
		err := trackPub.Start(ctx)
		if err != nil {
			pm.logger.Error("failed to start track publisher", "track", trackName, "error", err)
			// Continue with other tracks
		}
	}

	pm.logger.Info("publisher manager started", "nrTracks", len(pm.trackPubs))
	return nil
}

// Stop stops all track publishers and the media syncer
func (pm *PublisherManager) Stop() error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	var lastErr error

	// Stop all track publishers
	for trackName, trackPub := range pm.trackPubs {
		err := trackPub.Stop()
		if err != nil {
			pm.logger.Error("failed to stop track publisher", "track", trackName, "error", err)
			lastErr = err
		}
	}

	// Stop media syncer
	err := pm.mediaSyncer.Stop()
	if err != nil {
		lastErr = err
	}

	pm.logger.Info("publisher manager stopped")
	return lastErr
}

// HandleSubscribe handles a subscribe message using the new architecture
func (pm *PublisherManager) HandleSubscribe(
	sm *moqtransport.SubscribeMessage,
	w *moqtransport.SubscribeResponseWriter,
	sessionID uint64,
) error {
	subID := SubscriptionID{
		SessionID: sessionID,
		RequestID: sm.RequestID,
	}

	trackName := sm.Track

	// Check if this is a catalog subscription
	if trackName == "catalog" {
		return pm.handleCatalogSubscription(w)
	}

	pm.mu.Lock()
	defer pm.mu.Unlock()

	// Find the track publisher
	trackPub, exists := pm.trackPubs[trackName]
	if !exists {
		return fmt.Errorf("track not found: %s", trackName)
	}

	// Only support FilterTypeNextGroupStart for track subscriptions
	if sm.FilterType != moqtransport.FilterTypeNextGroupStart {
		return fmt.Errorf("track %s only supports FilterTypeNextGroupStart, got %d", trackName, sm.FilterType)
	}

	// Get largest location from track publisher
	largestGroup, largestObject := trackPub.GetLargestLocation()

	// Send SubscribeOkOptions with LargestLocation
	err := w.Accept(moqtransport.WithLargestLocation(&moqtransport.Location{
		Group:  largestGroup,
		Object: largestObject,
	}))
	if err != nil {
		return fmt.Errorf("failed to accept subscription: %w", err)
	}

	// Get publisher interface from response writer
	// Use SubscribeResponseWriter directly (it's already a pointer)
	publisher := w

	// Create subscription using client-provided request ID
	requestID := sm.RequestID

	currentGroup := trackPub.GetCurrentGroup()

	subscription := &Subscription{
		SessionID:   sessionID,
		Session:     nil, // Not accessible from SubscribeResponseWriter
		RequestID:   requestID,
		TrackName:   trackName,
		StartGroup:  currentGroup + 1, // Start at next group
		StartObject: 0,
		Priority:    128, // Default priority
		Publisher:   publisher,
		UpdateChan:  make(chan SubscriptionUpdate, 10),
		Done:        make(chan struct{}),
	}
	pm.subscriptions[subID] = subscription

	// Add subscription to track publisher
	err = trackPub.AddSubscription(subscription)
	if err != nil {
		delete(pm.subscriptions, subID)
		return fmt.Errorf("failed to add subscription to track publisher: %w", err)
	}

	pm.logger.Info("created subscription",
		"sessionID", sessionID,
		"subscriptionID", subID.String(),
		"track", trackName,
		"startGroup", subscription.StartGroup,
		"largestGroup", largestGroup,
		"largestObject", largestObject)

	return nil
}

// handleCatalogSubscription handles catalog subscriptions
func (pm *PublisherManager) handleCatalogSubscription(w *moqtransport.SubscribeResponseWriter) error {
	err := w.Accept()
	if err != nil {
		return fmt.Errorf("failed to accept catalog subscription: %w", err)
	}

	// Use SubscribeResponseWriter directly (it's already a pointer)
	publisher := w

	sg, err := publisher.OpenSubgroup(0, 0, 0)
	if err != nil {
		return fmt.Errorf("failed to open subgroup: %w", err)
	}

	catalogJSON, err := json.Marshal(pm.catalog)
	if err != nil {
		return fmt.Errorf("failed to marshal catalog: %w", err)
	}

	_, err = sg.WriteObject(0, catalogJSON)
	if err != nil {
		return fmt.Errorf("failed to write catalog: %w", err)
	}

	err = sg.Close()
	if err != nil {
		return fmt.Errorf("failed to close subgroup: %w", err)
	}

	return nil
}

// HandleSubscribeUpdate handles a subscribe update message
func (pm *PublisherManager) HandleSubscribeUpdate(
	sum *moqtransport.SubscribeUpdateMessage,
	sessionID uint64) error {
	pm.mu.Lock()
	defer pm.mu.Unlock()

	requestID := sum.RequestID

	// Find subscription by both RequestID and SessionID
	subID := SubscriptionID{
		SessionID: sessionID,
		RequestID: requestID,
	}
	subscription, exists := pm.subscriptions[subID]
	if !exists {
		return fmt.Errorf("subscription not found for sessionID: %d, requestID: %d", sessionID, requestID)
	}

	pm.logger.Info("updating subscription",
		"subscriptionID", subID.String(),
		"track", subscription.TrackName,
		"endGroup", sum.EndGroup,
		"subscriberPriority", sum.SubscriberPriority)

	// Find the track publisher
	trackPub, exists := pm.trackPubs[subscription.TrackName]
	if !exists {
		return fmt.Errorf("track publisher not found for track: %s", subscription.TrackName)
	}

	// Create subscription update
	update := SubscriptionUpdate{
		EndGroup: &sum.EndGroup,
		Priority: &sum.SubscriberPriority,
	}

	// Update the subscription via track publisher
	err := trackPub.UpdateSubscription(subID, update)
	if err != nil {
		return fmt.Errorf("failed to update subscription: %w", err)
	}

	return nil
}

// GetTrackPublisher returns the track publisher for a given track name
func (pm *PublisherManager) GetTrackPublisher(trackName string) (*ConcreteTrackPublisher, bool) {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	trackPub, exists := pm.trackPubs[trackName]
	return trackPub, exists
}

// GetTrackStatus returns status for all tracks
func (pm *PublisherManager) GetTrackStatus() map[string]TrackStatus {
	pm.mu.RLock()
	defer pm.mu.RUnlock()

	status := make(map[string]TrackStatus)
	for trackName, trackPub := range pm.trackPubs {
		status[trackName] = trackPub.GetTrackStatus()
	}
	return status
}
