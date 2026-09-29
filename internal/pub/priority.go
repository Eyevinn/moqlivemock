package pub

import "github.com/Eyevinn/moqlivemock/internal"

// Publisher priorities, one per kind of track. draft-ietf-moq-transport-18
// Section 7.2 gives them the range 0-255, lower first.
//
// mlmpub's sessions use moqtransport.PublisherPriorityMapper, which reduces a
// priority to a quic-go urgency by its top two bits. Priorities meant to be
// scheduled apart are therefore 64 apart; a smaller difference can land in
// the same urgency, where the scheduler cannot tell them apart.
//
// The catalog goes first, since nothing plays without it. Audio and subtitles
// go ahead of video: they are a small share of the bytes, so letting them
// through first costs video little when the link is short, and an audio gap
// is more disruptive than a late video frame. Video keeps the MOQT default.
const (
	CatalogPriority  = 0
	AudioPriority    = 64
	SubtitlePriority = 64
	VideoPriority    = 128
)

// trackPriority is the publisher priority for a media track's subgroups.
func trackPriority(ct *internal.ContentTrack) uint8 {
	if ct.ContentType == "audio" {
		return AudioPriority
	}
	return VideoPriority
}
