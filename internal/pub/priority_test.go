package pub

import (
	"testing"

	"github.com/Eyevinn/moqlivemock/internal"
	"github.com/Eyevinn/moqtransport"
	"github.com/stretchr/testify/assert"
)

// TestPrioritiesSeparate checks that the priorities keep their order once
// reduced to urgencies, which a difference under 64 need not survive.
func TestPrioritiesSeparate(t *testing.T) {
	urgency := func(p uint8) int8 {
		return moqtransport.PublisherPriorityMapper.MapPriority(
			moqtransport.ObjectPriority{PublisherPriority: p}).Urgency
	}
	assert.Less(t, urgency(CatalogPriority), urgency(AudioPriority), "catalog ahead of audio")
	assert.Equal(t, urgency(AudioPriority), urgency(SubtitlePriority), "audio and subtitles together")
	assert.Less(t, urgency(AudioPriority), urgency(VideoPriority), "audio ahead of video")
}

func TestTrackPriority(t *testing.T) {
	assert.Equal(t, uint8(AudioPriority), trackPriority(&internal.ContentTrack{ContentType: "audio"}))
	assert.Equal(t, uint8(VideoPriority), trackPriority(&internal.ContentTrack{ContentType: "video"}))
}
