package main

import (
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strings"
)

// mediaPipeline implements MediaPipeline interface
type mediaPipeline struct {
	mediaType   string
	cmafMux     *cmafMux // For muxed output
	videoWriter io.Writer
	audioWriter io.Writer
	logger      *slog.Logger
}

// NewMediaPipeline creates a new media pipeline
func NewMediaPipeline(
	mediaType string,
	cmafMux *cmafMux,
	videoWriter io.Writer,
	audioWriter io.Writer,
) MediaPipeline {
	return &mediaPipeline{
		mediaType:   mediaType,
		cmafMux:     cmafMux,
		videoWriter: videoWriter,
		audioWriter: audioWriter,
		logger:      slog.Default(),
	}
}

// ProcessObject processes a media object through the pipeline
func (mp *mediaPipeline) ProcessObject(obj MediaObject) error {
	mp.logger.Debug("processing media object",
		"mediaType", obj.MediaType,
		"trackName", obj.TrackName,
		"groupID", obj.GroupID,
		"objectID", obj.ObjectID,
		"payloadSize", len(obj.Payload))
	
	// All media objects are processed as media segments
	// ObjectID=0 just means "start of new group", not init segment
	// Init segments come from catalog and are handled separately
	return mp.processMediaSegment(obj)
}

// processInitSegment processes an init segment
func (mp *mediaPipeline) processInitSegment(obj MediaObject) error {
	mp.logger.Info("processing init segment",
		"mediaType", obj.MediaType,
		"trackName", obj.TrackName,
		"groupID", obj.GroupID,
		"payloadSize", len(obj.Payload))
	
	// Convert payload to base64 for mux processing
	initData := base64.StdEncoding.EncodeToString(obj.Payload)
	
	// Add init to CMAF mux if available
	if mp.cmafMux != nil {
		err := mp.cmafMux.addInit(initData, obj.MediaType)
		if err != nil {
			mp.logger.Error("failed to add init to mux",
				"mediaType", obj.MediaType,
				"error", err)
			// Continue processing even if mux fails
		}
	}
	
	// Write init segment to separate output files
	if err := mp.writeToSeparateOutputs(obj); err != nil {
		return fmt.Errorf("failed to write init segment to separate outputs: %w", err)
	}
	
	return nil
}

// processMediaSegment processes a media segment
func (mp *mediaPipeline) processMediaSegment(obj MediaObject) error {
	mp.logger.Debug("processing media segment",
		"mediaType", obj.MediaType,
		"trackName", obj.TrackName,
		"groupID", obj.GroupID,
		"objectID", obj.ObjectID,
		"payloadSize", len(obj.Payload))
	
	// Log start of new groups
	if obj.ObjectID == 0 {
		mp.logger.Info("group start", 
			"mediaType", obj.MediaType,
			"trackName", obj.TrackName, 
			"groupID", obj.GroupID)
	}
	
	// Add sample to CMAF mux if available
	if mp.cmafMux != nil {
		err := mp.cmafMux.muxSample(obj.Payload, obj.MediaType)
		if err != nil {
			mp.logger.Error("failed to mux sample",
				"mediaType", obj.MediaType,
				"error", err)
			// Continue processing even if mux fails
		}
	}
	
	// Write to separate output files
	if err := mp.writeToSeparateOutputs(obj); err != nil {
		return fmt.Errorf("failed to write media segment to separate outputs: %w", err)
	}
	
	return nil
}

// writeToSeparateOutputs writes the object payload to appropriate output writers
func (mp *mediaPipeline) writeToSeparateOutputs(obj MediaObject) error {
	var writer io.Writer
	
	switch obj.MediaType {
	case "video":
		writer = mp.videoWriter
	case "audio":
		writer = mp.audioWriter
	default:
		mp.logger.Warn("unknown media type, not writing to separate output",
			"mediaType", obj.MediaType)
		return nil
	}
	
	if writer == nil {
		// No separate output configured for this media type
		return nil
	}
	
	_, err := writer.Write(obj.Payload)
	if err != nil {
		return fmt.Errorf("failed to write %s payload: %w", obj.MediaType, err)
	}
	
	return nil
}

// Close closes the media pipeline
func (mp *mediaPipeline) Close() error {
	mp.logger.Info("closing media pipeline", "mediaType", mp.mediaType)
	
	// Close writers if they are files
	if closer, ok := mp.videoWriter.(io.Closer); ok && closer != os.Stdout {
		if err := closer.Close(); err != nil {
			mp.logger.Error("error closing video writer", "error", err)
		}
	}
	
	if closer, ok := mp.audioWriter.(io.Closer); ok && closer != os.Stdout {
		if err := closer.Close(); err != nil {
			mp.logger.Error("error closing audio writer", "error", err)
		}
	}
	
	return nil
}

// combinedMediaPipeline handles both video and audio through a single pipeline
type combinedMediaPipeline struct {
	videoPipeline MediaPipeline
	audioPipeline MediaPipeline
	logger        *slog.Logger
}

// NewCombinedMediaPipeline creates a pipeline that handles both video and audio
func NewCombinedMediaPipeline(
	cmafMux *cmafMux,
	videoWriter io.Writer,
	audioWriter io.Writer,
) MediaPipeline {
	return &combinedMediaPipeline{
		videoPipeline: NewMediaPipeline("video", cmafMux, videoWriter, nil),
		audioPipeline: NewMediaPipeline("audio", cmafMux, nil, audioWriter),
		logger:        slog.Default(),
	}
}

// ProcessObject routes objects to the appropriate sub-pipeline
func (cmp *combinedMediaPipeline) ProcessObject(obj MediaObject) error {
	switch obj.MediaType {
	case "video":
		return cmp.videoPipeline.ProcessObject(obj)
	case "audio":
		return cmp.audioPipeline.ProcessObject(obj)
	default:
		return fmt.Errorf("unknown media type: %s", obj.MediaType)
	}
}

// Close closes both sub-pipelines
func (cmp *combinedMediaPipeline) Close() error {
	var errs []string
	
	if err := cmp.videoPipeline.Close(); err != nil {
		errs = append(errs, fmt.Sprintf("video pipeline: %v", err))
	}
	
	if err := cmp.audioPipeline.Close(); err != nil {
		errs = append(errs, fmt.Sprintf("audio pipeline: %v", err))
	}
	
	if len(errs) > 0 {
		return fmt.Errorf("errors closing combined pipeline: %s", strings.Join(errs, ", "))
	}
	
	return nil
}