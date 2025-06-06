package internal

import (
	"context"
	"sync"
	"time"
)

// MediaSyncer coordinates group generation and synchronization across media types
type MediaSyncer struct {
	mu            sync.RWMutex
	videoGroupGen *GroupGenerator
	audioGroupGen *GroupGenerator
	videoTracks   map[string]TrackPublisher
	audioTracks   map[string]TrackPublisher
}

// NewMediaSyncer creates a new MediaSyncer with separate group generators for video and audio
func NewMediaSyncer() *MediaSyncer {
	return &MediaSyncer{
		videoGroupGen: NewGroupGenerator(VIDEO, time.Duration(MoqGroupDurMS)*time.Millisecond),
		audioGroupGen: NewGroupGenerator(AUDIO, time.Duration(MoqGroupDurMS)*time.Millisecond),
		videoTracks:   make(map[string]TrackPublisher),
		audioTracks:   make(map[string]TrackPublisher),
	}
}

// RegisterTrack registers a track publisher with the appropriate group generator
func (ms *MediaSyncer) RegisterTrack(trackName string, publisher TrackPublisher) error {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	
	mediaType := publisher.GetMediaType()
	switch mediaType {
	case VIDEO:
		ms.videoTracks[trackName] = publisher
		return ms.videoGroupGen.RegisterTrack(trackName, publisher)
	case AUDIO:
		ms.audioTracks[trackName] = publisher
		return ms.audioGroupGen.RegisterTrack(trackName, publisher)
	default:
		return ErrUnsupportedMediaType
	}
}

// UnregisterTrack removes a track publisher from the appropriate group generator
func (ms *MediaSyncer) UnregisterTrack(trackName string, mediaType MediaType) error {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	
	switch mediaType {
	case VIDEO:
		delete(ms.videoTracks, trackName)
		return ms.videoGroupGen.UnregisterTrack(trackName)
	case AUDIO:
		delete(ms.audioTracks, trackName)
		return ms.audioGroupGen.UnregisterTrack(trackName)
	default:
		return ErrUnsupportedMediaType
	}
}

// GetCurrentGroup returns the current group number for the specified media type
func (ms *MediaSyncer) GetCurrentGroup(mediaType MediaType) uint64 {
	ms.mu.RLock()
	defer ms.mu.RUnlock()
	
	switch mediaType {
	case VIDEO:
		return ms.videoGroupGen.GetCurrentGroup()
	case AUDIO:
		return ms.audioGroupGen.GetCurrentGroup()
	default:
		return 0
	}
}

// Start starts both group generators
func (ms *MediaSyncer) Start(ctx context.Context) error {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	
	if err := ms.videoGroupGen.Start(ctx); err != nil {
		return err
	}
	
	if err := ms.audioGroupGen.Start(ctx); err != nil {
		ms.videoGroupGen.Stop()
		return err
	}
	
	return nil
}

// Stop stops both group generators
func (ms *MediaSyncer) Stop() error {
	ms.mu.Lock()
	defer ms.mu.Unlock()
	
	var lastErr error
	if err := ms.videoGroupGen.Stop(); err != nil {
		lastErr = err
	}
	if err := ms.audioGroupGen.Stop(); err != nil {
		lastErr = err
	}
	return lastErr
}

// GroupGenerator manages group timing and generation for a specific media type
type GroupGenerator struct {
	mu            sync.RWMutex
	mediaType     MediaType
	currentGroup  uint64
	groupDuration time.Duration
	tracks        map[string]TrackPublisher
	
	// Control channels
	ctx       context.Context
	cancel    context.CancelFunc
	stopCh    chan struct{}
	ticker    *time.Ticker
	started   bool
}

// NewGroupGenerator creates a new GroupGenerator for the specified media type
func NewGroupGenerator(mediaType MediaType, groupDuration time.Duration) *GroupGenerator {
	return &GroupGenerator{
		mediaType:     mediaType,
		groupDuration: groupDuration,
		tracks:        make(map[string]TrackPublisher),
		stopCh:        make(chan struct{}),
	}
}

// RegisterTrack registers a track publisher with this group generator
func (gg *GroupGenerator) RegisterTrack(trackName string, publisher TrackPublisher) error {
	gg.mu.Lock()
	defer gg.mu.Unlock()
	
	if publisher.GetMediaType() != gg.mediaType {
		return ErrMediaTypeMismatch
	}
	
	gg.tracks[trackName] = publisher
	return nil
}

// UnregisterTrack removes a track publisher from this group generator
func (gg *GroupGenerator) UnregisterTrack(trackName string) error {
	gg.mu.Lock()
	defer gg.mu.Unlock()
	
	delete(gg.tracks, trackName)
	return nil
}

// GetCurrentGroup returns the current group number
func (gg *GroupGenerator) GetCurrentGroup() uint64 {
	gg.mu.RLock()
	defer gg.mu.RUnlock()
	return gg.currentGroup
}

// Start begins group generation for this media type
func (gg *GroupGenerator) Start(ctx context.Context) error {
	gg.mu.Lock()
	defer gg.mu.Unlock()
	
	if gg.started {
		return ErrAlreadyStarted
	}
	
	gg.ctx, gg.cancel = context.WithCancel(ctx)
	gg.ticker = time.NewTicker(gg.groupDuration)
	gg.started = true
	
	// Initialize current group based on current time
	now := time.Now().UnixMilli()
	gg.currentGroup = uint64(now) / uint64(gg.groupDuration.Milliseconds())
	
	go gg.run()
	return nil
}

// Stop stops group generation
func (gg *GroupGenerator) Stop() error {
	gg.mu.Lock()
	defer gg.mu.Unlock()
	
	if !gg.started {
		return nil
	}
	
	gg.cancel()
	gg.ticker.Stop()
	close(gg.stopCh)
	gg.started = false
	return nil
}

// run is the main loop for group generation
func (gg *GroupGenerator) run() {
	for {
		select {
		case <-gg.ctx.Done():
			return
		case <-gg.ticker.C:
			gg.advanceGroup()
		}
	}
}

// advanceGroup increments the current group number
func (gg *GroupGenerator) advanceGroup() {
	gg.mu.Lock()
	defer gg.mu.Unlock()
	gg.currentGroup++
}