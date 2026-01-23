# Video and Audio Test Sequence Generator

This directory contains tools to generate test video and audio suitable for being
sent over MoQ (MediaOverQuic) one frame at a time.

## Features

### Video (Go program)
- Creates x264 video with only I and P frames (no B-frames)
- Video shows bitrate, resolution, time, and frame number
- Video encoded at 400, 600, and 900 kbps
- IDR frames every second (25 frames)
- 10-second duration

### Audio (Shell scripts)
- Generates AAC, Opus, and AC-3 stereo audio at 48kHz
- Monotonic beeps: 880Hz beep every second (language code: mon)
- Scale beeps: C-major scale notes, one per second (language code: sca)
- Each beep is 0.5 seconds with fadeout
- AAC and Opus encoded at 128 kbps, AC-3 encoded at 192 kbps
- Outputs fragmented MP4 files with each frame in an individual fragment

## Requirements

- Go (1.16 or later recommended)
- FFmpeg installed and available in your PATH (with libfdk_aac for AAC encoding)

## Usage

### Generate Video

```bash
go run videogen.go
```

Output files in `output/`:
- `video_400kbps_avc.mp4`: 400 kbps H.264/AVC video track
- `video_600kbps_avc.mp4`: 600 kbps H.264/AVC video track
- `video_900kbps_avc.mp4`: 900 kbps H.264/AVC video track

### Generate Audio

Monotonic beeps (880Hz, one per second):
```bash
./gen_audio_monotonic.sh
```

Output files:
- `output/audio_monotonic_128kbps_aac.mp4`
- `output/audio_monotonic_128kbps_opus.mp4`
- `output/audio_monotonic_192kbps_ac3.mp4`

C-major scale beeps (one note per second):
```bash
./gen_audio_scale.sh
```

Output files:
- `output/audio_scale_128kbps_aac.mp4`
- `output/audio_scale_128kbps_opus.mp4`
- `output/audio_scale_192kbps_ac3.mp4`

## Actual Bitrates

The actual average bitrates (from the size of the files) are approximately:

* audio: ~171 kbps
* video_400kbps_avc: ~396 kbps
* video_600kbps_avc: ~583 kbps
* video_900kbps_avc: ~868 kbps

There is a relatively high overhead of ~100 bytes per sample corresponding
to ~25 kbps for video and ~40 kbps for audio.

## MP4 Fragmentation

The tools use the following FFmpeg flags for MP4 fragmentation:
```
-movflags cmaf+separate_moof+delay_moov+skip_trailer+frag_every_frame
```

This ensures each frame is in an individual MP4 fragment, suitable for streaming applications.

You can also set a longer fragment duration (in milliseconds) using the `--fragment-duration` flag:
```bash
go run videogen.go --fragment-duration 1000
./gen_audio_monotonic.sh --fragment-duration 1000
./gen_audio_scale.sh --fragment-duration 1000
```
