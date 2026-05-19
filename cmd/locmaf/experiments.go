package main

import (
	"bytes"
	"encoding/base64"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/Eyevinn/moqlivemock/internal"
	"github.com/Eyevinn/mp4ff/mp4"
	"github.com/quic-go/quic-go/quicvarint"
)

const (
	defaultInput           = "../../assets/test10s/video_400kbps_avc.mp4"
	locVideoAssetName      = "video_400kbps_avc"
	audioFragmentAssetName = "audio_monotonic_128kbps_aac"

	experimentDurationSeconds = 10
	videoSamplesPerGroup      = 25
	audioSamplesPerGroup      = 46
	measuredGroups            = 10
	mdatBoxHeaderBytes        = 8
	locTimestampPropertyID    = 0x06
	locHeaderTimestampUS      = 1_700_000_000_000_000

	defaultKID = "39112233445566778899aabbccddeeff"
	defaultKey = "39112233445566778899aabbccddeeff"
	defaultIV  = "41112233445566778899aabbccddeeff"
	laURL      = "http://localhost:8081/clearkey"
)

const objectsPerGroupHeader = "Objects per group"
const locmafDeltaMoofsPerGroupHeader = "#LOCMAF Delta Moofs per group"

type experimentResult struct {
	TestName                 string
	CaseName                 string
	AssetName                string
	Protection               string
	SamplesPerFragment       int
	ObjectsPerGroup          int
	Fragments                int
	Samples                  int
	MdatBytes                int
	CMAFOverheadBytes        int
	LOCOverheadBytes         int
	LOCMAFOverheadBytes      int
	DeltaLOCMAFOverheadBytes int
	DeltaFullHeaders         int
	DeltaHeaders             int
	MeasuredSeconds          float64
}

type resultColumn struct {
	Header     string
	TSVHeader  string
	AlignRight bool
	Value      func(experimentResult) string
	RawValue   func(experimentResult) string
}

type typstCell struct {
	value string
	raw   bool
	bold  bool
}

type deltaHeaderFieldUsage struct {
	AssetName        string
	Protection       string
	DeltaFullHeaders int
	DeltaHeaders     int
	FieldID          string
	FieldName        string
	Appearances      int
}

type deltaHeaderFieldUsageGroup struct {
	key     string
	title   string
	results []deltaHeaderFieldUsage
}

func runExperimentsCommand(args []string) error {
	flags := flag.NewFlagSet("experiments", flag.ContinueOnError)
	inputPath := flags.String("input", defaultInput, "MP4 file in assets/test10s to use as the baseline experiment asset")
	assetDir := flags.String("asset-dir", "", "directory containing test assets; defaults to the input file directory")
	drmScheme := flags.String("drm-scheme", "cbcs", "preferred DRM scheme ordering for DRM experiments: cenc or cbcs")
	if err := flags.Parse(args); err != nil {
		return err
	}

	if flags.NArg() > 0 {
		return fmt.Errorf("unexpected positional arguments: %s", strings.Join(flags.Args(), " "))
	}

	if *assetDir == "" {
		*assetDir = filepath.Dir(*inputPath)
	}

	return runExperiments(*inputPath, *assetDir, *drmScheme)
}

func runExperiments(inputPath, assetDir, drmScheme string) error {
	inputName := strings.TrimSuffix(filepath.Base(inputPath), filepath.Ext(inputPath))

	clearAsset, err := internal.LoadAsset(assetDir, 1, 1)
	if err != nil {
		return fmt.Errorf("load clear asset %s: %w", assetDir, err)
	}
	inputTrack := clearAsset.GetTrackByName(inputName)
	if inputTrack == nil {
		return fmt.Errorf("track %q not found in %s", inputName, assetDir)
	}

	var results []experimentResult
	sampleCountResults, err := runSampleCountExperiment("fragment-size", inputTrack)
	if err != nil {
		return err
	}
	results = append(results, sampleCountResults...)

	audioTrack := clearAsset.GetTrackByName(audioFragmentAssetName)
	if audioTrack == nil {
		return fmt.Errorf("audio fragment-size track %q not found in %s", audioFragmentAssetName, assetDir)
	}
	audioSampleCountResults, err := runSampleCountExperiment("fragment-size-audio", audioTrack)
	if err != nil {
		return err
	}
	results = append(results, audioSampleCountResults...)

	drmSchemes := orderedDRMSchemes(drmScheme)

	initHeaderResults, err := runInitHeaderExperiment(assetDir, clearAsset, inputTrack, drmSchemes)
	if err != nil {
		return err
	}
	results = append(results, initHeaderResults...)

	objectGroupResults, err := runObjectGroupSizeExperiment(audioTrack, 1)
	if err != nil {
		return err
	}
	results = append(results, objectGroupResults...)

	assetResults, err := runBitrateExperiment(clearAsset, inputTrack, 1)
	if err != nil {
		return err
	}
	assetResults, err = appendBitrateTrack(assetResults, audioTrack, 1)
	if err != nil {
		return err
	}
	results = append(results, assetResults...)

	locVideoTrack := inputTrack
	if locVideoTrack.Name != locVideoAssetName {
		locVideoTrack = clearAsset.GetTrackByName(locVideoAssetName)
		if locVideoTrack == nil {
			return fmt.Errorf("LOC header track %q not found in %s", locVideoAssetName, assetDir)
		}
	}
	locHeaderResults, err := runLOCHeaderExperiment(locVideoTrack, audioTrack)
	if err != nil {
		return err
	}
	results = append(results, locHeaderResults...)

	drmResults, err := runDRMExperiment(assetDir, inputTrack, drmSchemes, 1)
	if err != nil {
		return err
	}
	results = append(results, drmResults...)

	printResults(results)
	fieldUsageResults, err := runDeltaHeaderFieldUsageExperiment(assetDir, inputTrack, audioTrack,
		samplesPerGroup(inputTrack), samplesPerGroup(audioTrack))
	if err != nil {
		return err
	}
	printDeltaHeaderFieldUsageTable(os.Stdout, fieldUsageResults)
	if err := writeMarkdownResults("table.md", results, fieldUsageResults); err != nil {
		return fmt.Errorf("write markdown results: %w", err)
	}
	if err := writeTypstTableVariables("typst-tables.txt", results, fieldUsageResults, false); err != nil {
		return fmt.Errorf("write typst results: %w", err)
	}
	if err := writeTypstTableVariables("typst-tables-raw.txt", results, fieldUsageResults, true); err != nil {
		return fmt.Errorf("write raw typst results: %w", err)
	}
	return nil
}

func runInitHeaderExperiment(assetDir string, clearAsset *internal.Asset,
	inputTrack *internal.ContentTrack, drmSchemes []string) ([]experimentResult, error) {
	clearResult, err := measureInitHeaders("none", clearAsset, inputTrack.Name, internal.ProtectionNone)
	if err != nil {
		return nil, err
	}

	results := []experimentResult{clearResult}
	for _, drmScheme := range drmSchemes {
		drm, err := internal.ParseCENCflags(drmScheme, defaultKID, defaultKey, defaultIV, laURL)
		if err != nil {
			return nil, fmt.Errorf("configure %s init experiment: %w", drmScheme, err)
		}
		drmAsset, err := internal.LoadAssetWithDRM(assetDir, 1, 1, drm)
		if err != nil {
			return nil, fmt.Errorf("load %s asset %s for init experiment: %w", drmScheme, assetDir, err)
		}
		drmResult, err := measureInitHeaders(drmScheme, drmAsset, inputTrack.Name+"_drm", internal.ProtectionDRM)
		if err != nil {
			return nil, err
		}
		results = append(results, drmResult)
	}
	return results, nil
}

func measureInitHeaders(protectionLabel string, asset *internal.Asset, trackName string,
	protection internal.ProtectionType) (experimentResult, error) {
	cmafCatalog, err := asset.GenCMAFCatalogEntry("cmaf/init-experiment", protection, 0, "cmaf")
	if err != nil {
		return experimentResult{}, fmt.Errorf("generate CMAF catalog: %w", err)
	}
	locmafCatalog, err := asset.GenCMAFCatalogEntry("locmaf/init-experiment", protection, 0, "locmaf")
	if err != nil {
		return experimentResult{}, fmt.Errorf("generate LOCMAF catalog: %w", err)
	}

	cmafTrack := cmafCatalog.GetTrackByName(trackName)
	if cmafTrack == nil {
		return experimentResult{}, fmt.Errorf("CMAF catalog track %q not found", trackName)
	}
	locmafTrack := locmafCatalog.GetTrackByName(trackName)
	if locmafTrack == nil {
		return experimentResult{}, fmt.Errorf("LOCMAF catalog track %q not found", trackName)
	}

	cmafInit, err := base64.StdEncoding.DecodeString(cmafTrack.InitData)
	if err != nil {
		return experimentResult{}, fmt.Errorf("decode CMAF initData for track %s: %w", trackName, err)
	}
	locmafInit, err := base64.StdEncoding.DecodeString(locmafTrack.InitData)
	if err != nil {
		return experimentResult{}, fmt.Errorf("decode LOCMAF initData for track %s: %w", trackName, err)
	}

	return experimentResult{
		TestName:            "init-header",
		AssetName:           trackName,
		Protection:          protectionLabel,
		CMAFOverheadBytes:   len(cmafInit),
		LOCMAFOverheadBytes: len(locmafInit),
	}, nil
}

func runSampleCountExperiment(testName string, track *internal.ContentTrack) ([]experimentResult, error) {
	cases := fragmentSizeCases(track)
	objectsPerGroup := samplesPerGroup(track)

	results := make([]experimentResult, 0, len(cases))
	for _, tc := range cases {
		result, err := measureTrack(testName, tc.name, track, tc.samples, objectsPerGroup)
		if err != nil {
			return nil, fmt.Errorf("measure %s case %q: %w", testName, tc.name, err)
		}
		results = append(results, result)
	}
	return results, nil
}

func fragmentSizeCases(track *internal.ContentTrack) []struct {
	name    string
	samples int
} {
	groupSamples := samplesPerGroup(track)
	if track.ContentType == "video" {
		return []struct {
			name    string
			samples int
		}{
			{name: "1 sample/fragment", samples: 1},
			{name: "5 samples/fragment", samples: 5},
			{name: "1 group/fragment", samples: groupSamples},
		}
	}
	return []struct {
		name    string
		samples int
	}{
		{name: "1 sample/fragment", samples: 1},
		{name: "2 samples/fragment", samples: 2},
		{name: "1 group/fragment", samples: groupSamples},
	}
}

func runObjectGroupSizeExperiment(track *internal.ContentTrack, samplesPerFragment int) ([]experimentResult, error) {
	groupSamples := samplesPerGroup(track)
	cases := []struct {
		name            string
		objectsPerGroup int
	}{
		{name: "1 object/group", objectsPerGroup: 1},
		{name: fmt.Sprintf("%d objects/group", groupSamples), objectsPerGroup: groupSamples},
		{name: fmt.Sprintf("%d objects/group", groupSamples*measuredGroups), objectsPerGroup: groupSamples * measuredGroups},
	}

	results := make([]experimentResult, 0, len(cases))
	for _, tc := range cases {
		result, err := measureTrackWithProtectionSampleLimit("object-group-size", tc.name, track,
			samplesPerFragment, tc.objectsPerGroup, protectionName(track.Protection),
			measuredSamplesForObjectGroupSize(track, tc.objectsPerGroup))
		if err != nil {
			return nil, fmt.Errorf("measure object-group-size case %q: %w", tc.name, err)
		}
		results = append(results, result)
	}
	return results, nil
}

func runBitrateExperiment(asset *internal.Asset, inputTrack *internal.ContentTrack, samplesPerFragment int) ([]experimentResult, error) {
	tracks := tracksMatchingInput(asset, inputTrack, internal.ProtectionNone)
	if len(tracks) == 0 {
		return nil, fmt.Errorf("no clear %s/%s tracks found for bitrate experiment",
			inputTrack.ContentType, inputTrack.SpecData.Codec())
	}
	sort.Slice(tracks, func(i, j int) bool {
		if tracks[i].SampleBitrate != tracks[j].SampleBitrate {
			return tracks[i].SampleBitrate < tracks[j].SampleBitrate
		}
		return tracks[i].Name < tracks[j].Name
	})

	results := make([]experimentResult, 0, len(tracks))
	for i := range tracks {
		result, err := measureTrack("asset-bitrate", bitrateCaseName(&tracks[i]), &tracks[i], samplesPerFragment, samplesPerGroup(&tracks[i]))
		if err != nil {
			return nil, fmt.Errorf("measure bitrate track %s: %w", tracks[i].Name, err)
		}
		results = append(results, result)
	}
	return results, nil
}

func appendBitrateTrack(results []experimentResult, track *internal.ContentTrack,
	samplesPerFragment int) ([]experimentResult, error) {
	for _, result := range results {
		if result.AssetName == track.Name {
			return results, nil
		}
	}

	result, err := measureTrack("asset-bitrate", bitrateCaseName(track), track, samplesPerFragment, samplesPerGroup(track))
	if err != nil {
		return nil, fmt.Errorf("measure bitrate track %s: %w", track.Name, err)
	}
	return append(results, result), nil
}

func runLOCHeaderExperiment(tracks ...*internal.ContentTrack) ([]experimentResult, error) {
	results := make([]experimentResult, 0, len(tracks))
	for _, track := range tracks {
		result, err := measureLOCHeaders(track)
		if err != nil {
			return nil, fmt.Errorf("measure LOC headers for %s: %w", track.Name, err)
		}
		results = append(results, result)
	}
	return results, nil
}

func measureLOCHeaders(track *internal.ContentTrack) (experimentResult, error) {
	if track.TimeScale == 0 || track.SampleDur == 0 {
		return experimentResult{}, fmt.Errorf("invalid track timing timescale=%d sampleDur=%d",
			track.TimeScale, track.SampleDur)
	}

	result := experimentResult{
		TestName:        "loc-header",
		AssetName:       track.Name,
		Protection:      protectionName(track.Protection),
		Samples:         measuredSamples(track),
		MeasuredSeconds: measuredSeconds(track, measuredSamples(track)),
	}

	var videoConfig []byte
	switch sd := track.SpecData.(type) {
	case *internal.AVCData:
		videoConfig = sd.GenLOCVideoConfig()
	case *internal.HEVCData:
		videoConfig = sd.GenLOCVideoConfig()
	}

	for groupNr := uint64(0); ; groupNr++ {
		start, end := internal.CalcLOCGroupRange(track, groupNr, internal.MoqGroupDurMS)
		if start >= uint64(result.Samples) {
			break
		}
		end = min(end, uint64(result.Samples))
		for sampleNr := start; sampleNr < end; sampleNr++ {
			_, origNr := track.CalcSample(sampleNr)
			sample := track.Samples[origNr]

			payloadLen := len(sample.Data)
			if len(videoConfig) > 0 && sample.IsSync() {
				payloadLen += len(videoConfig)
			}

			result.Fragments++
			result.MdatBytes += len(sample.Data)
			result.LOCOverheadBytes += locHeaderSize(sampleNr-start, payloadLen)
		}
	}

	return result, nil
}

func locHeaderSize(objectID uint64, payloadLen int) int {
	timestampHeaderBytes := quicvarint.Len(locTimestampPropertyID) + quicvarint.Len(locHeaderTimestampUS)
	return quicvarint.Len(objectID) +
		quicvarint.Len(uint64(timestampHeaderBytes)) +
		timestampHeaderBytes +
		quicvarint.Len(uint64(payloadLen))
}

func runDRMExperiment(assetDir string, inputTrack *internal.ContentTrack, drmSchemes []string, samplesPerFragment int) ([]experimentResult, error) {
	drm, err := internal.ParseCENCflags(drmSchemes[0], defaultKID, defaultKey, defaultIV, laURL)
	if err != nil {
		return nil, fmt.Errorf("configure DRM experiment: %w", err)
	}
	drmAsset, err := internal.LoadAssetWithDRM(assetDir, 1, 1, drm)
	if err != nil {
		return nil, fmt.Errorf("load DRM asset %s: %w", assetDir, err)
	}

	clearTrack := drmAsset.GetTrackByName(inputTrack.Name)
	protectedTrack := drmAsset.GetTrackByName(inputTrack.Name + "_drm")
	if clearTrack == nil {
		return nil, fmt.Errorf("clear DRM experiment track %q not found", inputTrack.Name)
	}
	if protectedTrack == nil {
		return nil, fmt.Errorf("protected DRM experiment track %q not found", inputTrack.Name+"_drm")
	}

	clearResult, err := measureTrackWithProtection("drm", "none", clearTrack, samplesPerFragment, samplesPerGroup(clearTrack), "none")
	if err != nil {
		return nil, fmt.Errorf("measure clear DRM baseline: %w", err)
	}

	results := []experimentResult{clearResult}
	for _, drmScheme := range drmSchemes {
		drm, err := internal.ParseCENCflags(drmScheme, defaultKID, defaultKey, defaultIV, laURL)
		if err != nil {
			return nil, fmt.Errorf("configure %s DRM experiment: %w", drmScheme, err)
		}
		drmAsset, err := internal.LoadAssetWithDRM(assetDir, 1, 1, drm)
		if err != nil {
			return nil, fmt.Errorf("load %s DRM asset %s: %w", drmScheme, assetDir, err)
		}
		protectedTrack := drmAsset.GetTrackByName(inputTrack.Name + "_drm")
		if protectedTrack == nil {
			return nil, fmt.Errorf("protected %s DRM experiment track %q not found", drmScheme, inputTrack.Name+"_drm")
		}
		protectedResult, err := measureTrackWithProtection("drm", drmScheme, protectedTrack, samplesPerFragment, samplesPerGroup(protectedTrack), drmScheme)
		if err != nil {
			return nil, fmt.Errorf("measure %s protected DRM track: %w", drmScheme, err)
		}
		results = append(results, protectedResult)
	}
	return results, nil
}

func measureTrack(testName, caseName string, track *internal.ContentTrack, samplesPerFragment, objectsPerGroup int) (experimentResult, error) {
	return measureTrackWithProtection(testName, caseName, track, samplesPerFragment, objectsPerGroup,
		protectionName(track.Protection))
}

func measureTrackWithProtection(testName, caseName string, track *internal.ContentTrack,
	samplesPerFragment, objectsPerGroup int, protectionLabel string) (experimentResult, error) {
	return measureTrackWithProtectionSampleLimit(testName, caseName, track, samplesPerFragment, objectsPerGroup,
		protectionLabel, measuredSamples(track))
}

func measureTrackWithProtectionSampleLimit(testName, caseName string, track *internal.ContentTrack,
	samplesPerFragment, objectsPerGroup int, protectionLabel string, sampleLimit int) (experimentResult, error) {
	if samplesPerFragment <= 0 {
		return experimentResult{}, fmt.Errorf("samples per fragment must be positive")
	}
	if sampleLimit <= 0 {
		return experimentResult{}, fmt.Errorf("sample limit must be positive")
	}

	result := experimentResult{
		TestName:           testName,
		CaseName:           caseName,
		AssetName:          track.Name,
		Protection:         protectionLabel,
		SamplesPerFragment: samplesPerFragment,
		ObjectsPerGroup:    objectsPerGroup,
		Samples:            sampleLimit,
		MeasuredSeconds:    measuredSeconds(track, sampleLimit),
	}

	deltaCompressor := &internal.MoofDeltaCompressor{}
	var sequence uint32
	nextGroupStartSample := uint64(0)

	sampleLimitSamples := uint64(result.Samples)
	for start := uint64(0); start < sampleLimitSamples; start += uint64(samplesPerFragment) {
		end := min(start+uint64(samplesPerFragment), sampleLimitSamples)
		if shouldResetDeltaCompressor(start, objectsPerGroup, &nextGroupStartSample) {
			deltaCompressor = &internal.MoofDeltaCompressor{}
		}
		chunk, err := track.GenCMAFChunk(sequence, start, end)
		if err != nil {
			return result, fmt.Errorf("generate CMAF chunk seq=%d samples=%d-%d: %w", sequence, start, end, err)
		}
		moof, mdatBytes, err := decodeChunkHeaderAndMediaSize(chunk)
		if err != nil {
			return result, fmt.Errorf("decode CMAF chunk seq=%d: %w", sequence, err)
		}

		fullCompressor := &internal.MoofDeltaCompressor{}
		fullLocmafChunk, err := track.GenLocmafChunk(sequence, start, end, fullCompressor)
		if err != nil {
			return result, fmt.Errorf("generate full locmaf chunk seq=%d samples=%d-%d: %w", sequence, start, end, err)
		}
		_, fullHeaderBytes := locmafHeaderSize(fullLocmafChunk)

		deltaLocmafChunk, err := track.GenLocmafChunk(sequence, start, end, deltaCompressor)
		if err != nil {
			return result, fmt.Errorf("generate delta locmaf chunk seq=%d samples=%d-%d: %w", sequence, start, end, err)
		}
		deltaHeaderID, deltaHeaderBytes := locmafHeaderSize(deltaLocmafChunk)

		result.Fragments++
		result.MdatBytes += mdatBytes
		result.CMAFOverheadBytes += int(moof.Size()) + mdatBoxHeaderBytes
		result.LOCMAFOverheadBytes += fullHeaderBytes
		result.DeltaLOCMAFOverheadBytes += deltaHeaderBytes
		switch deltaHeaderID {
		case uint64(internal.LocmafFullMoof):
			result.DeltaFullHeaders++
		case uint64(internal.LocmafDeltaMoof):
			result.DeltaHeaders++
		default:
			return result, fmt.Errorf("unexpected delta compressor header id=%d", deltaHeaderID)
		}
		sequence++
	}

	return result, nil
}

func runDeltaHeaderFieldUsageExperiment(assetDir string, videoTrack, audioTrack *internal.ContentTrack,
	videoObjectsPerGroup, audioObjectsPerGroup int) ([]deltaHeaderFieldUsage, error) {
	videoResults, err := measureDeltaHeaderFieldUsage(videoTrack, 1, videoObjectsPerGroup, "none")
	if err != nil {
		return nil, fmt.Errorf("measure video delta header fields: %w", err)
	}
	audioResults, err := measureDeltaHeaderFieldUsage(audioTrack, 1, audioObjectsPerGroup, "none")
	if err != nil {
		return nil, fmt.Errorf("measure audio delta header fields: %w", err)
	}

	results := append(videoResults, audioResults...)
	for _, scheme := range []string{"cenc", "cbcs"} {
		drm, err := internal.ParseCENCflags(scheme, defaultKID, defaultKey, defaultIV, laURL)
		if err != nil {
			return nil, fmt.Errorf("configure %s delta header field usage: %w", scheme, err)
		}
		drmAsset, err := internal.LoadAssetWithDRM(assetDir, 1, 1, drm)
		if err != nil {
			return nil, fmt.Errorf("load %s asset %s for delta header field usage: %w", scheme, assetDir, err)
		}
		protectedVideoTrack := drmAsset.GetTrackByName(videoTrack.Name + "_drm")
		if protectedVideoTrack == nil {
			return nil, fmt.Errorf("protected video delta header field usage track %q not found", videoTrack.Name+"_drm")
		}
		protectedAudioTrack := drmAsset.GetTrackByName(audioTrack.Name + "_drm")
		if protectedAudioTrack == nil {
			return nil, fmt.Errorf("protected audio delta header field usage track %q not found", audioTrack.Name+"_drm")
		}

		protectedVideoResults, err := measureDeltaHeaderFieldUsage(protectedVideoTrack, 1, videoObjectsPerGroup, scheme)
		if err != nil {
			return nil, fmt.Errorf("measure %s video delta header fields: %w", scheme, err)
		}
		protectedAudioResults, err := measureDeltaHeaderFieldUsage(protectedAudioTrack, 1, audioObjectsPerGroup, scheme)
		if err != nil {
			return nil, fmt.Errorf("measure %s audio delta header fields: %w", scheme, err)
		}

		results = append(results, protectedVideoResults...)
		results = append(results, protectedAudioResults...)
	}
	return results, nil
}

func measureDeltaHeaderFieldUsage(track *internal.ContentTrack,
	samplesPerFragment, objectsPerGroup int, protectionLabel string) ([]deltaHeaderFieldUsage, error) {
	if samplesPerFragment <= 0 {
		return nil, fmt.Errorf("samples per fragment must be positive")
	}

	fieldCounts := make(map[uint64]int)
	deltaCompressor := &internal.MoofDeltaCompressor{}
	var sequence uint32
	var fullHeaders int
	var deltaHeaders int
	nextGroupStartSample := uint64(0)

	sampleLimit := uint64(measuredSamples(track))
	for start := uint64(0); start < sampleLimit; start += uint64(samplesPerFragment) {
		end := min(start+uint64(samplesPerFragment), sampleLimit)
		if shouldResetDeltaCompressor(start, objectsPerGroup, &nextGroupStartSample) {
			deltaCompressor = &internal.MoofDeltaCompressor{}
		}
		chunk, err := track.GenLocmafChunk(sequence, start, end, deltaCompressor)
		if err != nil {
			return nil, fmt.Errorf("generate locmaf chunk seq=%d samples=%d-%d: %w", sequence, start, end, err)
		}

		headerID, payload, err := locmafHeaderPayload(chunk)
		if err != nil {
			return nil, fmt.Errorf("parse locmaf chunk seq=%d: %w", sequence, err)
		}
		switch headerID {
		case uint64(internal.LocmafFullMoof):
			fullHeaders++
			sequence++
			continue
		case uint64(internal.LocmafDeltaMoof):
			deltaHeaders++
		default:
			return nil, fmt.Errorf("unexpected locmaf header id=%d seq=%d", headerID, sequence)
		}

		fieldIDs, err := locmafFieldIDs(payload)
		if err != nil {
			return nil, fmt.Errorf("parse delta header fields seq=%d: %w", sequence, err)
		}
		for _, fieldID := range fieldIDs {
			fieldCounts[fieldID]++
		}
		sequence++
	}

	fieldIDs := make([]uint64, 0, len(fieldCounts))
	for fieldID := range fieldCounts {
		fieldIDs = append(fieldIDs, fieldID)
	}
	sort.Slice(fieldIDs, func(i, j int) bool {
		return fieldIDs[i] < fieldIDs[j]
	})

	results := make([]deltaHeaderFieldUsage, 0, len(fieldIDs))
	if len(fieldIDs) == 0 {
		return []deltaHeaderFieldUsage{{
			AssetName:        deltaHeaderFieldUsageAssetName(track.Name),
			Protection:       protectionLabel,
			DeltaFullHeaders: fullHeaders,
			DeltaHeaders:     deltaHeaders,
			FieldID:          "N/A",
			FieldName:        "N/A",
			Appearances:      0,
		}}, nil
	}
	for _, fieldID := range fieldIDs {
		results = append(results, deltaHeaderFieldUsage{
			AssetName:        deltaHeaderFieldUsageAssetName(track.Name),
			Protection:       protectionLabel,
			DeltaFullHeaders: fullHeaders,
			DeltaHeaders:     deltaHeaders,
			FieldID:          fmt.Sprintf("%d", fieldID),
			FieldName:        locmafMoofFieldName(fieldID),
			Appearances:      fieldCounts[fieldID],
		})
	}
	return results, nil
}

func deltaHeaderFieldUsageAssetName(trackName string) string {
	return strings.TrimSuffix(trackName, "_drm")
}

func shouldResetDeltaCompressor(sampleNr uint64, objectsPerGroup int, nextResetSample *uint64) bool {
	if objectsPerGroup == 0 {
		return sampleNr == 0
	}
	resetInterval := uint64(objectsPerGroup)
	if sampleNr < *nextResetSample {
		return false
	}
	for *nextResetSample <= sampleNr {
		*nextResetSample += resetInterval
	}
	return true
}

func decodeChunkHeaderAndMediaSize(chunk []byte) (*mp4.MoofBox, int, error) {
	reader := bytes.NewReader(chunk)
	box, err := mp4.DecodeBox(0, reader)
	if err != nil {
		return nil, 0, err
	}
	moof, ok := box.(*mp4.MoofBox)
	if !ok {
		return nil, 0, fmt.Errorf("expected first box to be moof, got %T", box)
	}

	mdatBox, err := mp4.DecodeBox(moof.Size(), reader)
	if err != nil {
		return nil, 0, err
	}
	mdat, ok := mdatBox.(*mp4.MdatBox)
	if !ok {
		return nil, 0, fmt.Errorf("expected second box to be mdat, got %T", mdatBox)
	}
	return moof, len(mdat.Data), nil
}

func locmafHeaderSize(chunk []byte) (uint64, int) {
	headerID, n, err := quicvarint.Parse(chunk)
	if err != nil {
		return 0, 0
	}
	payloadLength, m, err := quicvarint.Parse(chunk[n:])
	if err != nil {
		return 0, 0
	}
	headerBytes := n + m + int(payloadLength)
	return headerID, headerBytes
}

func locmafHeaderPayload(chunk []byte) (uint64, []byte, error) {
	headerID, n, err := quicvarint.Parse(chunk)
	if err != nil {
		return 0, nil, fmt.Errorf("invalid header id: %w", err)
	}
	payloadLength, m, err := quicvarint.Parse(chunk[n:])
	if err != nil {
		return 0, nil, fmt.Errorf("invalid payload length: %w", err)
	}
	payloadStart := n + m
	payloadEnd := payloadStart + int(payloadLength)
	if payloadEnd > len(chunk) {
		return 0, nil, fmt.Errorf("payload length %d exceeds chunk length %d", payloadLength, len(chunk))
	}
	return headerID, chunk[payloadStart:payloadEnd], nil
}

func locmafFieldIDs(payload []byte) ([]uint64, error) {
	var fieldIDs []uint64
	pos := 0
	for pos < len(payload) {
		fieldID, n, err := quicvarint.Parse(payload[pos:])
		if err != nil {
			return nil, fmt.Errorf("invalid field id at offset %d: %w", pos, err)
		}
		pos += n
		fieldIDs = append(fieldIDs, fieldID)

		if fieldID%2 == 0 {
			_, n, err = quicvarint.Parse(payload[pos:])
			if err != nil {
				return nil, fmt.Errorf("invalid field value for id=%d: %w", fieldID, err)
			}
			pos += n
			continue
		}

		valueLength, n, err := quicvarint.Parse(payload[pos:])
		if err != nil {
			return nil, fmt.Errorf("invalid field length for id=%d: %w", fieldID, err)
		}
		pos += n
		if pos+int(valueLength) > len(payload) {
			return nil, fmt.Errorf("field id=%d length %d exceeds payload length %d", fieldID, valueLength, len(payload))
		}
		pos += int(valueLength)
	}
	return fieldIDs, nil
}

func locmafMoofFieldName(fieldID uint64) string {
	switch fieldID {
	case 1:
		return "sampleSizes"
	case 2:
		return "sampleDescriptionIndex"
	case 3:
		return "sampleDurations"
	case 4:
		return "defaultSampleDuration"
	case 5:
		return "sampleCompositionTimeOffsets"
	case 6:
		return "defaultSampleSize"
	case 7:
		return "sampleFlags"
	case 8:
		return "defaultSampleFlags"
	case 9:
		return "initializationVector"
	case 10:
		return "baseMediaDecodeTime"
	case 11:
		return "subsampleCount"
	case 12:
		return "firstSampleFlags"
	case 13:
		return "bytesOfClearData"
	case 14:
		return "perSampleIVSize"
	case 15:
		return "bytesOfProtectedData"
	case 16:
		return "sampleCount"
	case 17:
		return "deletedFields"
	default:
		return fmt.Sprintf("Unknown field %d", fieldID)
	}
}

func samplesForSeconds(track *internal.ContentTrack, seconds int) int {
	if track.SampleDur == 0 {
		return 1
	}
	samples := int(math.Floor(float64(seconds) * float64(track.TimeScale) / float64(track.SampleDur)))
	return max(1, samples)
}

func samplesPerGroup(track *internal.ContentTrack) int {
	switch track.ContentType {
	case "video":
		return videoSamplesPerGroup
	case "audio":
		return audioSamplesPerGroup
	default:
		return samplesForSeconds(track, 1)
	}
}

func measuredSamples(track *internal.ContentTrack) int {
	return min(int(track.NrSamples), samplesPerGroup(track)*measuredGroups)
}

func measuredSamplesForObjectGroupSize(track *internal.ContentTrack, objectsPerGroup int) int {
	targetSamples := measuredSamples(track)
	if objectsPerGroup <= 0 {
		return targetSamples
	}
	groups := max(1, int(math.Round(float64(targetSamples)/float64(objectsPerGroup))))
	return min(int(track.NrSamples), objectsPerGroup*groups)
}

func measuredSeconds(track *internal.ContentTrack, samples int) float64 {
	if track.TimeScale == 0 || track.SampleDur == 0 {
		return float64(experimentDurationSeconds)
	}
	return float64(samples) * float64(track.SampleDur) / float64(track.TimeScale)
}

func tracksMatchingInput(asset *internal.Asset, inputTrack *internal.ContentTrack, protection internal.ProtectionType) []internal.ContentTrack {
	var tracks []internal.ContentTrack
	for _, group := range asset.Groups {
		for _, track := range group.Tracks {
			if track.ContentType != inputTrack.ContentType {
				continue
			}
			if track.SpecData.Codec() != inputTrack.SpecData.Codec() {
				continue
			}
			if track.Protection != protection {
				continue
			}
			tracks = append(tracks, track)
		}
	}
	return tracks
}

func bitrateCaseName(track *internal.ContentTrack) string {
	return fmt.Sprintf("%s %.0fkbps", track.Name, float64(track.SampleBitrate)/1000)
}

func protectionName(protection internal.ProtectionType) string {
	switch protection {
	case internal.ProtectionNone:
		return "none"
	case internal.ProtectionDRM:
		return "drm"
	case internal.ProtectionECCP:
		return "eccp"
	default:
		return fmt.Sprintf("unknown(%d)", protection)
	}
}

func orderedDRMSchemes(preferred string) []string {
	var schemes []string
	addScheme := func(scheme string) {
		scheme = strings.ToLower(strings.TrimSpace(scheme))
		if scheme == "" {
			return
		}
		for _, existing := range schemes {
			if existing == scheme {
				return
			}
		}
		schemes = append(schemes, scheme)
	}

	addScheme(preferred)
	addScheme("cbcs")
	addScheme("cenc")
	return schemes
}

func printResults(results []experimentResult) {
	printTSVResults(os.Stdout, results)
}

func groupDeltaHeaderFieldUsageByProtection(results []deltaHeaderFieldUsage) []deltaHeaderFieldUsageGroup {
	groupTitles := map[string]string{
		"none": "No Protection",
		"cenc": "CENC",
		"cbcs": "CBCS",
	}
	groupOrder := []string{"none", "cenc", "cbcs"}
	grouped := make(map[string][]deltaHeaderFieldUsage)
	for _, result := range results {
		grouped[result.Protection] = append(grouped[result.Protection], result)
	}

	groups := make([]deltaHeaderFieldUsageGroup, 0, len(grouped))
	for _, key := range groupOrder {
		if len(grouped[key]) == 0 {
			continue
		}
		groups = append(groups, deltaHeaderFieldUsageGroup{
			key:     key,
			title:   groupTitles[key],
			results: grouped[key],
		})
		delete(grouped, key)
	}

	remainingKeys := make([]string, 0, len(grouped))
	for key := range grouped {
		remainingKeys = append(remainingKeys, key)
	}
	sort.Strings(remainingKeys)
	for _, key := range remainingKeys {
		groups = append(groups, deltaHeaderFieldUsageGroup{
			key:     strings.NewReplacer("-", "", "_", "").Replace(key),
			title:   key,
			results: grouped[key],
		})
	}
	return groups
}

func printDeltaHeaderFieldUsageTable(w io.Writer, results []deltaHeaderFieldUsage) {
	for _, group := range groupDeltaHeaderFieldUsageByProtection(results) {
		fmt.Fprintln(w)
		fmt.Fprintf(w, "## delta-header-fields-%s\n", group.key)
		fmt.Fprintln(w, "asset\tlocmaf_delta_moofs_per_group\tfield_id\tfield\tappearances")
		for _, result := range group.results {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\n",
				result.AssetName, formatAverage(deltaHeaderMoofsPerGroup(result)),
				result.FieldID, result.FieldName, result.Appearances)
		}
	}
}

func printTSVResults(w io.Writer, results []experimentResult) {
	fmt.Fprintln(w, "# LOCMAF overhead experiments")
	fmt.Fprintf(w, "# Non-init overhead byte totals are measured over %s.\n", measurementWindowDescription())
	fmt.Fprintln(w, "# Bitrate columns divide those totals by each row's measured media duration.")
	fmt.Fprintln(w, "# locmaf_overhead_kbps includes the LOCMAF header-id and payload-length varints.")
	fmt.Fprintln(w, "# locmaf_overhead_kbps uses the delta-compressor stream, including full LOCMAF moof properties emitted after resets.")
	fmt.Fprintln(w, "# loc_header_kbps counts MoQ subgroup object framing plus the LOC Timestamp extension header.")
	fmt.Fprintln(w)

	currentTest := ""
	for _, result := range results {
		if result.TestName != currentTest {
			if currentTest != "" {
				fmt.Fprintln(w)
			}
			currentTest = result.TestName
			fmt.Fprintf(w, "## %s\n", markdownTitle(currentTest))
			printTSVHeader(w, columnsForExperiment(currentTest))
		}
		printTSVRow(w, columnsForExperiment(currentTest), result)
	}
}

func printTSVHeader(w io.Writer, columns []resultColumn) {
	headers := make([]string, 0, len(columns))
	for _, column := range columns {
		headers = append(headers, column.TSVHeader)
	}
	fmt.Fprintln(w, strings.Join(headers, "\t"))
}

func printTSVRow(w io.Writer, columns []resultColumn, result experimentResult) {
	values := make([]string, 0, len(columns))
	for _, column := range columns {
		values = append(values, column.Value(result))
	}
	fmt.Fprintln(w, strings.Join(values, "\t"))
}

func writeMarkdownResults(path string, results []experimentResult, fieldUsageResults []deltaHeaderFieldUsage) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	fmt.Fprintln(f, "# LOCMAF Overhead Experiments")
	fmt.Fprintln(f)
	fmt.Fprintf(f, "Non-init overhead byte totals are measured over %s.\n", measurementWindowDescription())
	fmt.Fprintln(f)
	fmt.Fprintln(f, "Bitrate columns divide those totals by each row's measured media duration.")
	fmt.Fprintln(f)
	fmt.Fprintln(f, "`locmaf_overhead_kbps` includes the LOCMAF header-id and payload-length varints.")
	fmt.Fprintln(f)
	fmt.Fprintln(f, "`locmaf_overhead_kbps` uses the complete delta-compressor stream. It includes full LOCMAF moof properties emitted immediately after resets.")
	fmt.Fprintln(f)
	fmt.Fprintln(f, "`loc_header_kbps` counts MoQ subgroup object framing plus the LOC Timestamp extension header.")
	fmt.Fprintln(f)

	currentTest := ""
	for _, result := range results {
		if result.TestName != currentTest {
			if currentTest != "" {
				fmt.Fprintln(f)
			}
			currentTest = result.TestName
			fmt.Fprintf(f, "## %s\n", markdownTitle(currentTest))
			fmt.Fprintln(f)
			printMarkdownHeader(f, columnsForExperiment(currentTest))
		}
		printMarkdownRow(f, columnsForExperiment(currentTest), result)
	}
	if len(fieldUsageResults) > 0 {
		fmt.Fprintln(f)
		printMarkdownDeltaHeaderFieldUsageTable(f, fieldUsageResults)
	}
	return f.Close()
}

func printMarkdownHeader(w io.Writer, columns []resultColumn) {
	headers := make([]string, 0, len(columns))
	separators := make([]string, 0, len(columns))
	for _, column := range columns {
		headers = append(headers, column.Header)
		if column.AlignRight {
			separators = append(separators, "---:")
		} else {
			separators = append(separators, "---")
		}
	}
	fmt.Fprintf(w, "| %s |\n", strings.Join(headers, " | "))
	fmt.Fprintf(w, "|%s|\n", strings.Join(separators, "|"))
}

func printMarkdownRow(w io.Writer, columns []resultColumn, result experimentResult) {
	values := make([]string, 0, len(columns))
	for _, column := range columns {
		values = append(values, column.Value(result))
	}
	fmt.Fprintf(w, "| %s |\n", strings.Join(values, " | "))
}

func printMarkdownDeltaHeaderFieldUsageTable(w io.Writer, results []deltaHeaderFieldUsage) {
	for i, group := range groupDeltaHeaderFieldUsageByProtection(results) {
		if i > 0 {
			fmt.Fprintln(w)
		}
		fmt.Fprintf(w, "## Delta Header Fields - %s\n", group.title)
		fmt.Fprintln(w)
		fmt.Fprintln(w, "| Asset | #LOCMAF Delta Moofs<br>per group | Field ID | Field | #appearances |")
		fmt.Fprintln(w, "|---|---:|---:|---|---:|")
		for _, result := range group.results {
			fmt.Fprintf(w, "| %s | %s | %s | %s | %d |\n",
				result.AssetName, formatAverage(deltaHeaderMoofsPerGroup(result)),
				result.FieldID, result.FieldName, result.Appearances)
		}
	}
}

func writeTypstResults(path string, results []experimentResult) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	fmt.Fprintln(f, "// Generated LOCMAF experiment tables.")
	currentTest := ""
	firstTable := true
	for _, result := range results {
		if result.TestName != currentTest {
			if !firstTable {
				fmt.Fprintln(f, ")")
				fmt.Fprintln(f)
			}
			firstTable = false
			currentTest = result.TestName
			fmt.Fprintf(f, "= %s\n\n", typstText(markdownTitle(currentTest)))
			printTypstTableStart(f, columnsForExperiment(currentTest))
		}
		printTypstRow(f, columnsForExperiment(currentTest), result)
	}
	if !firstTable {
		fmt.Fprintln(f, ")")
	}
	return f.Close()
}

func writeTypstTableVariables(path string, results []experimentResult, fieldUsageResults []deltaHeaderFieldUsage, rawValues bool) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	for i, group := range groupResultsByExperiment(results) {
		if i > 0 {
			fmt.Fprintln(f)
		}
		fmt.Fprintf(f, "#let %s = ", typstVariableName(group.testName))
		printTypstResultVariableTable(f, columnsForExperiment(group.testName), group.results, rawValues)
	}
	for _, group := range groupDeltaHeaderFieldUsageByProtection(fieldUsageResults) {
		if len(results) > 0 {
			fmt.Fprintln(f)
		}
		fmt.Fprintf(f, "#let deltafields%s = ", group.key)
		printDeltaHeaderFieldUsageTypstTable(f, group.results)
	}
	return f.Close()
}

type experimentResultGroup struct {
	testName string
	results  []experimentResult
}

func groupResultsByExperiment(results []experimentResult) []experimentResultGroup {
	var groups []experimentResultGroup
	for _, result := range results {
		if len(groups) == 0 || groups[len(groups)-1].testName != result.TestName {
			groups = append(groups, experimentResultGroup{testName: result.TestName})
		}
		groups[len(groups)-1].results = append(groups[len(groups)-1].results, result)
	}
	return groups
}

func printTypstResultVariableTable(w io.Writer, columns []resultColumn, results []experimentResult, rawValues bool) {
	fmt.Fprintf(w, "table(\n  columns: (%s),\n  align: right,\n  stroke: none,\n", typstAutoColumns(len(columns)))
	printTypstResultHeader(w, columns)
	fmt.Fprintln(w, "  table.hline(),")
	for _, result := range results {
		printTypstResultRow(w, columns, result, rawValues)
	}
	fmt.Fprintln(w, ")")
}

func printDeltaHeaderFieldUsageTypstTable(w io.Writer, results []deltaHeaderFieldUsage) {
	fmt.Fprintf(w, "table(\n  columns: (%s),\n  align: right,\n  stroke: none,\n", typstAutoColumns(4))
	printTypstCellValues(w, []typstCell{
		headerTypstCell("Asset"),
		headerTypstCell(locmafDeltaMoofsPerGroupHeader),
		headerTypstCell("Field"),
		headerTypstCell("#appearances"),
	}, true)
	fmt.Fprintln(w, "  table.hline(),")
	for _, result := range results {
		printTypstCellValues(w, []typstCell{
			assetNameTypstCell(result.AssetName),
			textTypstCell(formatAverage(deltaHeaderMoofsPerGroup(result))),
			textTypstCell(result.FieldName),
			textTypstCell(fmt.Sprintf("%d", result.Appearances)),
		}, true)
	}
	fmt.Fprintln(w, ")")
}

func printTypstResultHeader(w io.Writer, columns []resultColumn) {
	values := make([]typstCell, 0, len(columns))
	for _, column := range columns {
		values = append(values, headerTypstCell(column.Header))
	}
	printTypstCellValues(w, values, true)
}

func printTypstResultRow(w io.Writer, columns []resultColumn, result experimentResult, rawValues bool) {
	values := make([]typstCell, 0, len(columns))
	for _, column := range columns {
		value := column.Value(result)
		if rawValues && column.RawValue != nil {
			value = column.RawValue(result)
		}
		if column.TSVHeader == "asset" {
			values = append(values, assetNameTypstCell(value))
			continue
		}
		values = append(values, textTypstCell(value))
	}
	printTypstCellValues(w, values, true)
}

func printTypstTableStart(w io.Writer, columns []resultColumn) {
	fmt.Fprintf(w, "table(\n  columns: (%s),\n  align: right,\n  stroke: none,\n", typstAutoColumns(len(columns)))
	printTypstHeaderCells(w, typstHeaders(columns), true)
	fmt.Fprintln(w, "  table.hline(),")
}

func printTypstRow(w io.Writer, columns []resultColumn, result experimentResult) {
	values := make([]typstCell, 0, len(columns))
	for _, column := range columns {
		value := column.Value(result)
		if column.TSVHeader == "asset" {
			values = append(values, assetNameTypstCell(value))
			continue
		}
		values = append(values, textTypstCell(value))
	}
	printTypstCellValues(w, values, true)
}

func printTypstCells(w io.Writer, values []string, trailingComma bool) {
	cells := make([]typstCell, 0, len(values))
	for _, value := range values {
		cells = append(cells, textTypstCell(value))
	}
	printTypstCellValues(w, cells, trailingComma)
}

func printTypstHeaderCells(w io.Writer, values []string, trailingComma bool) {
	cells := make([]typstCell, 0, len(values))
	for _, value := range values {
		cells = append(cells, headerTypstCell(value))
	}
	printTypstCellValues(w, cells, trailingComma)
}

func printTypstCellValues(w io.Writer, values []typstCell, trailingComma bool) {
	cells := make([]string, 0, len(values))
	for _, value := range values {
		cellText := value.value
		if !value.raw {
			cellText = typstText(value.value)
		}
		if value.bold {
			cellText = fmt.Sprintf("#strong[%s]", cellText)
		}
		cells = append(cells, fmt.Sprintf("[%s]", cellText))
	}
	suffix := ""
	if trailingComma {
		suffix = ","
	}
	fmt.Fprintf(w, "  %s%s\n", strings.Join(cells, ", "), suffix)
}

func typstHeaders(columns []resultColumn) []string {
	headers := make([]string, 0, len(columns))
	for _, column := range columns {
		headers = append(headers, column.Header)
	}
	return headers
}

func typstAutoColumns(count int) string {
	columns := make([]string, count)
	for i := range columns {
		columns[i] = "auto"
	}
	return strings.Join(columns, ", ")
}

func typstText(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `#`, `\#`)
	value = strings.ReplaceAll(value, `[`, `\[`)
	value = strings.ReplaceAll(value, `]`, `\]`)
	return value
}

func textTypstCell(value string) typstCell {
	value = typstAbbreviation(value)
	if value == objectsPerGroupHeader {
		return typstCell{
			value: typstText("Objects") + ` #linebreak() ` + typstText("per group"),
			raw:   true,
		}
	}
	if value == locmafDeltaMoofsPerGroupHeader {
		return typstCell{
			value: typstText("#LOCMAF Delta Moofs") + ` #linebreak() ` + typstText("per group"),
			raw:   true,
		}
	}
	return typstCell{value: value}
}

func headerTypstCell(value string) typstCell {
	cell := textTypstCell(value)
	cell.bold = true
	return cell
}

func assetNameTypstCell(value string) typstCell {
	value = typstAbbreviation(value)
	return typstCell{
		value: strings.ReplaceAll(typstText(value), "_", `\_ \ `),
		raw:   true,
	}
}

func typstAbbreviation(value string) string {
	switch value {
	case "video_400kbps_avc", "video_400kpbs_avc":
		return "video400avc"
	case "video_600kbps_avc", "video_600kpbs_avc":
		return "video600avc"
	case "video_900kbps_avc", "video_900kpbs_avc":
		return "video900avc"
	case "audio_monotonic_128kbps_aac":
		return "audio128aac"
	case "#LOCMAF Delta Moof / group", "#LOCMAF Delta Moofs per group":
		return "#LDM / group"
	case "#appearances":
		return "Count"
	case "Compression ratio":
		return "CR"
	case "Objects":
		return "Obj."
	case "objects":
		return "obj."
	case "Objects per group":
		return "Obj. / group"
	case "#samples / fragment", "#samples per fragment":
		return "#smpl. / frag"
	case "CMAF overhead/mdat size (%)":
		return "CMAF / mdat"
	case "LOCCMAF overhead/mdat size (%)", "LOCMAF overhead/mdat size (%)":
		return "LOCMAF / mdat"
	case "LOC overhead / mdat size (%)", "LOC header/mdat (%)":
		return "LOC / mdat"
	default:
		value = strings.ReplaceAll(value, "Objects", "Obj.")
		value = strings.ReplaceAll(value, "objects", "obj.")
		value = strings.ReplaceAll(value, "Overhead", "OH")
		return strings.ReplaceAll(value, "overhead", "OH")
	}
}

func typstVariableName(testName string) string {
	switch testName {
	case "fragment-size":
		return "vidfrag"
	case "fragment-size-audio":
		return "audiofrag"
	case "init-header":
		return "init"
	case "object-group-size":
		return "objectgroupsize"
	case "asset-bitrate":
		return "bitrate"
	case "loc-header":
		return "locheader"
	case "drm":
		return "drm"
	default:
		return strings.NewReplacer("-", "", "_", "").Replace(testName)
	}
}

func columnsForExperiment(testName string) []resultColumn {
	switch testName {
	case "fragment-size":
		return fragmentSizeColumns()
	case "fragment-size-audio":
		return fragmentSizeColumns()
	case "init-header":
		return initHeaderColumns()
	case "object-group-size":
		return objectGroupSizeColumns()
	case "asset-bitrate":
		return assetBitrateColumns()
	case "loc-header":
		return locHeaderColumns()
	case "drm":
		return drmColumns()
	default:
		return defaultColumns()
	}
}

func initHeaderColumns() []resultColumn {
	return []resultColumn{
		stringColumn("Protection", "protection", func(r experimentResult) string { return r.Protection }),
		intColumn("CMAF Init bytes", "normal_init_bytes", func(r experimentResult) int { return r.CMAFOverheadBytes }),
		intColumn("LOCMAF Init bytes", "compressed_init_bytes", func(r experimentResult) int { return r.LOCMAFOverheadBytes }),
		ratioColumn("Compression ratio", "compression_ratio", func(r experimentResult) float64 {
			return compressionRatio(r.CMAFOverheadBytes, r.LOCMAFOverheadBytes)
		}),
	}
}

func fragmentSizeColumns() []resultColumn {
	return []resultColumn{
		intColumn("#samples per fragment", "samples_per_fragment", func(r experimentResult) int { return r.SamplesPerFragment }),
		averageColumn(locmafDeltaMoofsPerGroupHeader, "locmaf_delta_moofs_per_group", deltaMoofsPerGroup),
		bitrateColumn("CMAF overhead", "cmaf_overhead_kbps", func(r experimentResult) int { return r.CMAFOverheadBytes }),
		bitrateColumn("LOCMAF overhead", "locmaf_overhead_kbps", func(r experimentResult) int { return r.DeltaLOCMAFOverheadBytes }),
		ratioColumn("Compression ratio", "compression_ratio", func(r experimentResult) float64 {
			return compressionRatio(r.CMAFOverheadBytes, r.DeltaLOCMAFOverheadBytes)
		}),
	}
}

func objectGroupSizeColumns() []resultColumn {
	return []resultColumn{
		intColumn(objectsPerGroupHeader, "objects_per_group", func(r experimentResult) int { return r.ObjectsPerGroup }),
		averageColumn(locmafDeltaMoofsPerGroupHeader, "locmaf_delta_moofs_per_group", deltaMoofsPerGroup),
		bitrateColumn("LOCMAF overhead", "locmaf_overhead_kbps", func(r experimentResult) int { return r.DeltaLOCMAFOverheadBytes }),
	}
}

func assetBitrateColumns() []resultColumn {
	return []resultColumn{
		stringColumn("Asset", "asset", func(r experimentResult) string { return r.AssetName }),
		bitrateColumn("Mdat (kbps)", "mdat_kbps", func(r experimentResult) int { return r.MdatBytes }),
		percentageColumn("CMAF overhead/mdat size (%)", "cmaf_overhead_mdat_percent", func(r experimentResult) float64 {
			return bitrateByteRatio(r.CMAFOverheadBytes, r.MdatBytes, r)
		}),
		percentageColumn("LOCMAF overhead/mdat size (%)", "locmaf_overhead_mdat_percent", func(r experimentResult) float64 {
			return bitrateByteRatio(r.DeltaLOCMAFOverheadBytes, r.MdatBytes, r)
		}),
	}
}

func locHeaderColumns() []resultColumn {
	return []resultColumn{
		stringColumn("Asset", "asset", func(r experimentResult) string { return r.AssetName }),
		intColumn("#LOC objects", "loc_objects", func(r experimentResult) int { return r.Fragments }),
		bitrateColumn("Mdat (kbps)", "mdat_kbps", func(r experimentResult) int { return r.MdatBytes }),
		bitrateColumn("LOC overhead", "loc_header_kbps", func(r experimentResult) int { return r.LOCOverheadBytes }),
		percentageColumn("LOC / mdat", "loc_header_mdat_percent", func(r experimentResult) float64 {
			return ratio(r.LOCOverheadBytes, r.MdatBytes)
		}),
	}
}

func drmColumns() []resultColumn {
	return []resultColumn{
		stringColumn("Protection", "protection", func(r experimentResult) string { return r.Protection }),
		bitrateColumn("CMAF overhead", "cmaf_overhead_kbps", func(r experimentResult) int { return r.CMAFOverheadBytes }),
		bitrateColumn("LOCMAF overhead", "locmaf_overhead_kbps", func(r experimentResult) int { return r.DeltaLOCMAFOverheadBytes }),
		ratioColumn("Compression ratio", "compression_ratio", func(r experimentResult) float64 {
			return compressionRatio(r.CMAFOverheadBytes, r.DeltaLOCMAFOverheadBytes)
		}),
	}
}

func defaultColumns() []resultColumn {
	return []resultColumn{
		stringColumn("Case", "case", func(r experimentResult) string { return r.CaseName }),
		stringColumn("Asset", "asset", func(r experimentResult) string { return r.AssetName }),
		stringColumn("Protection", "protection", func(r experimentResult) string { return r.Protection }),
		intColumn("#samples per fragment", "samples_per_fragment", func(r experimentResult) int { return r.SamplesPerFragment }),
		intColumn(objectsPerGroupHeader, "objects_per_group", func(r experimentResult) int { return r.ObjectsPerGroup }),
		intColumn("#fragments", "fragments", func(r experimentResult) int { return r.Fragments }),
		intColumn("#samples", "samples", func(r experimentResult) int { return r.Samples }),
		intColumn("Mdat bytes", "mdat_bytes", func(r experimentResult) int { return r.MdatBytes }),
		bitrateColumn("LOC header (kbps)", "loc_header_kbps", func(r experimentResult) int { return r.LOCOverheadBytes }),
		intColumn("#LOCMAF Full Moofs", "locmaf_full_moofs", func(r experimentResult) int { return r.DeltaFullHeaders }),
		averageColumn(locmafDeltaMoofsPerGroupHeader, "locmaf_delta_moofs_per_group", deltaMoofsPerGroup),
		bitrateColumn("CMAF overhead", "cmaf_overhead_kbps", func(r experimentResult) int { return r.CMAFOverheadBytes }),
		bitrateColumn("LOCMAF overhead", "locmaf_overhead_kbps", func(r experimentResult) int { return r.DeltaLOCMAFOverheadBytes }),
		ratioColumn("CMAF overhead/mdat ratio", "cmaf_overhead_mdat_ratio", func(r experimentResult) float64 {
			return ratio(r.CMAFOverheadBytes, r.MdatBytes)
		}),
		ratioColumn("LOCMAF overhead/mdat ratio", "locmaf_overhead_mdat_ratio", func(r experimentResult) float64 {
			return ratio(r.DeltaLOCMAFOverheadBytes, r.MdatBytes)
		}),
		ratioColumn("Compression ratio", "compression_ratio", func(r experimentResult) float64 {
			return compressionRatio(r.CMAFOverheadBytes, r.DeltaLOCMAFOverheadBytes)
		}),
	}
}

func stringColumn(header, tsvHeader string, value func(experimentResult) string) resultColumn {
	return resultColumn{Header: header, TSVHeader: tsvHeader, Value: value, RawValue: value}
}

func intColumn(header, tsvHeader string, value func(experimentResult) int) resultColumn {
	format := func(result experimentResult) string {
		return fmt.Sprintf("%d", value(result))
	}
	return resultColumn{
		Header:     header,
		TSVHeader:  tsvHeader,
		AlignRight: true,
		Value:      format,
		RawValue:   format,
	}
}

func averageColumn(header, tsvHeader string, value func(experimentResult) float64) resultColumn {
	return resultColumn{
		Header:     header,
		TSVHeader:  tsvHeader,
		AlignRight: true,
		Value: func(result experimentResult) string {
			return formatAverage(value(result))
		},
		RawValue: func(result experimentResult) string {
			return rawFloat(value(result))
		},
	}
}

func bitrateColumn(header, tsvHeader string, value func(experimentResult) int) resultColumn {
	return resultColumn{
		Header:     header,
		TSVHeader:  tsvHeader,
		AlignRight: true,
		Value: func(result experimentResult) string {
			return fmt.Sprintf("%.2f", bitrateKbps(value(result), result))
		},
		RawValue: func(result experimentResult) string {
			return rawBitrateKbps(value(result), result)
		},
	}
}

func ratioColumn(header, tsvHeader string, value func(experimentResult) float64) resultColumn {
	return resultColumn{
		Header:     header,
		TSVHeader:  tsvHeader,
		AlignRight: true,
		Value: func(result experimentResult) string {
			return fmt.Sprintf("%.2f", value(result))
		},
		RawValue: func(result experimentResult) string {
			return rawFloat(value(result))
		},
	}
}

func percentageColumn(header, tsvHeader string, value func(experimentResult) float64) resultColumn {
	return resultColumn{
		Header:     header,
		TSVHeader:  tsvHeader,
		AlignRight: true,
		Value: func(result experimentResult) string {
			return fmt.Sprintf("%.2f%%", value(result)*100)
		},
		RawValue: func(result experimentResult) string {
			return rawFloat(value(result)*100) + "%"
		},
	}
}

func rawFloat(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func formatAverage(value float64) string {
	if math.Abs(value-math.Round(value)) < 1e-9 {
		return fmt.Sprintf("%.0f", value)
	}
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", value), "0"), ".")
}

func deltaMoofsPerGroup(result experimentResult) float64 {
	return averagePerGroup(result.DeltaHeaders, result.DeltaFullHeaders)
}

func deltaHeaderMoofsPerGroup(result deltaHeaderFieldUsage) float64 {
	return averagePerGroup(result.DeltaHeaders, result.DeltaFullHeaders)
}

func averagePerGroup(count, groups int) float64 {
	if groups == 0 {
		return 0
	}
	return float64(count) / float64(groups)
}

func measurementWindowDescription() string {
	return fmt.Sprintf("%d measured group(s): %d video samples or %d audio samples",
		measuredGroups, videoSamplesPerGroup*measuredGroups, audioSamplesPerGroup*measuredGroups)
}

func rawBitrateKbps(bytes int, result experimentResult) string {
	return rawFloat(bitrateKbps(bytes, result))
}

func exactDecimal(numerator, denominator int) string {
	whole := numerator / denominator
	remainder := numerator % denominator
	if remainder == 0 {
		return fmt.Sprintf("%d", whole)
	}

	digits := make([]byte, 0, 8)
	for remainder != 0 {
		remainder *= 10
		digits = append(digits, byte('0'+remainder/denominator))
		remainder %= denominator
	}
	return fmt.Sprintf("%d.%s", whole, string(digits))
}

func markdownTitle(testName string) string {
	switch testName {
	case "init-header":
		return "Init Header"
	case "fragment-size":
		return "Fragment Size"
	case "fragment-size-audio":
		return "Audio Fragment Size"
	case "asset-bitrate":
		return "Asset Bitrate"
	case "loc-header":
		return "LOC Header"
	case "object-group-size":
		return "Object Group Size"
	case "drm":
		return "DRM"
	default:
		return testName
	}
}

func bitrateKbps(bytes int, result experimentResult) float64 {
	durationSeconds := result.MeasuredSeconds
	if durationSeconds <= 0 {
		durationSeconds = float64(experimentDurationSeconds)
	}
	return float64(bytes) * 8 / durationSeconds / 1000
}

func bitrateByteRatio(numeratorBytes, denominatorBytes int, result experimentResult) float64 {
	return ratioFloat(bitrateKbps(numeratorBytes, result), bitrateKbps(denominatorBytes, result))
}

func compressionRatio(originalBytes, compressedBytes int) float64 {
	return ratio(originalBytes, compressedBytes)
}

func ratio(numerator, denominator int) float64 {
	if denominator == 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}

func ratioFloat(numerator, denominator float64) float64 {
	if denominator == 0 {
		return 0
	}
	return numerator / denominator
}
