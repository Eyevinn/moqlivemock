package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/Eyevinn/moqlivemock/internal"
	"github.com/Eyevinn/mp4ff/mp4"
)

const defaultRoundtripInput = "../../assets/test10s/video_400kbps_avc.mp4"

// segmentPaths collects repeated -segment flag values.
type segmentPaths []string

func (s *segmentPaths) String() string     { return strings.Join(*s, ",") }
func (s *segmentPaths) Set(v string) error { *s = append(*s, v); return nil }

func runRoundtripCommand(args []string) error {
	flags := flag.NewFlagSet("roundtrip", flag.ContinueOnError)
	inputPath := flags.String("input", "", "single fragmented MP4 to round-trip (mutually exclusive with -init)")
	initPath := flags.String("init", "", "CMAF init segment (used with one or more -segment)")
	var segments segmentPaths
	flags.Var(&segments, "segment", "CMAF media segment (.m4s); repeat for multiple")
	trackInfoPath := flags.String("track-info", "",
		"optional CMSF Track JSON (overrides metadata otherwise synthesised from the moov)")
	verbose := flags.Bool("verbose", false, "print per-fragment stats")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("unexpected positional arguments: %v", flags.Args())
	}

	if *inputPath == "" && *initPath == "" {
		*inputPath = defaultRoundtripInput
	}
	if *inputPath != "" && *initPath != "" {
		return fmt.Errorf("-input and -init are mutually exclusive")
	}
	if *initPath != "" && len(segments) == 0 {
		return fmt.Errorf("-init requires at least one -segment")
	}

	return runRoundtrip(*inputPath, *initPath, segments, *trackInfoPath, *verbose)
}

func runRoundtrip(inputPath, initPath string, segmentPaths []string, trackInfoPath string, verbose bool) error {
	moov, initBytes, fragments, sourceLabel, err := loadMoovAndFragments(inputPath, initPath, segmentPaths)
	if err != nil {
		return err
	}

	track, trackSource, err := resolveTrack(moov, trackInfoPath)
	if err != nil {
		return err
	}

	initStats, err := roundtripMoov(moov, initBytes, track)
	if err != nil {
		return fmt.Errorf("moov round-trip: %w", err)
	}

	moofStats, err := roundtripFragments(fragments, moov, initStats.decodedMoov, verbose)
	if err != nil {
		return fmt.Errorf("moof round-trip: %w", err)
	}

	printRoundtripReport(sourceLabel, trackSource, track, initStats, moofStats)
	return nil
}

// loadMoovAndFragments parses either a single concatenated fMP4 (inputPath)
// or a separate init+segments pair, and returns the moov, the raw wire-form
// init bytes (the on-disk init.mp4 in separate mode, or a fresh serialisation
// of ftyp+moov in single-input mode), and the list of fragments in order.
func loadMoovAndFragments(inputPath, initPath string, segmentPaths []string) (
	moov *mp4.MoovBox, initBytes []byte, fragments []*mp4.Fragment, sourceLabel string, err error) {

	if inputPath != "" {
		file, ferr := decodeFile(inputPath)
		if ferr != nil {
			return nil, nil, nil, "", ferr
		}
		if !file.IsFragmented() {
			return nil, nil, nil, "", fmt.Errorf("%s is not a fragmented MP4", inputPath)
		}
		if len(file.Moov.Traks) != 1 {
			return nil, nil, nil, "", fmt.Errorf("expected one track in moov, got %d", len(file.Moov.Traks))
		}
		var buf bytes.Buffer
		if file.Ftyp != nil {
			if err := file.Ftyp.Encode(&buf); err != nil {
				return nil, nil, nil, "", fmt.Errorf("re-serialise ftyp: %w", err)
			}
		}
		if err := file.Moov.Encode(&buf); err != nil {
			return nil, nil, nil, "", fmt.Errorf("re-serialise moov: %w", err)
		}
		var frags []*mp4.Fragment
		for _, seg := range file.Segments {
			frags = append(frags, seg.Fragments...)
		}
		return file.Moov, buf.Bytes(), frags, inputPath, nil
	}

	rawInit, readErr := os.ReadFile(initPath)
	if readErr != nil {
		return nil, nil, nil, "", fmt.Errorf("read init %s: %w", initPath, readErr)
	}
	initFile, ferr := mp4.DecodeFile(bytes.NewReader(rawInit))
	if ferr != nil {
		return nil, nil, nil, "", fmt.Errorf("decode %s: %w", initPath, ferr)
	}
	if initFile.Moov == nil {
		return nil, nil, nil, "", fmt.Errorf("%s has no moov", initPath)
	}
	if len(initFile.Moov.Traks) != 1 {
		return nil, nil, nil, "", fmt.Errorf("expected one track in moov, got %d", len(initFile.Moov.Traks))
	}

	var allFrags []*mp4.Fragment
	for _, segPath := range segmentPaths {
		segFile, ferr := decodeFile(segPath)
		if ferr != nil {
			return nil, nil, nil, "", ferr
		}
		if len(segFile.Segments) == 0 {
			return nil, nil, nil, "", fmt.Errorf("%s has no segments", segPath)
		}
		for _, seg := range segFile.Segments {
			allFrags = append(allFrags, seg.Fragments...)
		}
	}
	label := fmt.Sprintf("%s + %d segment(s)", initPath, len(segmentPaths))
	return initFile.Moov, rawInit, allFrags, label, nil
}

func decodeFile(path string) (*mp4.File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	file, err := mp4.DecodeFile(f)
	if err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	return file, nil
}

// resolveTrack returns the Track metadata to feed DecompressInit. If a
// track-info JSON path is given, it is loaded and used as authoritative.
// Otherwise the metadata is synthesised from the moov.
func resolveTrack(moov *mp4.MoovBox, trackInfoPath string) (internal.Track, string, error) {
	if trackInfoPath == "" {
		t, err := synthesizeTrackFromMoov(moov)
		return t, "synthesised from moov", err
	}
	data, err := os.ReadFile(trackInfoPath)
	if err != nil {
		return internal.Track{}, "", fmt.Errorf("read track info %s: %w", trackInfoPath, err)
	}
	var t internal.Track
	if err := json.Unmarshal(data, &t); err != nil {
		return internal.Track{}, "", fmt.Errorf("parse track info %s: %w", trackInfoPath, err)
	}
	if t.Role == "" || t.Timescale == nil {
		return internal.Track{}, "", fmt.Errorf("track info %s missing required fields role/timescale",
			trackInfoPath)
	}
	return t, "from " + trackInfoPath, nil
}

// initRoundtripStats holds size accounting and the decoded moov used to
// decompress the moofs.
type initRoundtripStats struct {
	cmafInitBytes   int
	locmafInitBytes int
	gzipInitBytes   int
	decodedMoov     *mp4.MoovBox
}

func roundtripMoov(moov *mp4.MoovBox, cmafInitBytes []byte, track internal.Track) (initRoundtripStats, error) {
	compressedMoov, err := internal.CompressMoov(moov)
	if err != nil {
		return initRoundtripStats{}, fmt.Errorf("compress moov: %w", err)
	}
	locmafInit := createCompressedObject(uint64(internal.MoovHeader), compressedMoov, nil)

	decodedInit, err := internal.DecompressInit(compressedMoov, track)
	if err != nil {
		return initRoundtripStats{}, fmt.Errorf("decompress init: %w", err)
	}
	if decodedInit == nil || decodedInit.Moov == nil {
		return initRoundtripStats{}, fmt.Errorf("decompressed init has no moov")
	}

	if err := verifyMoovFidelity(moov, decodedInit.Moov); err != nil {
		return initRoundtripStats{}, fmt.Errorf("moov fidelity: %w", err)
	}

	gzipSize, err := gzipSize(cmafInitBytes)
	if err != nil {
		return initRoundtripStats{}, fmt.Errorf("gzip init: %w", err)
	}

	return initRoundtripStats{
		cmafInitBytes:   len(cmafInitBytes),
		locmafInitBytes: len(locmafInit),
		gzipInitBytes:   gzipSize,
		decodedMoov:     decodedInit.Moov,
	}, nil
}

// gzipSize returns the gzip-compressed size of data at default compression
// level. Use this as the "what could plain gzip achieve" baseline.
func gzipSize(data []byte) (int, error) {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	if _, err := w.Write(data); err != nil {
		return 0, err
	}
	if err := w.Close(); err != nil {
		return 0, err
	}
	return buf.Len(), nil
}

// fragmentRoundtripStats accumulates moof-level wire stats.
type fragmentRoundtripStats struct {
	count                int
	cmafMoofBytes        int
	locmafObjectBytes    int
	locmafFullMoofBytes  int
	locmafDeltaMoofBytes int
	fullMoofCount        int
	deltaMoofCount       int
}

func roundtripFragments(fragments []*mp4.Fragment, encoderMoov, decoderMoov *mp4.MoovBox,
	verbose bool) (fragmentRoundtripStats, error) {
	var compressor internal.MoofDeltaCompressor
	var decompressor internal.MoofDeltaDecompressor
	var stats fragmentRoundtripStats

	if verbose {
		fmt.Printf("%-5s %-7s %10s %10s %8s %s\n",
			"frag#", "kind", "cmafMoof", "locmafObj", "ratio", "samples")
	}

	for idx, frag := range fragments {
		if frag.Moof == nil || frag.Mdat == nil {
			return stats, fmt.Errorf("fragment %d missing moof or mdat", idx)
		}
		cmafMoofSize := int(frag.Moof.Size())

		headerID, props, err := compressor.CompressMoof(frag.Moof, encoderMoov)
		if err != nil {
			return stats, fmt.Errorf("fragment %d: compress moof: %w", idx, err)
		}
		locmafObject := createCompressedObject(uint64(headerID), props, frag.Mdat.Data)
		locmafObjectSize := len(locmafObject) - len(frag.Mdat.Data)

		seqnum := frag.Moof.Mfhd.SequenceNumber
		reconMoof, reconMdat, err := decompressor.DecompressMoof(locmafObject, seqnum, decoderMoov)
		if err != nil {
			return stats, fmt.Errorf("fragment %d: decompress moof: %w", idx, err)
		}
		if !bytes.Equal(reconMdat, frag.Mdat.Data) {
			return stats, fmt.Errorf("fragment %d: mdat mismatch (got %d bytes, want %d bytes)",
				idx, len(reconMdat), len(frag.Mdat.Data))
		}
		if err := verifyFragmentFidelity(frag, reconMoof, encoderMoov, decoderMoov); err != nil {
			return stats, fmt.Errorf("fragment %d fidelity: %w", idx, err)
		}

		stats.count++
		stats.cmafMoofBytes += cmafMoofSize
		stats.locmafObjectBytes += locmafObjectSize
		kind := "delta"
		if headerID == internal.MoofHeader {
			kind = "full"
			stats.fullMoofCount++
			stats.locmafFullMoofBytes += locmafObjectSize
		} else {
			stats.deltaMoofCount++
			stats.locmafDeltaMoofBytes += locmafObjectSize
		}
		if verbose {
			ratio := float64(locmafObjectSize) / float64(cmafMoofSize)
			fmt.Printf("%-5d %-7s %10d %10d %7.2fx %d\n",
				idx, kind, cmafMoofSize, locmafObjectSize, ratio,
				len(frag.Moof.Traf.Trun.Samples))
		}
	}
	if stats.count == 0 {
		return stats, fmt.Errorf("no fragments found in input")
	}
	return stats, nil
}

func printRoundtripReport(sourceLabel, trackSource string, track internal.Track,
	init initRoundtripStats, moof fragmentRoundtripStats) {
	fmt.Printf("\nLOCMAF round-trip report for %s\n", sourceLabel)
	fmt.Printf("  track (%s): role=%s codec=%s timescale=%d\n",
		trackSource, track.Role, track.Codec, derefInt(track.Timescale))
	fmt.Printf("\nInit segment:\n")
	fmt.Printf("  cmaf   = %6d B\n", init.cmafInitBytes)
	fmt.Printf("  locmaf = %6d B  (%.1f%% of cmaf)\n",
		init.locmafInitBytes, percent(init.locmafInitBytes, init.cmafInitBytes))
	fmt.Printf("  gzip   = %6d B  (%.1f%% of cmaf)\n",
		init.gzipInitBytes, percent(init.gzipInitBytes, init.cmafInitBytes))

	fmt.Printf("\nMoofs (%d fragments: %d full + %d delta):\n",
		moof.count, moof.fullMoofCount, moof.deltaMoofCount)
	fmt.Printf("  cmaf   total = %7d B  (avg %5.1f B/moof)\n",
		moof.cmafMoofBytes, avg(moof.cmafMoofBytes, moof.count))
	fmt.Printf("  locmaf total = %7d B  (avg %5.1f B/moof, %.1f%% of cmaf)\n",
		moof.locmafObjectBytes, avg(moof.locmafObjectBytes, moof.count),
		percent(moof.locmafObjectBytes, moof.cmafMoofBytes))
	if moof.fullMoofCount > 0 {
		fmt.Printf("  locmaf full  = %7d B  (avg %5.1f B/full moof)\n",
			moof.locmafFullMoofBytes, avg(moof.locmafFullMoofBytes, moof.fullMoofCount))
	}
	if moof.deltaMoofCount > 0 {
		fmt.Printf("  locmaf delta = %7d B  (avg %5.1f B/delta moof)\n",
			moof.locmafDeltaMoofBytes, avg(moof.locmafDeltaMoofBytes, moof.deltaMoofCount))
	}

	totalCmaf := init.cmafInitBytes + moof.cmafMoofBytes
	totalLocmaf := init.locmafInitBytes + moof.locmafObjectBytes
	fmt.Printf("\nCombined header overhead (init + moofs, excluding mdat payload):\n")
	fmt.Printf("  cmaf   = %7d B\n", totalCmaf)
	fmt.Printf("  locmaf = %7d B  (%.1f%% of cmaf, saving %d B)\n",
		totalLocmaf, percent(totalLocmaf, totalCmaf), totalCmaf-totalLocmaf)
	fmt.Printf("\nFidelity: OK — all %d fragments mdat-equal and structurally consistent.\n", moof.count)
}

func synthesizeTrackFromMoov(moov *mp4.MoovBox) (internal.Track, error) {
	if moov == nil || moov.Trak == nil {
		return internal.Track{}, fmt.Errorf("moov or trak missing")
	}
	trak := moov.Trak
	if trak.Mdia == nil || trak.Mdia.Mdhd == nil {
		return internal.Track{}, fmt.Errorf("mdhd missing")
	}
	if trak.Mdia.Minf == nil || trak.Mdia.Minf.Stbl == nil || trak.Mdia.Minf.Stbl.Stsd == nil {
		return internal.Track{}, fmt.Errorf("stsd missing")
	}
	stsd := trak.Mdia.Minf.Stbl.Stsd
	if len(stsd.Children) == 0 {
		return internal.Track{}, fmt.Errorf("stsd has no children")
	}

	timescale := int(trak.Mdia.Mdhd.Timescale)
	t := internal.Track{Timescale: &timescale}

	switch entry := stsd.Children[0].(type) {
	case *mp4.VisualSampleEntryBox:
		t.Role = "video"
		w := int(trak.Tkhd.Width >> 16)
		h := int(trak.Tkhd.Height >> 16)
		t.Width = &w
		t.Height = &h
		t.Codec = innerCodec(entry, entry.Type())
	case *mp4.AudioSampleEntryBox:
		t.Role = "audio"
		sr := int(entry.SampleRate)
		t.SampleRate = &sr
		t.Codec = innerCodec(entry, entry.Type())
	case *mp4.StppBox:
		t.Role = "subtitle"
		t.Codec = entry.Type()
	case *mp4.WvttBox:
		t.Role = "subtitle"
		t.Codec = entry.Type()
	default:
		return internal.Track{}, fmt.Errorf("unsupported sample entry type %s", stsd.Children[0].Type())
	}
	return t, nil
}

// innerCodec returns the original four-CC for a sample entry. For encrypted
// entries (encv/enca) it walks sinf.frma to find the wrapped codec; otherwise
// it returns the sample entry type directly.
func innerCodec(entry mp4.Box, fallback string) string {
	var sinf *mp4.SinfBox
	switch e := entry.(type) {
	case *mp4.VisualSampleEntryBox:
		sinf = e.Sinf
	case *mp4.AudioSampleEntryBox:
		sinf = e.Sinf
	}
	if sinf != nil && sinf.Frma != nil && sinf.Frma.DataFormat != "" {
		return sinf.Frma.DataFormat
	}
	return fallback
}

// verifyMoovFidelity checks that the round-tripped moov has matching
// timescale and sample entry type. LOCMAF intentionally drops fields that
// can be re-derived (e.g. track_id, which the decoder always sets to 1),
// so this is a structural check, not a byte-level one.
func verifyMoovFidelity(orig, recon *mp4.MoovBox) error {
	if orig.Mvhd.Timescale != recon.Mvhd.Timescale {
		return fmt.Errorf("mvhd timescale: orig=%d recon=%d",
			orig.Mvhd.Timescale, recon.Mvhd.Timescale)
	}
	if orig.Trak.Mdia.Mdhd.Timescale != recon.Trak.Mdia.Mdhd.Timescale {
		return fmt.Errorf("mdhd timescale: orig=%d recon=%d",
			orig.Trak.Mdia.Mdhd.Timescale, recon.Trak.Mdia.Mdhd.Timescale)
	}
	origEntry := orig.Trak.Mdia.Minf.Stbl.Stsd.Children[0]
	reconEntry := recon.Trak.Mdia.Minf.Stbl.Stsd.Children[0]
	if origEntry.Type() != reconEntry.Type() {
		return fmt.Errorf("sample entry type: orig=%s recon=%s",
			origEntry.Type(), reconEntry.Type())
	}
	return nil
}

// verifyFragmentFidelity checks that the round-tripped moof yields the same
// effective samples as the original, after trex defaults are applied. The
// original fragment is resolved against the source moov's trex (whose
// track_id matches the source tfhd), while the reconstructed fragment uses
// the decoded moov's trex (track_id always 1 — LOCMAF does not preserve it).
func verifyFragmentFidelity(orig *mp4.Fragment, reconMoof *mp4.MoofBox,
	origMoov, decodedMoov *mp4.MoovBox) error {
	if origMoov.Mvex == nil || origMoov.Mvex.Trex == nil {
		return fmt.Errorf("original moov missing trex")
	}
	if decodedMoov.Mvex == nil || decodedMoov.Mvex.Trex == nil {
		return fmt.Errorf("decoded moov missing trex")
	}
	origTrex := origMoov.Mvex.Trex
	reconTrex := decodedMoov.Mvex.Trex

	origSamples, err := orig.GetFullSamples(origTrex)
	if err != nil {
		return fmt.Errorf("get original samples: %w", err)
	}

	reconFrag := mp4.NewFragment()
	reconFrag.AddChild(reconMoof)
	reconFrag.AddChild(&mp4.MdatBox{Data: orig.Mdat.Data})
	reconSamples, err := reconFrag.GetFullSamples(reconTrex)
	if err != nil {
		return fmt.Errorf("get reconstructed samples: %w", err)
	}

	if len(origSamples) != len(reconSamples) {
		return fmt.Errorf("sample count: orig=%d recon=%d",
			len(origSamples), len(reconSamples))
	}
	origFlags := effectiveSampleFlags(orig.Moof.Traf, origTrex)
	reconFlags := effectiveSampleFlags(reconFrag.Moof.Traf, reconTrex)
	for i := range origSamples {
		o, r := origSamples[i], reconSamples[i]
		if o.Size != r.Size {
			return fmt.Errorf("sample %d size: orig=%d recon=%d", i, o.Size, r.Size)
		}
		if o.Dur != r.Dur {
			return fmt.Errorf("sample %d dur: orig=%d recon=%d", i, o.Dur, r.Dur)
		}
		if origFlags[i] != reconFlags[i] {
			return fmt.Errorf("sample %d flags: orig=%#x recon=%#x",
				i, origFlags[i], reconFlags[i])
		}
		if o.CompositionTimeOffset != r.CompositionTimeOffset {
			return fmt.Errorf("sample %d cto: orig=%d recon=%d",
				i, o.CompositionTimeOffset, r.CompositionTimeOffset)
		}
		if o.DecodeTime != r.DecodeTime {
			return fmt.Errorf("sample %d dts: orig=%d recon=%d",
				i, o.DecodeTime, r.DecodeTime)
		}
	}
	return nil
}

// effectiveSampleFlags returns the per-sample flags after applying the
// ISO 14496-12 §8.8.8.2 override: when first_sample_flags is present in the
// trun, it replaces the per-sample value for sample 0. This matches how a
// spec-compliant player resolves flags during playback, regardless of how
// the trun's HasSampleFlags / HasFirstSampleFlags bits are set in memory.
func effectiveSampleFlags(traf *mp4.TrafBox, trex *mp4.TrexBox) []uint32 {
	trun := traf.Trun
	tfhd := traf.Tfhd
	defaultFlags := uint32(0)
	if tfhd.HasDefaultSampleFlags() {
		defaultFlags = tfhd.DefaultSampleFlags
	} else if trex != nil {
		defaultFlags = trex.DefaultSampleFlags
	}
	out := make([]uint32, len(trun.Samples))
	for i := range trun.Samples {
		if i == 0 {
			if first, present := trun.FirstSampleFlags(); present {
				out[i] = first
				continue
			}
		}
		if trun.HasSampleFlags() {
			out[i] = trun.Samples[i].Flags
		} else {
			out[i] = defaultFlags
		}
	}
	return out
}

func percent(n, d int) float64 {
	if d == 0 {
		return 0
	}
	return 100 * float64(n) / float64(d)
}

func avg(total, count int) float64 {
	if count == 0 {
		return 0
	}
	return float64(total) / float64(count)
}

func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}
