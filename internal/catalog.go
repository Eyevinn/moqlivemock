package internal

import (
	"encoding/json"
	"fmt"
)

// Catalog represents the WARP JSON catalog as defined in
// [draft-ietf-moq-warp](https://moq-wg.github.io/warp-streaming-format/draft-ietf-moq-warp.html)
// as of 28 Apr 2025 17:43:00 +0200
// It provides information about the tracks being produced by a WARP publisher.
type Catalog struct {
	// Version specifies the version of WARP referenced by this catalog.
	// Required field at the root level.
	Version int `json:"version"`

	// DeltaUpdate indicates that this catalog object represents a delta (or partial) update.
	// Optional field at the root level.
	DeltaUpdate bool `json:"deltaUpdate,omitempty"`

	// AddTracks indicates a delta processing instruction to add new tracks.
	// Optional field at the root level, only used in delta updates.
	AddTracks []Track `json:"addTracks,omitempty"`

	// RemoveTracks indicates a delta processing instruction to remove tracks.
	// Optional field at the root level, only used in delta updates.
	RemoveTracks []Track `json:"removeTracks,omitempty"`

	// CloneTracks indicates a delta processing instruction to clone new tracks from previously declared tracks.
	// Optional field at the root level, only used in delta updates.
	CloneTracks []Track `json:"cloneTracks,omitempty"`

	// Tracks is an array of track objects.
	// Required field at the root level for non-delta updates.
	Tracks []Track `json:"tracks,omitempty"`

	// SupportsDeltaUpdates indicates if the publisher may issue incremental (delta) updates.
	// Optional field at the root level. Default is false.
	SupportsDeltaUpdates bool `json:"supportsDeltaUpdates,omitempty"`
}

func (c *Catalog) GetTrackByName(name string) *Track {
	for _, track := range c.Tracks {
		if track.Name == name {
			return &track
		}
	}
	return nil
}

// String returns a JSON string representation of the catalog with indentation.
// The InitData fields longer than 20 characters are shortened to show only the first 20 characters
// followed by "..." and the total length.
func (c *Catalog) String() string {
	// Create a deep copy of the catalog to modify InitData
	copyCat := *c
	copyTracks := make([]Track, len(c.Tracks))

	for i, track := range c.Tracks {
		copyTracks[i] = track
		if len(track.InitData) > 20 {
			copyTracks[i].InitData = track.InitData[:20] + "..." + fmt.Sprintf("(len=%d)", len(track.InitData))
		}
	}

	copyCat.Tracks = copyTracks

	// Marshal with indentation
	jsonBytes, err := json.MarshalIndent(copyCat, "", "  ")
	if err != nil {
		return fmt.Sprintf("Error marshaling catalog: %v", err)
	}

	return string(jsonBytes)
}

// Track represents a track object in the WARP catalog.
type Track struct {
	// Name defines the name of the track.
	// Required field at the track level.
	Name string `json:"name"`

	// Namespace is the namespace under which the track name is defined.
	// Optional field at the track level.
	Namespace string `json:"namespace,omitempty"`

	// Packaging defines the type of payload encapsulation.
	// Required field at the track level. Allowed values: "loc", but we also use "cmaf"
	Packaging string `json:"packaging"`

	// Label is a human-readable label for the track.
	// Optional field at the track level.
	Label string `json:"label,omitempty"`

	// RenderGroup specifies a group of tracks which are designed to be rendered together.
	// Optional field at the track level.
	RenderGroup *int `json:"renderGroup,omitempty"`

	// AltGroup specifies a group of tracks which are alternate versions of one-another.
	// Optional field at the track level.
	AltGroup *int `json:"altGroup,omitempty"`

	// InitData holds Base64 encoded initialization data for the track.
	// Optional field at the track level. We use this for CMAF init segment.
	InitData string `json:"initData,omitempty"`

	// Dependencies holds an array of track names on which the current track is dependent.
	// Optional field at the track level.
	Dependencies []string `json:"depends,omitempty"`

	// TemporalID identifies the temporal layer/sub-layer encoding of the track.
	// Optional field at the track level.
	TemporalID *int `json:"temporalId,omitempty"`

	// SpatialID identifies the spatial layer encoding of the track.
	// Optional field at the track level.
	SpatialID *int `json:"spatialId,omitempty"`

	// Codec defines the codec used to encode the track.
	// Optional field at the track level.
	Codec string `json:"codec,omitempty"`

	// MimeType defines the mime type of the track.
	// Optional field at the track level.
	MimeType string `json:"mimeType,omitempty"`

	// Framerate defines the video framerate of the track, expressed as frames per second.
	// Optional field at the track level.
	Framerate *float64 `json:"framerate,omitempty"`

	// Bitrate defines the bitrate of track, expressed in bits per second.
	// Optional field at the track level.
	Bitrate *int `json:"bitrate,omitempty"`

	// Width expresses the encoded width of the video frames in pixels.
	// Optional field at the track level.
	Width *int `json:"width,omitempty"`

	// Height expresses the encoded height of the video frames in pixels.
	// Optional field at the track level.
	Height *int `json:"height,omitempty"`

	// SampleRate is the number of audio frame samples per second.
	// Optional field at the track level, should only accompany audio codecs.
	SampleRate *int `json:"samplerate,omitempty"`

	// ChannelConfig specifies the audio channel configuration.
	// Optional field at the track level, should only accompany audio codecs.
	ChannelConfig string `json:"channelConfig,omitempty"`

	// DisplayWidth expresses the intended display width of the track content in pixels.
	// Optional field at the track level.
	DisplayWidth *int `json:"displayWidth,omitempty"`

	// DisplayHeight expresses the intended display height of the track content in pixels.
	// Optional field at the track level.
	DisplayHeight *int `json:"displayHeight,omitempty"`

	// Language defines the dominant language of the track.
	// Optional field at the track level.
	Language string `json:"lang,omitempty"`

	// ParentName defines the parent track name to be cloned.
	// This field is only included inside a CloneTracks object.
	ParentName string `json:"parentName,omitempty"`
}
