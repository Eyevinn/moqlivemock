package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/Eyevinn/moqlivemock/internal"
	"github.com/mengelbart/moqtransport"
)

// SimpleClient represents a basic MoQ client using the modular architecture
type SimpleClient struct {
	namespace        []string
	subscriptionMgr  SubscriptionManager
	mediaRouter      MediaRouter
	mediaPipeline    MediaPipeline
	mediaChannel     MediaChannel
	logger           *slog.Logger
	
	// Output writers
	muxout    io.Writer
	videoout  io.Writer
	audioout  io.Writer
	
	// CMAF mux for direct access
	cmafMux   *cmafMux
}

// NewSimpleClient creates a new simple client
func NewSimpleClient(namespace []string, muxout, videoout, audioout io.Writer) *SimpleClient {
	return &SimpleClient{
		namespace:    namespace,
		mediaChannel: make(MediaChannel, 100), // Buffered channel
		logger:       slog.Default(),
		muxout:       muxout,
		videoout:     videoout,
		audioout:     audioout,
	}
}

// RunSimplePlayback runs basic playback without switching
func (c *SimpleClient) RunSimplePlayback(ctx context.Context, session *moqtransport.Session) error {
	c.logger.Info("starting simple playback")
	
	// Initialize components
	if err := c.initializeComponents(session); err != nil {
		return fmt.Errorf("failed to initialize components: %w", err)
	}
	defer c.cleanup()
	
	// Subscribe to catalog first to discover tracks  
	c.logger.Info("attempting to subscribe to catalog")
	catalog, err := c.subscribeToCatalog(ctx, session)
	if err != nil {
		c.logger.Error("failed to subscribe to catalog", "error", err)
		return fmt.Errorf("failed to subscribe to catalog: %w", err)
	}
	c.logger.Info("catalog subscription successful")
	
	// Find first video and audio tracks
	var videoTrack, audioTrack *internal.Track
	for _, track := range catalog.Tracks {
		if videoTrack == nil && isVideoTrack(&track) {
			videoTrack = &track
		}
		if audioTrack == nil && isAudioTrack(&track) {
			audioTrack = &track
		}
		if videoTrack != nil && audioTrack != nil {
			break
		}
	}
	
	if videoTrack == nil && audioTrack == nil {
		return fmt.Errorf("no video or audio tracks found in catalog")
	}
	
	// Initialize tracks with init segments before subscribing
	if videoTrack != nil {
		c.logger.Info("initializing video track", "name", videoTrack.Name)
		if err := c.initializeTrack(videoTrack, "video"); err != nil {
			return fmt.Errorf("failed to initialize video track: %w", err)
		}
		
		c.logger.Info("subscribing to video track", "name", videoTrack.Name)
		_, err := c.subscriptionMgr.Subscribe(ctx, videoTrack.Name, "")
		if err != nil {
			return fmt.Errorf("failed to subscribe to video track: %w", err)
		}
	}
	
	if audioTrack != nil {
		c.logger.Info("initializing audio track", "name", audioTrack.Name)
		if err := c.initializeTrack(audioTrack, "audio"); err != nil {
			return fmt.Errorf("failed to initialize audio track: %w", err)
		}
		
		c.logger.Info("subscribing to audio track", "name", audioTrack.Name)
		_, err := c.subscriptionMgr.Subscribe(ctx, audioTrack.Name, "")
		if err != nil {
			return fmt.Errorf("failed to subscribe to audio track: %w", err)
		}
	}
	
	c.logger.Info("simple playback started, waiting for context cancellation")
	
	// Wait for context cancellation
	<-ctx.Done()
	
	c.logger.Info("simple playback stopped")
	return ctx.Err()
}

// initializeComponents initializes all the components
func (c *SimpleClient) initializeComponents(session *moqtransport.Session) error {
	// Create CMAF mux if muxout is configured
	if c.muxout != nil {
		c.cmafMux = newCmafMux(c.muxout)
	}
	
	// Create media pipeline
	c.mediaPipeline = NewCombinedMediaPipeline(
		c.cmafMux,
		c.videoout,
		c.audioout,
	)
	
	// Create media router
	c.mediaRouter = NewMediaRouter(c.mediaChannel)
	
	// Register pipelines with router
	c.mediaRouter.RegisterPipeline("video", c.mediaPipeline)
	c.mediaRouter.RegisterPipeline("audio", c.mediaPipeline)
	
	// Create subscription manager
	namespaceStr := strings.Join(c.namespace, "/")
	c.subscriptionMgr = NewSubscriptionManager(
		session,
		namespaceStr,
		c.mediaChannel,
	)
	
	return nil
}

// cleanup cleans up all components
func (c *SimpleClient) cleanup() {
	c.logger.Info("cleaning up simple client components")
	
	if c.subscriptionMgr != nil {
		c.subscriptionMgr.Close()
	}
	
	if c.mediaRouter != nil {
		c.mediaRouter.Close()
	}
	
	if c.mediaPipeline != nil {
		c.mediaPipeline.Close()
	}
}

// subscribeToCatalog subscribes to the catalog and returns parsed catalog
func (c *SimpleClient) subscribeToCatalog(ctx context.Context, session *moqtransport.Session) (*internal.Catalog, error) {
	c.logger.Info("subscribing to catalog", "namespace", c.namespace)
	
	rs, err := session.Subscribe(ctx, c.namespace, "catalog", "")
	if err != nil {
		c.logger.Error("session.Subscribe failed", "error", err)
		return nil, fmt.Errorf("failed to subscribe to catalog: %w", err)
	}
	defer rs.Close()
	
	c.logger.Info("subscription created, reading catalog object")
	
	// Read catalog object
	obj, err := rs.ReadObject(ctx)
	if err != nil {
		c.logger.Error("failed to read catalog object", "error", err)
		return nil, fmt.Errorf("failed to read catalog object: %w", err)
	}
	
	c.logger.Info("catalog object received", 
		"groupID", obj.GroupID, 
		"objectID", obj.ObjectID,
		"payloadSize", len(obj.Payload))
	
	// Parse catalog
	var catalog internal.Catalog
	err = json.Unmarshal(obj.Payload, &catalog)
	if err != nil {
		c.logger.Error("failed to parse catalog JSON", "error", err, "payload", string(obj.Payload[:min(100, len(obj.Payload))]))
		return nil, fmt.Errorf("failed to parse catalog: %w", err)
	}
	
	c.logger.Info("catalog parsed successfully", "tracks", len(catalog.Tracks))
	return &catalog, nil
}

// Helper functions to determine track types
func isVideoTrack(track *internal.Track) bool {
	return track.MimeType != "" && 
		   (track.MimeType == "video/mp4" || 
		    track.MimeType == "video/cmaf" ||
		    strings.Contains(track.MimeType, "video"))
}

func isAudioTrack(track *internal.Track) bool {
	return track.MimeType != "" && 
		   (track.MimeType == "audio/mp4" || 
		    track.MimeType == "audio/cmaf" ||
		    strings.Contains(track.MimeType, "audio"))
}

// initializeTrack writes the init segment for a track and adds it to mux
func (c *SimpleClient) initializeTrack(track *internal.Track, mediaType string) error {
	if track.InitData == "" {
		c.logger.Warn("track has no init data", "trackName", track.Name, "mediaType", mediaType)
		return nil
	}
	
	c.logger.Info("writing init segment", 
		"trackName", track.Name, 
		"mediaType", mediaType,
		"initDataLength", len(track.InitData))
	
	// Write init segment to separate output file
	var writer io.Writer
	switch mediaType {
	case "video":
		writer = c.videoout
	case "audio":
		writer = c.audioout
	}
	
	if writer != nil {
		err := unpackWrite(track.InitData, writer)
		if err != nil {
			return fmt.Errorf("failed to write %s init data: %w", mediaType, err)
		}
		c.logger.Info("wrote init segment to separate output", "mediaType", mediaType)
	}
	
	// Add init segment directly to CMAF mux (not through pipeline)
	// The mux needs both video and audio init segments before it can create the combined init
	if err := c.addInitToMux(track.InitData, mediaType); err != nil {
		c.logger.Error("failed to add init segment to mux", 
			"mediaType", mediaType, "error", err)
		// Don't return error, continue with subscription
	}
	
	return nil
}

// addInitToMux adds init segment directly to the CMAF mux (like old architecture)
func (c *SimpleClient) addInitToMux(initData string, mediaType string) error {
	if c.cmafMux == nil {
		return nil // No mux configured
	}
	
	c.logger.Info("adding init segment to mux", "mediaType", mediaType)
	err := c.cmafMux.addInit(initData, mediaType)
	if err != nil {
		return fmt.Errorf("failed to add %s init to mux: %w", mediaType, err)
	}
	
	c.logger.Info("init segment added to mux successfully", "mediaType", mediaType)
	return nil
}

