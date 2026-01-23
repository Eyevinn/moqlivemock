#!/bin/bash

# Parse command line arguments
fragment_duration=0  # Default: 0 = one fragment per sample
while [[ $# -gt 0 ]]; do
  case $1 in
    --fragment-duration)
      fragment_duration="$2"
      shift 2
      ;;
    *)
      echo "Unknown option: $1"
      echo "Usage: $0 [--fragment-duration <ms>]"
      echo "  --fragment-duration: Fragment duration in milliseconds (0 = one fragment per sample)"
      exit 1
      ;;
  esac
done

echo "Fragment duration: ${fragment_duration}ms"

# Create a 10-second silent base
ffmpeg -f lavfi -i "anullsrc=r=48000:cl=stereo:d=10" -c:a pcm_s16le silent_base.wav

# Generate 10 beeps at 880Hz (A4), one per second
freq=880
for i in {0..9}; do
  ffmpeg -f lavfi -i "sine=frequency=${freq}:duration=0.5" -af "volume=0.8,afade=t=out:st=0.3:d=0.2,adelay=$((i*1000))|$((i*1000))" "beep$i.wav"
done

# Mix all beeps with the silent base, normalize=0 prevents volume reduction
ffmpeg -i silent_base.wav \
  $(for i in {0..9}; do echo "-i beep$i.wav"; done) \
  -filter_complex "$(for i in {0..10}; do echo "[$i:0]"; done)amix=inputs=11:duration=longest:normalize=0" \
  -c:a pcm_s16le monotonic_beeps.wav

# Encode with different codecs and bitrates
# Define codec configurations: codec:bitrate:output_file
codec_configs=(
  "libfdk_aac:128k:audio_monotonic_128kbps_aac.mp4"
  "opus:128k:audio_monotonic_128kbps_opus.mp4"
)

for config in "${codec_configs[@]}"; do
  IFS=':' read -r codec bitrate output <<< "$config"
  echo "Encoding with $codec at $bitrate..."

  # Build movflags based on fragment duration
  if [[ "$fragment_duration" -eq 0 ]]; then
    movflags="cmaf+separate_moof+delay_moov+skip_trailer+frag_every_frame"
    frag_args=""
  else
    movflags="cmaf+separate_moof+delay_moov+skip_trailer"
    fragment_duration_micros=$((fragment_duration * 1000))  # Convert ms to microseconds
    frag_args="-frag_duration $fragment_duration_micros"
  fi

  # Add opus-specific options
  if [[ "$codec" == "opus" ]]; then
    ffmpeg -y -i monotonic_beeps.wav \
      -t 10 \
      -c:a "$codec" \
      -b:a "$bitrate" \
      -strict -2 \
      -ar 48000 \
      -ac 2 \
      -metadata:s:a:0 language=mon \
      -movflags "$movflags" \
      $frag_args \
      "output/$output"
  else
    ffmpeg -y -i monotonic_beeps.wav \
      -t 10 \
      -c:a "$codec" \
      -b:a "$bitrate" \
      -ar 48000 \
      -ac 2 \
      -metadata:s:a:0 language=mon \
      -movflags "$movflags" \
      $frag_args \
      "output/$output"
  fi
done

# Clean up temporary files
rm silent_base.wav monotonic_beeps.wav
for i in {0..9}; do
  rm "beep$i.wav"
done

echo "Audio monotonic generation completed. Output files: output/audio_monotonic_128kbps_aac.mp4, output/audio_monotonic_128kbps_opus.mp4"
