package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Eyevinn/moqlivemock/internal"
	"github.com/quic-go/quic-go/quicvarint"
)

const (
	defaultTestFileGeneratorInput  = "../../assets/test10s/video_400kbps_avc.mp4"
	defaultTestFileGeneratorOutput = "../../assets/locmaftestfiles"
	cmafInitReferenceFile          = "init.cmaf.mp4"
	locmafInitEncodingFile         = "LOCMAF-init-encoding"
	locmafFullMoofObjectFile       = "LOCMAF-full-moof-object"
	locmafDeltaMoofObjectFile      = "LOCMAF-delta-moof-object"
	testFileGeneratorKID           = "39112233445566778899aabbccddeeff"
	testFileGeneratorKey           = "40112233445566778899aabbccddeeff"
	testFileGeneratorIV            = "41112233445566778899aabbccddeeff"
	testFileGeneratorLAURL         = "http://localhost:8081/clearkey"
)

func runTestFileGeneratorCommand(args []string) error {
	flags := flag.NewFlagSet("testgen", flag.ContinueOnError)
	inputPath := flags.String("input", defaultTestFileGeneratorInput, "MP4 file to generate LOCMAF test files from")
	outputDir := flags.String("out", defaultTestFileGeneratorOutput, "directory to write generated test files")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() > 0 {
		return fmt.Errorf("unexpected positional arguments: %v", flags.Args())
	}

	return generateArtifacts(*inputPath, *outputDir)
}

func generateArtifacts(inputPath, outputDir string) error {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("create output directory %s: %w", outputDir, err)
	}

	track, err := loadProtectedTestGeneratorTrack(inputPath)
	if err != nil {
		return err
	}

	cmafInitReference, err := track.SpecData.GenCMAFInitData()
	if err != nil {
		return fmt.Errorf("generate CMAF init reference: %w", err)
	}

	compressedMoov, err := internal.CompressMoov(track.SpecData.GetInit().Moov)
	if err != nil {
		return fmt.Errorf("unable to compress init: %w", err)
	}
	initEncoding := createCompressedObject(uint64(internal.MoovHeader), compressedMoov, nil)

	fullMoofObjects, err := generateFullMoofObjects(track, 2)
	if err != nil {
		return err
	}

	deltaMoofObjects, err := generateDeltaMoofObjects(track, 2)
	if err != nil {
		return err
	}

	return writeEncodedTestFiles(outputDir, cmafInitReference, initEncoding, fullMoofObjects, deltaMoofObjects)
}

func loadProtectedTestGeneratorTrack(inputPath string) (*internal.ContentTrack, error) {
	assetDir := filepath.Dir(inputPath)
	trackName := strings.TrimSuffix(filepath.Base(inputPath), filepath.Ext(inputPath))

	drm, err := internal.ParseCENCflags("cbcs", testFileGeneratorKID, testFileGeneratorKey,
		testFileGeneratorIV, testFileGeneratorLAURL)
	if err != nil {
		return nil, fmt.Errorf("configure DRM test files: %w", err)
	}
	asset, err := internal.LoadAssetWithDRM(assetDir, 1, 1, drm)
	if err != nil {
		return nil, fmt.Errorf("load DRM asset %s: %w", assetDir, err)
	}
	track := asset.GetTrackByName(trackName + "_drm")
	if track == nil {
		return nil, fmt.Errorf("protected track %q not found in %s", trackName+"_drm", assetDir)
	}
	return track, nil
}

func generateFullMoofObjects(track *internal.ContentTrack, count int) ([][]byte, error) {
	objects := make([][]byte, 0, count)
	for i := 0; i < count; i++ {
		var compressor internal.MoofDeltaCompressor
		object, err := track.GenLocmafChunk(uint32(i), uint64(i), uint64(i+1), &compressor)
		if err != nil {
			return nil, fmt.Errorf("generate full LOCMAF moof object %d: %w", i+1, err)
		}
		objects = append(objects, object)
	}
	return objects, nil
}

func generateDeltaMoofObjects(track *internal.ContentTrack, count int) ([][]byte, error) {
	objects := make([][]byte, 0, count)
	var compressor internal.MoofDeltaCompressor
	for i := 0; len(objects) < count; i++ {
		object, err := track.GenLocmafChunk(uint32(i), uint64(i), uint64(i+1), &compressor)
		if err != nil {
			return nil, fmt.Errorf("generate delta LOCMAF moof object %d: %w", len(objects)+1, err)
		}
		headerID, err := parseLocmafHeaderID(object)
		if err != nil {
			return nil, fmt.Errorf("parse generated LOCMAF object %d: %w", i, err)
		}
		if headerID == uint64(internal.MoofDeltaHeader) {
			objects = append(objects, object)
		}
	}
	return objects, nil
}

func createCompressedObject(headerID uint64, locPayload, mdatPayload []byte) []byte {
	object := quicvarint.Append(nil, headerID)
	object = quicvarint.Append(object, uint64(len(locPayload)))
	object = append(object, locPayload...)
	object = append(object, mdatPayload...)
	return object
}

func parseLocmafHeaderID(object []byte) (uint64, error) {
	headerID, _, err := quicvarint.Parse(object)
	return headerID, err
}

func writeEncodedTestFiles(outputDir string, cmafInitReference,
	initEncoding []byte, fullMoofObjects, deltaMoofObjects [][]byte) error {

	files := []struct {
		name string
		data []byte
	}{
		{cmafInitReferenceFile, cmafInitReference},
		{locmafInitEncodingFile, initEncoding},
		{locmafFullMoofObjectFile + "-1", fullMoofObjects[0]},
		{locmafFullMoofObjectFile + "-2", fullMoofObjects[1]},
		{locmafDeltaMoofObjectFile + "-1", deltaMoofObjects[0]},
		{locmafDeltaMoofObjectFile + "-2", deltaMoofObjects[1]},
	}
	for _, file := range files {
		path := filepath.Join(outputDir, file.name)
		if err := os.WriteFile(path, file.data, 0o644); err != nil {
			return fmt.Errorf("unable to write %s: %w", path, err)
		}
		fmt.Printf("Wrote to file: %s\n", path)
	}
	return nil
}
