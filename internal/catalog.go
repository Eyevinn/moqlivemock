package internal

import (
	"encoding/json"
	"fmt"
)

// Catalog represents the MSF JSON catalog as defined in
// draft-ietf-moq-msf-01 (MOQT Streaming Format) and
// draft-ietf-moq-cmsf-00 (CMAF MOQT Streaming Format).
// It provides information about the tracks being produced by an MSF publisher.
type Catalog struct {
	// Version specifies the version of MSF referenced by this catalog.
	// Required field at the root level. Draft-01 makes this a JSON string
	// and, per Section 5.1.1, recommends the "draft-XX" convention when
	// running against IETF Internet-Draft releases (here "draft-01").
	Version string `json:"version"`

	// GeneratedAt is the wallclock time at which this catalog was generated,
	// expressed as milliseconds since the Unix epoch.
	// Optional field at the root level. Should be present when tracks are live.
	GeneratedAt *int64 `json:"generatedAt,omitempty"`

	// IsComplete signals that a previously live broadcast is complete.
	// All tracks are complete, no new tracks will be added.
	// Optional field at the root level. Must not be included if false.
	IsComplete bool `json:"isComplete,omitempty"`

	// DeltaUpdate is an ordered array of operations to apply to the catalog
	// (draft-ietf-moq-msf-01 Section 5.1.6). Optional field at the root level,
	// only used in delta updates.
	DeltaUpdate []DeltaOperation `json:"deltaUpdate,omitempty"`

	// Tracks is an array of track objects.
	// Required field at the root level for non-delta updates.
	Tracks []Track `json:"tracks,omitempty"`

	// InitDataList holds initialization data referenced by tracks via InitRef
	// (draft-ietf-moq-msf-01 Section 5.1.7). Two tracks (e.g. a CMAF track and
	// its LOCMAF counterpart) may reference the same entry.
	// Optional field at the root level.
	InitDataList []InitData `json:"initDataList,omitempty"`

	// ContentProtections contains content protection information.
	// No content protection is information is repeated at the track level,
	// all tracks must refer to this common field.
	// Optional field at the root level, only used if content is protected.
	ContentProtections []ContentProtection `json:"contentProtections,omitempty"`
}

// DeltaOperation is a single delta-update operation
// (draft-ietf-moq-msf-01 Section 5.1.6).
type DeltaOperation struct {
	// Op is the operation type: "add", "remove" or "clone".
	Op string `json:"op"`
	// Tracks is the array of track objects the operation applies to.
	Tracks []Track `json:"tracks"`
}

// InitData is an entry in the catalog-level InitDataList
// (draft-ietf-moq-msf-01 Section 5.1.7).
type InitData struct {
	// ID is a reference to this initialization data, unique within the catalog.
	ID string `json:"id"`
	// Type is the type of reference. Currently only "inline" is defined.
	Type string `json:"type"`
	// Data holds the init payload as defined by Type. For "inline" it is
	// Base64-encoded initialization data.
	Data string `json:"data"`
}

func (c *Catalog) GetTrackByName(name string) *Track {
	for _, track := range c.Tracks {
		if track.Name == name {
			return &track
		}
	}
	return nil
}

// InitDataFor resolves a track's InitRef against the catalog InitDataList and
// returns the Base64-encoded init payload. The boolean is false when the track
// has no InitRef or the referenced entry is missing.
func (c *Catalog) InitDataFor(t *Track) (string, bool) {
	if t == nil || t.InitRef == "" {
		return "", false
	}
	for _, id := range c.InitDataList {
		if id.ID == t.InitRef {
			return id.Data, true
		}
	}
	return "", false
}

// String returns a JSON string representation of the catalog with indentation.
// InitDataList Data fields longer than 20 characters are shortened to show only
// the first 20 characters followed by "..." and the total length.
func (c *Catalog) String() string {
	// Create a shallow copy of the catalog to shorten init payloads.
	copyCat := *c
	copyInit := make([]InitData, len(c.InitDataList))

	for i, id := range c.InitDataList {
		copyInit[i] = id
		if len(id.Data) > 20 {
			copyInit[i].Data = id.Data[:20] + "..." + fmt.Sprintf("(len=%d)", len(id.Data))
		}
	}

	copyCat.InitDataList = copyInit

	// Marshal with indentation
	jsonBytes, err := json.MarshalIndent(copyCat, "", "  ")
	if err != nil {
		return fmt.Sprintf("Error marshaling catalog: %v", err)
	}

	return string(jsonBytes)
}

// Track represents a track object in the MSF/CMSF catalog.
type Track struct {
	// Name defines the name of the track.
	// Required field at the track level.
	Name string `json:"name"`

	// Namespace is the namespace under which the track name is defined.
	// Optional field at the track level.
	Namespace string `json:"namespace,omitempty"`

	// Packaging defines the type of payload encapsulation.
	// Required field at the track level. MSF values: "loc", "mediatimeline", "eventtimeline".
	// CMSF adds: "cmaf", a custom Low Overhead CMAF variant uses "locmaf".
	Packaging string `json:"packaging"`

	// LocmafVersion advertises the LOCMAF wire-format version when
	// Packaging == "locmaf". Receivers should compare against their
	// highest supported version and fall back if the encoder is ahead.
	// Omitted for non-LOCMAF packagings.
	LocmafVersion string `json:"locmafVersion,omitempty"`

	// IsLive indicates whether new objects will be added to the track.
	// Required field at the track level (MSF Section 5.1.15).
	IsLive bool `json:"isLive"`

	// TargetLatency is the target latency in milliseconds.
	// Optional field at the track level. Must not be included if IsLive is false.
	TargetLatency *int `json:"targetLatency,omitempty"`

	// Role defines the role of content carried by the track.
	// Optional field at the track level. Reserved values include:
	// "video", "audio", "subtitle", "caption", "audiodescription",
	// "mediatimeline", "eventtimeline", "signlanguage".
	Role string `json:"role,omitempty"`

	// Label is a human-readable label for the track.
	// Optional field at the track level.
	Label string `json:"label,omitempty"`

	// RenderGroup specifies a group of tracks which are designed to be rendered together.
	// Optional field at the track level.
	RenderGroup *int `json:"renderGroup,omitempty"`

	// AltGroup specifies a group of tracks which are alternate versions of one-another.
	// Optional field at the track level.
	AltGroup *int `json:"altGroup,omitempty"`

	// InitRef points at the id field of an entry in the catalog-level
	// InitDataList (draft-ietf-moq-msf-01 Section 5.2.13).
	// Optional field at the track level.
	InitRef string `json:"initRef,omitempty"`

	// Dependencies holds an array of track names on which the current track is dependent.
	// Optional field at the track level.
	Dependencies []string `json:"depends,omitempty"`

	// TemporalID identifies the temporal layer/sub-layer encoding of the track.
	// Optional field at the track level.
	TemporalID *int `json:"temporalId,omitempty"`

	// SpatialID identifies the spatial layer encoding of the track.
	// Optional field at the track level.
	SpatialID *int `json:"spatialId,omitempty"`

	// Codec defines the codec used to encode the track
	// (draft-ietf-moq-msf-01 Section 5.2.18). Conditionally required: it
	// MUST be specified for tracks which have an inherent codec (e.g. audio
	// and video tracks); not required for raw data tracks or event streams.
	Codec string `json:"codec,omitempty"`

	// Framerate defines the video framerate of the track, expressed as frames per second.
	// Optional field at the track level.
	Framerate *float64 `json:"framerate,omitempty"`

	// Timescale is the number of time units that pass per second.
	// Optional field at the track level (MSF Section 5.1.27).
	Timescale *int `json:"timescale,omitempty"`

	// Bitrate defines the maximum bitrate of the track, expressed in bits
	// per second (draft-ietf-moq-msf-01 Section 5.2.22, JSON key "bitrate").
	// Conditionally required: it MUST be specified for audio and video tracks.
	Bitrate *int `json:"bitrate,omitempty"`

	// Width expresses the encoded width of the video frames in pixels.
	// Optional field at the track level.
	Width *int `json:"width,omitempty"`

	// Height expresses the encoded height of the video frames in pixels.
	// Optional field at the track level.
	Height *int `json:"height,omitempty"`

	// SampleRate is the number of audio frame samples per second
	// (draft-ietf-moq-msf-01 Section 5.2.28). Conditionally required: it
	// MUST accompany tracks for which audio codecs are specified.
	SampleRate *int `json:"samplerate,omitempty"`

	// ChannelConfig specifies the audio channel configuration
	// (draft-ietf-moq-msf-01 Section 5.2.29). Conditionally required: it
	// MUST accompany tracks for which audio codecs are specified.
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

	// TrackDuration is the duration of the track in integer milliseconds.
	// Optional field at the track level. Must not be included if IsLive is true.
	TrackDuration *int `json:"trackDuration,omitempty"`

	// EventType defines the type & structure of data in an event timeline track.
	// Optional field at the track level. Required when packaging is "eventtimeline".
	EventType string `json:"eventType,omitempty"`

	// ParentName defines the parent track name to be cloned.
	// This field is only included inside a "clone" DeltaOperation's tracks.
	ParentName string `json:"parentName,omitempty"`

	// ContentProtectionRefIDs defines which content protection information should be used for this track.
	// The ID is used as a key in the root-level field ContentProtection.
	// Optional field at the track level, only used when the track is protected.
	ContentProtectionRefIDs []string `json:"contentProtectionRefIDs,omitempty"`
}

// ContentProtection contains all information needed to create DRM request
type ContentProtection struct {
	RefID       string     `json:"refID,omitempty"`
	DefaultKIDs []string   `json:"defaultKID,omitempty"`
	Scheme      string     `json:"scheme,omitempty"`
	DRMSystem   *DRMSystem `json:"drmSystem,omitempty"`
}

// DRMSystem represents information related to a DRM system
type DRMSystem struct {
	SystemID   string      `json:"systemID,omitempty"`
	Robustness string      `json:"robustness,omitempty"`
	LaURL      *DRMService `json:"laURL,omitempty"`
	AuthzURL   *DRMService `json:"authzURL,omitempty"`
	CertURL    *DRMService `json:"certURL,omitempty"`
	Pssh       string      `json:"pssh,omitempty"`
}

// DRMService represents a license or authorization service.
type DRMService struct {
	URL  string `json:"url,omitempty"`
	Type string `json:"type,omitempty"`
}
