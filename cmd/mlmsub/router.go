package main

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// mediaRouter implements MediaRouter interface
type mediaRouter struct {
	mediaChannel MediaChannel
	pipelines    map[string]MediaPipeline // mediaType -> pipeline
	activeTracks map[string]string        // mediaType -> active trackName
	
	// Duplicate detection
	groupTracks map[uint64]map[string]time.Time // groupID -> (trackName -> timestamp)
	
	logger *slog.Logger
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	mu     sync.RWMutex
}

// NewMediaRouter creates a new media router
func NewMediaRouter(mediaChannel MediaChannel) MediaRouter {
	ctx, cancel := context.WithCancel(context.Background())
	
	mr := &mediaRouter{
		mediaChannel: mediaChannel,
		pipelines:    make(map[string]MediaPipeline),
		activeTracks: make(map[string]string),
		groupTracks:  make(map[uint64]map[string]time.Time),
		logger:       slog.Default(),
		ctx:          ctx,
		cancel:       cancel,
	}
	
	// Start the routing goroutine
	mr.wg.Add(1)
	go mr.routingLoop()
	
	return mr
}

// RegisterPipeline registers a pipeline for a specific media type
func (mr *mediaRouter) RegisterPipeline(mediaType string, pipeline MediaPipeline) {
	mr.mu.Lock()
	defer mr.mu.Unlock()
	
	mr.pipelines[mediaType] = pipeline
	mr.logger.Info("registered pipeline", "mediaType", mediaType)
}

// SetActiveTrack sets the active track for a media type
func (mr *mediaRouter) SetActiveTrack(mediaType string, trackName string) {
	mr.mu.Lock()
	defer mr.mu.Unlock()
	
	mr.activeTracks[mediaType] = trackName
	mr.logger.Info("set active track", "mediaType", mediaType, "trackName", trackName)
}

// RouteObject routes a media object to the appropriate pipeline
func (mr *mediaRouter) RouteObject(obj MediaObject) {
	mr.mu.RLock()
	defer mr.mu.RUnlock()
	
	// Check for duplicate groups
	if mr.hasDuplicateGroup(obj) {
		mr.logger.Warn("multiple tracks for same group",
			"mediaType", obj.MediaType,
			"groupID", obj.GroupID,
			"trackName", obj.TrackName,
			"tracks", mr.getTracksForGroup(obj.GroupID))
		
		// Prefer new track (more recent timestamp)
		if !mr.shouldPreferNewTrack(obj) {
			mr.logger.Debug("dropping duplicate object from old track",
				"trackName", obj.TrackName,
				"groupID", obj.GroupID,
				"objectID", obj.ObjectID)
			return
		}
	}
	
	// Record this object's track and timestamp
	mr.recordObjectTrack(obj)
	
	// Route to appropriate pipeline
	if pipeline, ok := mr.pipelines[obj.MediaType]; ok {
		err := pipeline.ProcessObject(obj)
		if err != nil {
			mr.logger.Error("error processing object in pipeline",
				"mediaType", obj.MediaType,
				"trackName", obj.TrackName,
				"groupID", obj.GroupID,
				"objectID", obj.ObjectID,
				"error", err)
		}
	} else {
		mr.logger.Warn("no pipeline registered for media type",
			"mediaType", obj.MediaType,
			"trackName", obj.TrackName)
	}
}

// Close closes the media router
func (mr *mediaRouter) Close() {
	mr.logger.Info("closing media router")
	
	// Cancel context to stop routing loop
	mr.cancel()
	
	// Wait for routing loop to finish
	mr.wg.Wait()
	
	// Close all pipelines
	mr.mu.Lock()
	defer mr.mu.Unlock()
	
	for mediaType, pipeline := range mr.pipelines {
		if err := pipeline.Close(); err != nil {
			mr.logger.Error("error closing pipeline",
				"mediaType", mediaType,
				"error", err)
		}
	}
}

// routingLoop runs the main routing loop
func (mr *mediaRouter) routingLoop() {
	defer mr.wg.Done()
	
	mr.logger.Info("starting media routing loop")
	
	for {
		select {
		case <-mr.ctx.Done():
			mr.logger.Info("media routing loop stopped")
			return
		case obj := <-mr.mediaChannel:
			mr.RouteObject(obj)
		}
	}
}

// hasDuplicateGroup checks if this group has been seen from multiple tracks
func (mr *mediaRouter) hasDuplicateGroup(obj MediaObject) bool {
	tracks, exists := mr.groupTracks[obj.GroupID]
	if !exists {
		return false
	}
	
	// Check if there are other tracks for this group
	for trackName := range tracks {
		if trackName != obj.TrackName {
			return true
		}
	}
	
	return false
}

// shouldPreferNewTrack determines if we should prefer the new track over existing ones
func (mr *mediaRouter) shouldPreferNewTrack(obj MediaObject) bool {
	tracks, exists := mr.groupTracks[obj.GroupID]
	if !exists {
		return true // First track for this group
	}
	
	// Prefer track with more recent timestamp (newer track)
	objTime := obj.Timestamp
	for trackName, timestamp := range tracks {
		if trackName != obj.TrackName && timestamp.After(objTime) {
			return false // Existing track is newer
		}
	}
	
	return true // This track is newer or equal
}

// recordObjectTrack records the track and timestamp for this object's group
func (mr *mediaRouter) recordObjectTrack(obj MediaObject) {
	if _, exists := mr.groupTracks[obj.GroupID]; !exists {
		mr.groupTracks[obj.GroupID] = make(map[string]time.Time)
	}
	
	mr.groupTracks[obj.GroupID][obj.TrackName] = obj.Timestamp
	
	// Clean up old groups to prevent memory leak
	// Keep only recent groups (last 100 groups)
	if len(mr.groupTracks) > 100 {
		mr.cleanupOldGroups()
	}
}

// getTracksForGroup returns the list of tracks for a given group
func (mr *mediaRouter) getTracksForGroup(groupID uint64) []string {
	tracks, exists := mr.groupTracks[groupID]
	if !exists {
		return nil
	}
	
	result := make([]string, 0, len(tracks))
	for trackName := range tracks {
		result = append(result, trackName)
	}
	
	return result
}

// cleanupOldGroups removes old group tracking data
func (mr *mediaRouter) cleanupOldGroups() {
	// Simple cleanup: remove groups older than 30 seconds
	cutoff := time.Now().Add(-30 * time.Second)
	
	for groupID, tracks := range mr.groupTracks {
		allOld := true
		for _, timestamp := range tracks {
			if timestamp.After(cutoff) {
				allOld = false
				break
			}
		}
		
		if allOld {
			delete(mr.groupTracks, groupID)
		}
	}
}