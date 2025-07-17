package main

import (
	"context"
	"time"

	"github.com/mengelbart/moqtransport"
)

// MediaObject represents a media object received from a MoQ track
type MediaObject struct {
	TrackName  string
	TrackAlias uint64
	GroupID    uint64
	ObjectID   uint64
	MediaType  string // "video" or "audio"
	Payload    []byte
	Timestamp  time.Time
	IsNewGroup bool // true when ObjectID == 0
}

// MediaChannel is used for passing media objects between components
type MediaChannel chan MediaObject

// Subscription represents an active subscription to a MoQ track
type Subscription struct {
	TrackName   string
	TrackAlias  uint64
	StartGroup  uint64
	EndGroup    *uint64 // nil means no end
	RemoteTrack *moqtransport.RemoteTrack
	MediaType   string
	Context     context.Context
	Cancel      context.CancelFunc
}

// SubscriptionManager handles the control plane operations
type SubscriptionManager interface {
	Subscribe(ctx context.Context, trackName string, filter string) (*Subscription, error)
	UpdateSubscription(sub *Subscription, endGroup uint64) error
	FindSubscriptionByTrackName(trackName string) *Subscription
	Close()
}

// MediaRouter routes media objects from subscriptions to output pipelines
type MediaRouter interface {
	RouteObject(obj MediaObject)
	RegisterPipeline(mediaType string, pipeline MediaPipeline)
	SetActiveTrack(mediaType string, trackName string)
	SetTrackSwitcher(switcher TrackSwitcher)
	Close()
}

// MediaPipeline processes media objects for output
type MediaPipeline interface {
	ProcessObject(obj MediaObject) error
	Close() error
}

// TrackSwitcher manages seamless track switching
type TrackSwitcher interface {
	InitiateSwitch(fromTrack, toTrack string, mediaType string) error
	HandleGroupTransition(obj MediaObject) SwitchAction
	Close()
}

// SwitchAction indicates what action to take during switching
type SwitchAction int

const (
	ContinueReading SwitchAction = iota
	EndOldTrack
	PreferNewTrack
)
