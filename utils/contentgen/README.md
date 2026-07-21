# Video and Audio Test Sequence Generator

This directory contains tools to generate test video and audio suitable for being
sent over MoQ (MediaOverQuic) one frame at a time.

## Features

### Video (Go program)
- Encodes AVC (libx264), HEVC (libx265), and AV1 (libsvtav1)
- All codecs produce only I and P frames (no B-frames / no reordering).
  AV1 uses SVT-AV1 low-delay CBR mode (VBR is not supported for low delay).
- Video shows codec, bitrate, resolution, time, and frame number
  (the codec name is the first overlay line)
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
- A small Go post-processor (`trimaudio`) strips encoder priming, drops any
  trailing short sample, trims to the codec's target frame count, and removes
  the elst — so the source mp4 has uniform per-sample durations starting at
  `tfdt=0` and the publisher can loop it without per-loop drift.

## Requirements

- Go (1.16 or later recommended)
- FFmpeg with the `drawtext` filter (libfreetype) plus `libx264`, `libx265`,
  and `libsvtav1` for video, and libfdk_aac for AAC audio. A build that has
  all of these (e.g. Homebrew's `ffmpeg-full`) is required for AV1; point the
  tool at it with `FFMPEG_PATH` if it is not your default `ffmpeg`.

## Usage

### Generate Video

```bash
# Default is AVC only; pass -codecs to select codecs.
go run videogen.go -codecs h264,h265,av1

# If your default ffmpeg lacks libsvtav1 or drawtext, point at one that has both:
FFMPEG_PATH=/opt/homebrew/opt/ffmpeg-full/bin/ffmpeg go run videogen.go -codecs h264,h265,av1
```

Output files in `output/` (suffix per codec: `avc`, `hevc`, `av1`):
- `video_{400,600,900}kbps_avc.mp4`: H.264/AVC video tracks
- `video_{400,600,900}kbps_hevc.mp4`: HEVC/H.265 video tracks
- `video_{400,600,900}kbps_av1.mp4`: AV1 video tracks

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

### Post-process audio for seamless looping

After generating the raw audio mp4s, run the `trimaudio` tool in-place to
strip priming, drop trailing short samples, and remove the elst:

```bash
go run ./trimaudio -inplace output/audio_*.mp4
```

Target frame counts (TS=48000):

| Codec | Frame ts | Frames | Total ts | Total seconds |
|-------|---------:|-------:|---------:|--------------:|
| AAC   | 1024     | 469    | 480 256  | 10.005 333    |
| Opus  | 960      | 500    | 480 000  | 10.000 000    |
| AC-3  | 1536     | 313    | 480 768  | 10.016 000    |

Each codec's loop period averages exactly 10 s wall-clock over a small
integer number of loops (AAC: 4 loops / 40 s; Opus: 1 loop; AC-3: 2 loops /
20 s), so the publisher's emission stays in lock-step with wall-clock and
the audio live edge never drifts.

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
