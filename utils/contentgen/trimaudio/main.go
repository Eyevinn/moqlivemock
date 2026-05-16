// trimaudio post-processes an encoded fragmented audio mp4 so the publisher
// can loop it seamlessly:
//
//  1. drops any priming fragments (those whose tfdt is below elst.mediaTime),
//  2. drops any trailing fragment whose sample duration differs from the bulk
//     sample duration (the "short tail" that AAC/Opus encoders emit),
//  3. trims the kept fragments down to a target frame count whose total media
//     duration is at least 10 s (so the publisher's snap-logic produces the
//     [469,469,469,468]-style cycle pattern that averages exactly 10 s),
//  4. re-anchors every kept fragment's tfdt so the first one starts at 0,
//  5. removes the elst box from the moov.
//
// After processing, the file has uniform-duration samples, no elst, and starts
// at decode time 0. The publisher then never has to think about priming.
//
// Target counts per codec (TS=48000):
//   - AAC  (1024 ts/frame): 469 frames -> 480256 ts = 10.005333 s
//   - Opus (960  ts/frame): 500 frames -> 480000 ts = 10.000000 s
//   - AC-3 (1536 ts/frame): 313 frames -> 480768 ts = 10.016000 s
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/Eyevinn/mp4ff/mp4"
)

type codecSpec struct {
	name         string
	sampleDur    uint32
	targetFrames int
}

// match by codec name extracted from filename suffix
var codecSpecs = map[string]codecSpec{
	"aac":  {"aac", 1024, 469},
	"opus": {"opus", 960, 500},
	"ac3":  {"ac3", 1536, 313},
}

func main() {
	inPlace := flag.Bool("inplace", false, "overwrite the input file (default: write <name>.trimmed.mp4)")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: %s [-inplace] <audio_*_aac.mp4|*_opus.mp4|*_ac3.mp4> ...\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()
	if flag.NArg() == 0 {
		flag.Usage()
		os.Exit(2)
	}

	for _, path := range flag.Args() {
		spec, err := codecFromName(path)
		if err != nil {
			log.Printf("%s: %v", path, err)
			os.Exit(1)
		}
		if err := trim(path, spec, *inPlace); err != nil {
			log.Printf("%s: %v", path, err)
			os.Exit(1)
		}
	}
}

func codecFromName(path string) (codecSpec, error) {
	base := strings.ToLower(filepath.Base(path))
	base = strings.TrimSuffix(base, ".mp4")
	for key, spec := range codecSpecs {
		if strings.HasSuffix(base, "_"+key) {
			return spec, nil
		}
	}
	return codecSpec{}, fmt.Errorf("cannot infer codec from filename (expected suffix _aac, _opus, or _ac3)")
}

func trim(path string, spec codecSpec, inPlace bool) error {
	r, err := os.Open(path)
	if err != nil {
		return err
	}
	file, err := mp4.DecodeFile(r)
	_ = r.Close()
	if err != nil {
		return fmt.Errorf("decode: %w", err)
	}

	if file.Init == nil || file.Init.Moov == nil || file.Init.Moov.Trak == nil {
		return fmt.Errorf("missing init/moov/trak")
	}
	trak := file.Init.Moov.Trak

	// 1. read elst.mediaTime (in media timescale)
	var primingTS uint64
	if trak.Edts != nil && len(trak.Edts.Elst) > 0 && len(trak.Edts.Elst[0].Entries) > 0 {
		primingTS = uint64(trak.Edts.Elst[0].Entries[0].MediaTime)
	}
	// Whole-frame priming only counts when primingTS is a multiple of the
	// sample duration (AAC: 2048/1024 = 2 frames). For Opus (120) and AC-3
	// (256) the priming is a sub-frame amount, so we keep all frames and let
	// the (tiny) priming artifact play out — acceptable for a live stream.
	primingFramesToDrop := uint64(0)
	if spec.sampleDur > 0 && primingTS%uint64(spec.sampleDur) == 0 {
		primingFramesToDrop = primingTS / uint64(spec.sampleDur)
	}

	// 2. walk fragments; identify priming + tail
	trex := file.Init.Moov.Mvex.Trex
	type kept struct {
		seg  *mp4.MediaSegment
		frag *mp4.Fragment
	}
	var (
		keptList      []kept
		droppedPri    uint64
		droppedTail   int
		droppedExcess int
	)
	for _, seg := range file.Segments {
		for _, frag := range seg.Fragments {
			samples, err := frag.GetFullSamples(trex)
			if err != nil {
				return fmt.Errorf("get samples for fragment tfdt=%d: %w",
					frag.Moof.Traf.Tfdt.BaseMediaDecodeTime(), err)
			}
			if len(samples) != 1 {
				return fmt.Errorf("expected one sample per fragment, got %d (tfdt=%d)",
					len(samples), frag.Moof.Traf.Tfdt.BaseMediaDecodeTime())
			}
			if droppedPri < primingFramesToDrop {
				droppedPri++
				continue
			}
			if samples[0].Dur != spec.sampleDur {
				droppedTail++
				continue
			}
			if len(keptList) >= spec.targetFrames {
				droppedExcess++
				continue
			}
			keptList = append(keptList, kept{seg, frag})
		}
	}

	if len(keptList) < spec.targetFrames {
		return fmt.Errorf("not enough uniform-duration frames after priming/tail strip: got %d, want %d",
			len(keptList), spec.targetFrames)
	}

	// 3. re-anchor tfdts: first kept fragment -> 0, then strictly increasing by sampleDur
	for i, k := range keptList {
		newTfdt := uint64(i) * uint64(spec.sampleDur)
		k.frag.Moof.Traf.Tfdt.SetBaseMediaDecodeTime(newTfdt)
	}

	// 4. drop the EdtsBox from trak (both the named field and the Children slice
	//    that the encoder iterates over).
	if trak.Edts != nil {
		newChildren := trak.Children[:0]
		for _, c := range trak.Children {
			if _, ok := c.(*mp4.EdtsBox); ok {
				continue
			}
			newChildren = append(newChildren, c)
		}
		trak.Children = newChildren
		trak.Edts = nil
	}

	// 5. rebuild Segments containing only kept fragments, preserving original
	//    grouping by media segment (so styp boxes etc. stay attached to their
	//    original first fragment).
	var (
		newSegments []*mp4.MediaSegment
		curr        *mp4.MediaSegment
		currOrig    *mp4.MediaSegment
	)
	for _, k := range keptList {
		if k.seg != currOrig {
			// shallow-copy the segment header (styp, sidx) but reset Fragments
			ns := *k.seg
			ns.Fragments = nil
			curr = &ns
			newSegments = append(newSegments, curr)
			currOrig = k.seg
		}
		curr.Fragments = append(curr.Fragments, k.frag)
	}
	file.Segments = newSegments

	// 6. encode
	outPath := path
	if !inPlace {
		ext := filepath.Ext(path)
		outPath = strings.TrimSuffix(path, ext) + ".trimmed" + ext
	}
	tmpPath := outPath + ".tmp"
	w, err := os.Create(tmpPath)
	if err != nil {
		return err
	}
	if err := file.Encode(w); err != nil {
		_ = w.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("encode: %w", err)
	}
	if err := w.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	if err := os.Rename(tmpPath, outPath); err != nil {
		return err
	}

	fmt.Printf("%s -> %s: kept %d frames (dropped %d priming, %d short-tail, %d excess); total=%d ts (%.6f s)\n",
		path, outPath, len(keptList), int(droppedPri), droppedTail, droppedExcess,
		uint64(len(keptList))*uint64(spec.sampleDur),
		float64(uint64(len(keptList))*uint64(spec.sampleDur))/float64(trak.Mdia.Mdhd.Timescale))
	return nil
}
