package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/mengelbart/moqtransport"
)

// subscriptionManager implements SubscriptionManager interface
type subscriptionManager struct {
	session      *moqtransport.Session
	namespace    string
	mediaChannel MediaChannel
	logger       *slog.Logger
	subscriptions map[uint64]*Subscription // trackAlias -> subscription
}

// NewSubscriptionManager creates a new subscription manager
func NewSubscriptionManager(session *moqtransport.Session, namespace string, mediaChannel MediaChannel) SubscriptionManager {
	return &subscriptionManager{
		session:       session,
		namespace:     namespace,
		mediaChannel:  mediaChannel,
		logger:        slog.Default(),
		subscriptions: make(map[uint64]*Subscription),
	}
}

// Subscribe creates a new subscription to a track
func (sm *subscriptionManager) Subscribe(ctx context.Context, trackName string, filter string) (*Subscription, error) {
	sm.logger.Info("subscribing to track", "trackName", trackName, "filter", filter)
	
	// Convert namespace string to []string
	namespaceSlice := strings.Split(sm.namespace, "/")
	rs, err := sm.session.Subscribe(ctx, namespaceSlice, trackName, filter)
	if err != nil {
		return nil, fmt.Errorf("failed to subscribe to track %s: %w", trackName, err)
	}

	// Create subscription context for cancellation
	subCtx, cancel := context.WithCancel(ctx)
	
	// Determine media type from track name
	mediaType := determineMediaType(trackName)
	
	// Extract start group from subscription response
	startGroup := sm.extractStartGroup(rs)
	
	// Use RequestID as track identifier since TrackAlias is not exposed
	requestID := rs.RequestID()
	
	sub := &Subscription{
		TrackName:   trackName,
		TrackAlias:  requestID, // Using RequestID as substitute for now
		StartGroup:  startGroup,
		EndGroup:    nil,
		RemoteTrack: rs,
		MediaType:   mediaType,
		Context:     subCtx,
		Cancel:      cancel,
	}
	
	// Store subscription using RequestID
	sm.subscriptions[requestID] = sub
	
	// Start reading objects in background
	go sm.readObjectsToChannel(sub)
	
	sm.logger.Info("subscription created",
		"trackName", trackName,
		"requestID", requestID,
		"mediaType", mediaType,
		"startGroup", startGroup)
	
	return sub, nil
}

// UpdateSubscription updates subscription parameters (e.g., set end group)
func (sm *subscriptionManager) UpdateSubscription(sub *Subscription, endGroup uint64) error {
	sm.logger.Info("updating subscription",
		"trackName", sub.TrackName,
		"trackAlias", sub.TrackAlias,
		"endGroup", endGroup)
	
	// Update the subscription's end group
	sub.EndGroup = &endGroup
	
	// Send SUBSCRIBE_UPDATE message using RequestID
	opts := &moqtransport.SubscribeUpdateOptions{
		EndGroup: endGroup,
	}
	err := sm.session.UpdateSubscription(sub.Context, sub.RemoteTrack.RequestID(), opts)
	if err != nil {
		return fmt.Errorf("failed to update subscription for track %s: %w", sub.TrackName, err)
	}
	
	return nil
}

// Close closes all subscriptions and stops the manager
func (sm *subscriptionManager) Close() {
	sm.logger.Info("closing subscription manager")
	
	// Cancel all subscription contexts
	for _, sub := range sm.subscriptions {
		if sub.Cancel != nil {
			sub.Cancel()
		}
	}
	
	// Clear subscriptions
	sm.subscriptions = make(map[uint64]*Subscription)
}

// readObjectsToChannel reads objects from a subscription and sends them to the media channel
func (sm *subscriptionManager) readObjectsToChannel(sub *Subscription) {
	defer sm.logger.Info("stopped reading objects", "trackName", sub.TrackName)
	
	sm.logger.Info("starting to read objects", "trackName", sub.TrackName)
	
	for {
		select {
		case <-sub.Context.Done():
			sm.logger.Info("subscription context cancelled", "trackName", sub.TrackName)
			return
		default:
		}
		
		// Read object from remote track
		obj, err := sub.RemoteTrack.ReadObject(sub.Context)
		if err != nil {
			sm.logger.Error("error reading object",
				"trackName", sub.TrackName,
				"error", err)
			return
		}
		
		// Create MediaObject
		mediaObj := MediaObject{
			TrackName:  sub.TrackName,
			TrackAlias: sub.TrackAlias,
			GroupID:    obj.GroupID,
			ObjectID:   obj.ObjectID,
			MediaType:  sub.MediaType,
			Payload:    obj.Payload,
			Timestamp:  time.Now(),
			IsNewGroup: obj.ObjectID == 0,
		}
		
		// Send to media channel (non-blocking)
		select {
		case sm.mediaChannel <- mediaObj:
			// Object sent successfully
		case <-sub.Context.Done():
			return
		default:
			sm.logger.Warn("media channel full, dropping object",
				"trackName", sub.TrackName,
				"groupID", obj.GroupID,
				"objectID", obj.ObjectID)
		}
	}
}

// extractStartGroup extracts the start group from subscription response
func (sm *subscriptionManager) extractStartGroup(rs *moqtransport.RemoteTrack) uint64 {
	// TODO: Extract from SUBSCRIBE_RESPONSE message when available in moqtransport
	// For now, return 0 as default
	return 0
}

// determineMediaType determines media type from track name
func determineMediaType(trackName string) string {
	trackLower := strings.ToLower(trackName)
	if strings.Contains(trackLower, "video") {
		return "video"
	} else if strings.Contains(trackLower, "audio") {
		return "audio"
	}
	return "unknown"
}