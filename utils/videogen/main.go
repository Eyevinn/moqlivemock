package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	duration        = 10 // seconds
	frameRate       = 25 // fps
	audioSampleRate = 48000
	audioBitrate    = 128 // kbps
	outputDir       = "output"
	logDir          = "logs"
	videoWidth      = 1280
	videoHeight     = 720
)

func main() {
	// Parse command line flags
	codecList := flag.String("codecs", "h264,aac,opus", "Comma-separated list of codecs to generate (h264, aac, opus))")
	fragmentDuration := flag.Int("fragment-duration", 0, "Fragment duration in milliseconds (0 = one sample/fragment")
	flag.Parse()

	// Parse the codec list
	codecs := strings.Split(*codecList, ",")
	codecMap := make(map[string]bool)
	for _, codec := range codecs {
		codecMap[strings.TrimSpace(codec)] = true
	}

	// Create output directory if it doesn't exist
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		log.Fatalf("Failed to create output directory: %v", err)
	}

	// Create logs directory if it doesn't exist
	if err := os.MkdirAll(logDir, 0755); err != nil {
		log.Fatalf("Failed to create logs directory: %v", err)
	}

	// Check and prepare required files
	ensureRequiredFiles()

	// Generate audio files based on codec selection
	if codecMap["aac"] {
		generateAudio("aac", "libfdk_aac", audioBitrate, *fragmentDuration)
	}
	if codecMap["opus"] {
		generateAudio("opus", "libopus", audioBitrate, *fragmentDuration)
	}

	type videoSetup struct {
		codec   string
		options []string
	}

	setups := []videoSetup{
		{"h264", []string{
			"-c:v", "libx264",
			"-preset", "medium",
			"-profile:v", "main",
			"-x264opts", fmt.Sprintf("keyint=%d:min-keyint=%d:scenecut=0:bframes=0:force-cfr=1", frameRate, frameRate),
			"-pix_fmt", "yuv420p"},
		},
	}

	// Generate video files based on codec selection
	for _, setup := range setups {
		if codecMap[setup.codec] {
			// Generate video files at different bitrates
			videoBitrates := []int{400, 600, 900} // kbps
			for _, bitrate := range videoBitrates {
				generateVideo(setup.codec, setup.options, bitrate, *fragmentDuration)
			}
		}
	}

	fmt.Println("All files generated successfully!")

	// Print average bitrates based on file sizes
	printActualBitrates(codecMap)
}

func ensureRequiredFiles() {

	// Check if font file exists
	if _, err := os.Stat("resources/RobotoSlab-Regular.ttf"); os.IsNotExist(err) {
		log.Fatalf("Required font file resources/RobotoSlab-Regular.ttf not found")
	}
}

func generateAudio(codec, codecLib string, bitrateKbps, fragmentDurationMs int) {
	outputFile := filepath.Join(outputDir, fmt.Sprintf("audio_monotonic_%dkbps_%s.mp4", bitrateKbps, codec))
	logFile := filepath.Join(logDir, fmt.Sprintf("audio_%dkbps_%s.log", bitrateKbps, codec))
	fmt.Printf("Generating audio file: %s\n", outputFile)

	// Create log file
	logFileHandle, err := os.Create(logFile)
	if err != nil {
		log.Fatalf("Failed to create log file: %v", err)
	}
	defer logFileHandle.Close()

	// Use the same audio generation approach as in the shell script
	// Beep every second (aligned with timecode, not wall clock)
	cmdArgs := []string{
		"-y",          // Overwrite output file if it exists
		"-f", "lavfi", // Use libavfilter virtual input
		"-i", fmt.Sprintf("sine=frequency=1:beep_factor=880:sample_rate=%d", audioSampleRate), // Audio pattern with beeps
		"-c:a", codecLib,
		"-b:a", fmt.Sprintf("%dk", bitrateKbps), // Audio bitrate
	}

	// Add opus-specific options
	if codecLib == "libopus" {
		cmdArgs = append(cmdArgs, "-vbr", "off")
	}

	// Build movflags based on fragment duration
	var movflags string
	if fragmentDurationMs == 0 {
		movflags = "cmaf+separate_moof+delay_moov+skip_trailer+frag_every_frame"
	} else {
		movflags = "cmaf+separate_moof+delay_moov+skip_trailer"
	}

	cmdArgs = append(cmdArgs, []string{
		"-ar", fmt.Sprintf("%d", audioSampleRate), // 48kHz sample rate
		"-ac", "2", // Stereo audio (2 channels)
		"-metadata:s:a:0", "language=mon", // Set language to 'mon' to indicate monotonic
		"-t", fmt.Sprintf("%d", duration), // Duration in seconds
		"-movflags", movflags, // MP4 fragmentation
	}...)

	// Add fragment duration if specified
	if fragmentDurationMs > 0 {
		fragmentDurationMicros := fragmentDurationMs * 1000 // Convert ms to microseconds
		cmdArgs = append(cmdArgs, "-frag_duration", fmt.Sprintf("%d", fragmentDurationMicros))
	}

	cmdArgs = append(cmdArgs, outputFile)

	// Print the ffmpeg command
	cmdString := "ffmpeg " + strings.Join(cmdArgs, " ")
	fmt.Println("Executing ffmpeg command:")
	fmt.Println(cmdString)

	// Write command to log file
	_, _ = logFileHandle.WriteString("Command: " + cmdString + "\n\n")

	cmd := exec.Command("ffmpeg", cmdArgs...)
	cmd.Stdout = logFileHandle
	cmd.Stderr = logFileHandle

	if err := cmd.Run(); err != nil {
		log.Fatalf("Failed to generate audio file: %v", err)
	}

	fmt.Printf("Audio %s generation completed. Log saved to: %s\n", codec, logFile)
}

func generateVideo(codec string, options []string, bitrateKbps, fragmentDurationMs int) {
	// Map internal codec names to output file codec suffixes
	codecSuffix := codec
	switch codec {
	case "h264":
		codecSuffix = "avc"
	case "h265":
		codecSuffix = "hevc"
	}
	outputFile := filepath.Join(outputDir, fmt.Sprintf("video_%dkbps_%s.mp4", bitrateKbps, codecSuffix))
	logFile := filepath.Join(logDir, fmt.Sprintf("video_%dkbps_%s.log", bitrateKbps, codecSuffix))
	fmt.Printf("Generating video file at %d kbps: %s\n", bitrateKbps, outputFile)

	// Create log file
	logFileHandle, err := os.Create(logFile)
	if err != nil {
		log.Fatalf("Failed to create log file: %v", err)
	}
	defer logFileHandle.Close()

	fontFile := "resources/RobotoSlab-Regular.ttf"
	logoFile := "resources/logo.png"

	// Select text color based on bitrate
	var textColor string
	switch bitrateKbps {
	case 400:
		textColor = "white"
	case 600:
		textColor = "yellow"
	case 900:
		textColor = "orange"
	default:
		textColor = "white"
	}

	// Scale logo to half size, then rotate so it completes a full turn in 10s
	logoScale := "scale=iw/2:ih/2"
	rotationDuration := float64(duration) // 10s for a full rotation
	rotationExpr := fmt.Sprintf("2*PI*n/(%d*%d)", frameRate, int(rotationDuration))
	//nolint: lll
	videoFilter := fmt.Sprintf(
		"[1:v]%s,format=rgba,rotate='%s':c=none:ow=rotw(iw):oh=roth(ih)[logo];"+
			"[0:v][logo]overlay=x=20:y=main_h-overlay_h-20:shortest=1[bg];"+
			"[bg]drawtext=fontfile=%s:text='Bitrate\\: %d kbps':fontcolor=%s:fontsize=36:box=1:boxcolor=black@0.5:boxborderw=5:x=20:y=20,"+
			"drawtext=fontfile=%s:text='Resolution\\: %d x %d':fontcolor=%s:fontsize=36:box=1:boxcolor=black@0.5:boxborderw=5:x=20:y=70,"+
			"drawtext=fontfile=%s:text='Time\\: %%{pts\\:hms}':fontcolor=%s:fontsize=36:box=1:boxcolor=black@0.5:boxborderw=5:x=20:y=120,"+
			"drawtext=fontfile=%s:text='Frame\\: %%{frame_num}':fontcolor=%s:fontsize=36:box=1:boxcolor=black@0.5:boxborderw=5:x=20:y=170",
		logoScale, rotationExpr,
		fontFile, bitrateKbps, textColor, fontFile, videoWidth, videoHeight, textColor, fontFile, textColor, fontFile, textColor,
	)

	// ffmpeg command line args
	cmdArgsFirst := []string{
		"-y",
		"-f", "lavfi",
		"-i", fmt.Sprintf("testsrc=size=%dx%d:rate=%d:duration=%d:decimals=3", videoWidth, videoHeight, frameRate, duration),
		"-loop", "1", // Loop the logo image
		"-framerate", fmt.Sprintf("%d", frameRate), // Match video framerate
		"-i", logoFile,
		"-filter_complex", videoFilter,
	}

	// Build movflags based on fragment duration
	var movflags string
	if fragmentDurationMs == 0 {
		movflags = "cmaf+separate_moof+delay_moov+skip_trailer+frag_every_frame"
	} else {
		movflags = "cmaf+separate_moof+delay_moov+skip_trailer"
	}

	cmdArgsLast := []string{
		"-b:v", fmt.Sprintf("%dk", bitrateKbps),
		"-an",
		"-movflags", movflags,
	}

	// Add fragment duration if specified
	if fragmentDurationMs > 0 {
		fragmentDurationMicros := fragmentDurationMs * 1000 // Convert ms to microseconds
		cmdArgsLast = append(cmdArgsLast, "-frag_duration", fmt.Sprintf("%d", fragmentDurationMicros))
	}

	cmdArgsLast = append(cmdArgsLast, outputFile)

	// Print the ffmpeg command
	cmdArgs := make([]string, 0, len(cmdArgsFirst)+len(options)+len(cmdArgsLast))
	cmdArgs = append(cmdArgs, cmdArgsFirst...)
	cmdArgs = append(cmdArgs, options...)
	cmdArgs = append(cmdArgs, cmdArgsLast...)
	cmdString := "ffmpeg " + strings.Join(cmdArgs, " ")
	fmt.Println("Executing ffmpeg command:")
	fmt.Println(cmdString)

	// Write command to log file
	_, _ = logFileHandle.WriteString("Command: " + cmdString + "\n\n")

	// Run ffmpeg command
	cmd := exec.Command("ffmpeg", cmdArgs...)
	cmd.Stdout = logFileHandle
	cmd.Stderr = logFileHandle

	if err := cmd.Run(); err != nil {
		log.Fatalf("Failed to generate video file at %d kbps: %v", bitrateKbps, err)
	}

	fmt.Printf("Video generation completed. Log saved to: %s\n", logFile)
}

func printActualBitrates(codecMap map[string]bool) {
	fmt.Println("\nActual average bitrates based on file sizes:")
	fmt.Println("--------------------------------------------")

	// Check audio files based on selected codecs
	if codecMap["aac"] {
		audioFile := filepath.Join(outputDir, fmt.Sprintf("audio_monotonic_%dkbps_aac.mp4", audioBitrate))
		printFileBitrate(audioFile, duration, true)
	}
	if codecMap["opus"] {
		audioFile := filepath.Join(outputDir, fmt.Sprintf("audio_monotonic_%dkbps_opus.mp4", audioBitrate))
		printFileBitrate(audioFile, duration, true)
	}

	// Check video files based on selected codecs
	videoBitrates := []int{400, 600, 900} // kbps - keep in sync with main()
	for _, bitrate := range videoBitrates {
		if codecMap["h264"] {
			videoFile := filepath.Join(outputDir, fmt.Sprintf("video_%dkbps_avc.mp4", bitrate))
			printFileBitrate(videoFile, duration, false)
		}
	}
}

func printFileBitrate(filePath string, durationSec int, isAudio bool) {
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		fmt.Printf("Error getting file info for %s: %v\n", filePath, err)
		return
	}

	// Calculate bitrate: (file size in bits) / (duration in seconds)
	fileSizeBytes := fileInfo.Size()
	fileSizeBits := fileSizeBytes * 8
	actualBitrateKbps := float64(fileSizeBits) / float64(durationSec) / 1000.0

	// Get target bitrate from filename
	fileName := filepath.Base(filePath)
	fileType := "Video"
	if isAudio {
		fileType = "Audio"
	}

	fmt.Printf("%s file: %s\n", fileType, fileName)
	fmt.Printf("  File size: %.2f KB (%.2f MB)\n", float64(fileSizeBytes)/1024.0, float64(fileSizeBytes)/1024.0/1024.0)
	fmt.Printf("  Duration: %d seconds\n", durationSec)
	fmt.Printf("  Average bitrate: %.2f kbps\n\n", actualBitrateKbps)
}
