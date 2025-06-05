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
	gg.started = true
	gg.currentGroup = 0 // Will be calculated properly in calculateNextGroup
	
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
		default:
			// Calculate when the next group should start based on object availability
			nextGroup := gg.calculateNextGroup()
			if nextGroup > gg.GetCurrentGroup() {
				gg.setCurrentGroup(nextGroup)
				gg.notifyTracks(nextGroup)
			}
			
			// Calculate and sleep until the next group starts
			sleepDuration := gg.calculateSleepUntilNextGroup()
			if sleepDuration > 0 {
				time.Sleep(sleepDuration)
			} else {
				// If next group should already be available, check again immediately
				time.Sleep(1 * time.Millisecond)
			}
		}
	}
}

// calculateNextGroup determines the current group based on object availability timing
func (gg *GroupGenerator) calculateNextGroup() uint64 {
	gg.mu.RLock()
	defer gg.mu.RUnlock()
	
	// Get a representative track to calculate timing
	// All tracks of the same media type should have similar timing characteristics
	var sampleTrack TrackPublisher
	for _, track := range gg.tracks {
		sampleTrack = track
		break
	}
	
	if sampleTrack == nil {
		return gg.currentGroup
	}
	
	// Use concrete track to get timing information
	if ctp, ok := sampleTrack.(*ConcreteTrackPublisher); ok {
		nowMS := uint64(time.Now().UnixMilli())
		return CurrMoQGroupNr(ctp.track, nowMS, MoqGroupDurMS)
	}
	
	return gg.currentGroup
}

// calculateSleepUntilNextGroup calculates how long to sleep until the next group starts
func (gg *GroupGenerator) calculateSleepUntilNextGroup() time.Duration {
	gg.mu.RLock()
	defer gg.mu.RUnlock()
	
	// Get a representative track to calculate timing
	var sampleTrack TrackPublisher
	for _, track := range gg.tracks {
		sampleTrack = track
		break
	}
	
	if sampleTrack == nil {
		return 100 * time.Millisecond // Default fallback
	}
	
	// Use concrete track to calculate when next group starts
	if ctp, ok := sampleTrack.(*ConcreteTrackPublisher); ok {
		nowMS := uint64(time.Now().UnixMilli())
		currentGroup := CurrMoQGroupNr(ctp.track, nowMS, MoqGroupDurMS)
		nextGroup := currentGroup + 1
		
		// Calculate when the next group's first object becomes available
		// This is based on the availability timing from the README:
		// Object (G, 0) is available at G seconds + sampleOffset + objectDuration
		
		// Calculate object duration in milliseconds
		objectDurMS := float64(ctp.track.SampleDur*uint32(ctp.track.SampleBatch)) * 1000.0 / float64(ctp.track.TimeScale)
		
		// Calculate sample offset based on content type
		var sampleOffsetMS float64
		if ctp.track.ContentType == "audio" {
			// For audio, offset is minimal time later than video given audio sample duration
			audioSampleDurMS := float64(ctp.track.SampleDur) * 1000.0 / float64(ctp.track.TimeScale)
			sampleOffsetMS = audioSampleDurMS
		} else {
			// Video has no sample offset
			sampleOffsetMS = 0
		}
		
		// Next group's first object becomes available at:
		// nextGroup seconds + sampleOffset + objectDuration
		nextGroupStartMS := float64(nextGroup)*1000.0 + sampleOffsetMS + objectDurMS
		
		sleepMS := int64(nextGroupStartMS) - int64(nowMS)
		if sleepMS <= 0 {
			return 0 // Group should already be available
		}
		
		return time.Duration(sleepMS) * time.Millisecond
	}
	
	// Fallback to group duration if we can't calculate precisely
	return gg.groupDuration
}

// setCurrentGroup updates the current group number
func (gg *GroupGenerator) setCurrentGroup(group uint64) {
	gg.mu.Lock()
	defer gg.mu.Unlock()
	gg.currentGroup = group
}

// notifyTracks notifies all registered tracks of a new group
func (gg *GroupGenerator) notifyTracks(group uint64) {
	gg.mu.RLock()
	tracks := make([]TrackPublisher, 0, len(gg.tracks))
	for _, track := range gg.tracks {
		tracks = append(tracks, track)
	}
	gg.mu.RUnlock()
	
	// Notify tracks without holding the lock
	for _, track := range tracks {
		if ctp, ok := track.(*ConcreteTrackPublisher); ok {
			go ctp.onNewGroup(group)
		}
	}
}