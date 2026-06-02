package locmafv02

import (
	"fmt"

	"github.com/Eyevinn/mp4ff/mp4"
)

// parseSenc returns the senc box of the moof's traf (parsing it on
// demand if it is still raw) plus the effective per-sample IV size.
// Mirrors v0.1's getParsedSencBox but is kept private here so the two
// codecs cannot accidentally share state.
func parseSenc(moof *mp4.MoofBox, moov *mp4.MoovBox) (*mp4.SencBox, uint8, error) {
	if moof == nil || moof.Traf == nil {
		return nil, 0, fmt.Errorf("locmafv02: moof or traf not defined")
	}
	traf := moof.Traf
	ok, parsed := traf.ContainsSencBox()
	if !ok {
		return nil, 0, nil
	}
	defaultIVSize := defaultPerSampleIVSize(moov, traf.Tfhd.TrackID)
	if !parsed {
		if err := traf.ParseReadSenc(defaultIVSize, moof.StartPos); err != nil {
			return nil, 0, fmt.Errorf("locmafv02: parse senc: %w", err)
		}
	}
	senc := traf.Senc
	if senc == nil && traf.UUIDSenc != nil {
		senc = traf.UUIDSenc.Senc
	}
	if senc == nil {
		return nil, 0, nil
	}
	return senc, senc.PerSampleIVSize(), nil
}

// safeGetSinf wraps moov.GetSinf so we don't panic on a moov with no
// stsd children (synthetic test moovs). The mp4ff helper indexes
// stsd.Children[0] unconditionally; this wrapper guards that.
func safeGetSinf(moov *mp4.MoovBox, trackID uint32) *mp4.SinfBox {
	if moov == nil {
		return nil
	}
	for _, trak := range moov.Traks {
		if trak == nil || trak.Tkhd == nil || trak.Mdia == nil ||
			trak.Mdia.Minf == nil || trak.Mdia.Minf.Stbl == nil ||
			trak.Mdia.Minf.Stbl.Stsd == nil {
			continue
		}
		stsd := trak.Mdia.Minf.Stbl.Stsd
		if len(stsd.Children) == 0 {
			continue
		}
		if trak.Tkhd.TrackID != trackID {
			continue
		}
		switch sd := stsd.Children[0].(type) {
		case *mp4.VisualSampleEntryBox:
			return sd.Sinf
		case *mp4.AudioSampleEntryBox:
			return sd.Sinf
		}
	}
	return nil
}

// defaultPerSampleIVSize returns the tenc.DefaultPerSampleIVSize for
// the given trackID, or 0 if the moov has no encryption metadata.
func defaultPerSampleIVSize(moov *mp4.MoovBox, trackID uint32) uint8 {
	sinf := safeGetSinf(moov, trackID)
	if sinf == nil || sinf.Schi == nil || sinf.Schi.Tenc == nil {
		return 0
	}
	return sinf.Schi.Tenc.DefaultPerSampleIVSize
}

// shouldCreateEmptySenc reports whether reconstruction should emit a
// senc box with zero per-sample IV size (the constant-IV cbcs case).
func shouldCreateEmptySenc(moov *mp4.MoovBox, trackID uint32, perSampleIVSize uint8) bool {
	if moov == nil || perSampleIVSize != 0 {
		return false
	}
	sinf := safeGetSinf(moov, trackID)
	if sinf == nil || sinf.Schi == nil || sinf.Schi.Tenc == nil {
		return false
	}
	tenc := sinf.Schi.Tenc
	return tenc.DefaultIsProtected == 1 && len(tenc.DefaultConstantIV) > 0
}
