package main

import (
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/Eyevinn/mp4ff/bits"
	"github.com/Eyevinn/mp4ff/mp4"
)

var trackIDs = map[string]uint32{
	"video": 1,
	"audio": 2,
}

type cmafMux struct {
	mu         sync.Mutex
	init       *mp4.InitSegment
	inits      map[string]*mp4.InitSegment
	timeScales map[string]int64
	w          io.Writer
}

func newCmafMux(w io.Writer) *cmafMux {
	return &cmafMux{
		w:          w,
		inits:      map[string]*mp4.InitSegment{},
		timeScales: map[string]int64{},
	}
}

func (m *cmafMux) addInit(initData string, contentType string) error {
	if m.inits[contentType] != nil {
		return fmt.Errorf("init already added for %s", contentType)
	}
	initDataBytes, err := base64.StdEncoding.DecodeString(initData)
	if err != nil {
		return err
	}
	sr := bits.NewFixedSliceReader(initDataBytes)
	f, err := mp4.DecodeFileSR(sr)
	if err != nil {
		return err
	}
	if f.Init == nil {
		return fmt.Errorf("no init segment in initData")
	}
	m.inits[contentType] = f.Init
	m.inits[contentType].Moov.Trak.Tkhd.TrackID = trackIDs[contentType]
	m.inits[contentType].Moov.Mvex.Trex.TrackID = trackIDs[contentType]
	m.timeScales[contentType] = int64(f.Init.Moov.Trak.Mdia.Mdhd.Timescale)
	return m.muxInit()
}

func (m *cmafMux) muxInit() error {
	if m.inits["video"] == nil || m.inits["audio"] == nil {
		return nil
	}
	if m.init != nil {
		return fmt.Errorf("init already muxed")
	}
	m.init = mp4.CreateEmptyInit()
	m.init.Moov.AddChild(m.inits["video"].Moov.Trak)
	m.init.Moov.AddChild(m.inits["audio"].Moov.Trak)
	m.init.Moov.Mvex.AddChild(m.inits["video"].Moov.Mvex.Trex)
	m.init.Moov.Mvex.AddChild(m.inits["audio"].Moov.Mvex.Trex)
	return m.init.Encode(m.w)
}

func (m *cmafMux) muxSample(sample []byte, mediaType string) error {
	m.mu.Lock()
	if m.init == nil {
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()
	sr := bits.NewFixedSliceReader(sample)
	f, err := mp4.DecodeFileSR(sr)
	if err != nil {
		return err
	}
	if len(f.Segments) != 1 {
		return fmt.Errorf("expected 1 segment, got %d", len(f.Segments))
	}
	if len(f.Segments[0].Fragments) != 1 {
		return fmt.Errorf("expected 1 fragment, got %d", len(f.Segments[0].Fragments))
	}
	frag := f.Segments[0].Fragments[0]
	trackID := trackIDs[mediaType]
	frag.Moof.Traf.Tfhd.TrackID = trackID
	nowMS := time.Now().UnixMilli()
	mediaMS := 1000 * int64(frag.Moof.Traf.Tfdt.BaseMediaDecodeTime()) / m.timeScales[mediaType]
	slog.Debug("timestamps", "mediaType", mediaType, "mediaMS", mediaMS, "latencyMS", nowMS-mediaMS)
	m.mu.Lock()
	err = frag.Encode(m.w)
	m.mu.Unlock()
	return err
}
