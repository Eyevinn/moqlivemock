package internal

import (
	"context"
	"fmt"
	"time"
	"unsafe"

	"github.com/mengelbart/moqtransport"
)

// MediaType represents the type of media content
type MediaType int

const (
	VIDEO MediaType = iota
	AUDIO
)

func (mt MediaType) String() string {
	switch mt {
	case VIDEO:
		return "video"
	case AUDIO:
		return "audio"
	default:
		return "unknown"
	}
}

// TrackStatus provides current status information about a track
type TrackStatus struct {
	MediaType       MediaType
	CurrentGroup    uint64
	CurrentObject   uint64
	SubscriberCount int
	Bitrate         uint64
	IsLive          bool
}

// SubscriptionID uniquely identifies a subscription by session and request ID
type SubscriptionID struct {
	Session   *moqtransport.Session
	RequestID uint64
}

// String returns a string representation of the SubscriptionID
func (s SubscriptionID) String() string {
	// Use pointer address as a short unique identifier for the session
	sessionID := fmt.Sprintf("%08x", uintptr(unsafe.Pointer(s.Session))&0xffffffff)
	return fmt.Sprintf("s:%s:r:%d", sessionID, s.RequestID)
}

// SubscriptionUpdate represents changes to a subscription
type SubscriptionUpdate struct {
	EndGroup *uint64
	Priority *uint8
}

// Subscription represents an active subscription to a track
type Subscription struct {
	// Identity
	Session    *moqtransport.Session
	RequestID  uint64
	TrackAlias uint64
	TrackName  string

	// Location tracking
	StartGroup  uint64
	StartObject uint64
	EndGroup    *uint64 // nil means no end

	// Delivery state
	LastGroupSent  uint64
	LastObjectSent uint64
	DeliveryLag    time.Duration

	// Control
	Priority   uint8
	Publisher  moqtransport.Publisher
	UpdateChan chan SubscriptionUpdate
	Done       chan struct{}
}

// TrackPublisher interface defines the contract for publishing tracks
type TrackPublisher interface {
	// Core lifecycle
	Start(ctx context.Context) error
	Stop() error

	// Media type info
	GetMediaType() MediaType
	GetTrackName() string

	// Subscription management
	AddSubscription(sub *Subscription) error
	RemoveSubscription(subID SubscriptionID) error
	UpdateSubscription(subID SubscriptionID, update SubscriptionUpdate) error

	// Track state
	GetCurrentGroup() uint64
	GetLargestLocation() (group uint64, object uint64)
	GetTrackStatus() TrackStatus
}
