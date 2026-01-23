# Video and Audio Test Sequence Generator

This Go program uses FFmpeg to generate test video and audio suitable for being
sent over MoQ (MediaOverQuic) one frame at at time.

## Features

- Generates AAC or Opus stereo audio at 48kHz with beeps (regular beeps every second) (language code: mon)
- Creates x264 video with only I and P frames (no B-frames)
- Video shows bitrate, resolution, time, and frame number
- Video encoded at 400, 600, and 900 kbps
- IDR frames every second (25 frames)
- 10-second duration for all outputs
- Audio encoded at 128 kbps, but has a relatively large overhead
- Outputs fragmented MP4 files with each frame in an individual fragment
- A script generates audio with C-major scale beeps (language code: sca)

## Requirements

- Go (1.16 or later recommended)
- FFmpeg installed and available in your PATH

## Usage

1. Run the program:

```bash
go run main.go
```

2. Check the `output` directory for the generated files:
   - `audio_monotonic_128kbps_aac.mp4`: AAC stereo audio track
   - `audio_monotonic_128kbps_opus.mp4`: Opus stereo audio track
   - `video_400kbps_avc.mp4`: 400 kbps H.264/AVC video track
   - `video_600kbps_avc.mp4`: 600 kbps H.264/AVC video track
   - `video_900kbps_avc.mp4`: 900 kbps H.264/AVC video track

The actual average bitrates (from the size of the files) are:

* audio 171kbps
* video_400kbps_avc 396kbps
* video_600kbps_avc 583kbps
* video_900kbps_avc 868kbps

There is a relatively high overhead of 100bytes per sample corresponding
to 25kbps for video and 40kbps for audio.

2. Audio with C-major scale beeps

```bash
./gen_audio_scale.sh
```

generates the files `audio_scale_128kbps_aac.mp4` and `audio_scale_128kbps_opus.mp4`.

## MP4 Fragmentation

The program by default uses the following FFmpeg flags for MP4 fragmentation:
```
-movflags cmaf+separate_moof+delay_moov+skip_trailer+frag_every_frame
```

This ensures each frame is in an individual MP4 fragment, suitable for streaming applications.

However, one can also set a longer duration (in milliseconds), e.g. 1000 matching a new fragment
every 1s.