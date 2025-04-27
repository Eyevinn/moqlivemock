package internal

import (
	"encoding/json"
	"fmt"
)

// Catalog represents the WARP JSON catalog as defined in
// [draft-ietf-moq-warp-00.txt](https://tools.ietf.org/html/draft-ietf-moq-warp-00)
// It provides information about the tracks being produced by a WARP publisher.
type Catalog struct {
	// Version specifies the version of WARP referenced by this catalog.
	// Required field at the root level.
	Version int `json:"version"`

	// SupportsDeltaUpdates indicates if the publisher may issue incremental (delta) updates.
	// Optional field at the root level. Default is false.
	SupportsDeltaUpdates bool `json:"supportsDeltaUpdates,omitempty"`

	// Tracks is an array of track objects.
	// Required field at the root level.
	Tracks []Track `json:"tracks"`
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
}

// CatalogPatch represents a JSON Patch operation as defined in RFC 6902.
// Used for incremental updates to the catalog.
type CatalogPatch []PatchOperation

// PatchOperation represents a single operation in a JSON Patch.
type PatchOperation struct {
	// Op is the operation to perform.
	// Allowed values: "add", "remove", "replace", "move", "copy", "test"
	Op string `json:"op"`

	// Path is a JSON Pointer (RFC 6901) that references a location in the target document.
	Path string `json:"path"`

	// Value is the value to be used within the operation.
	// Used by "add", "replace", and "test" operations.
	Value any `json:"value,omitempty"`

	// From is a JSON Pointer that references the location in the target document
	// to move/copy from.
	// Used by "move" and "copy" operations.
	From string `json:"from,omitempty"`
}
