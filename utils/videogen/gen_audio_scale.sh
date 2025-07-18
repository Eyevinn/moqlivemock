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

# Create a 10-second base with low-level white noise
ffmpeg -f lavfi -i "anoisesrc=amplitude=0.001:color=white:duration=10" -c:a pcm_s16le -ar 48000 -ac 2 silent_with_noise.wav

# Frequencies for extended C-major scale (C4 to E5)
# C4=261.63, D4=293.66, E4=329.63, F4=349.23, G4=392.00, A4=440.00, B4=493.88, C5=523.25, D5=587.33, E5=659.25
freqs=(261.63 293.66 329.63 349.23 392.00 440.00 493.88 523.25 587.33 659.25)

# Generate each note and mix them
for i in {0..9}; do
  ffmpeg -f lavfi -i "sine=frequency=${freqs[$i]}:duration=0.03" -af "volume=1.0,adelay=$((i*1000))|$((i*1000))" "note$i.wav"
done

# Mix all notes with the base that has white noise
ffmpeg -i silent_with_noise.wav \
  $(for i in {0..9}; do echo "-i note$i.wav"; done) \
  -filter_complex "$(for i in {0..10}; do echo "[$i:0]"; done)amix=inputs=11:duration=longest" \
  -c:a pcm_s16le c_major_scale.wav

# Encode with different codecs and bitrates
# Define codec configurations: codec:bitrate:output_file
codec_configs=(
  "libfdk_aac:128k:audio_scale_128kbps_aac.mp4"
  "libopus:128k:audio_scale_128kbps_opus.mp4"
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
  if [[ "$codec" == "libopus" ]]; then
    ffmpeg -y -i c_major_scale.wav \
      -t 10 \
      -c:a "$codec" \
      -b:a "$bitrate" \
      -vbr off \
      -ar 48000 \
      -ac 2 \
      -metadata:s:a:0 language=sca \
      -movflags "$movflags" \
      $frag_args \
      "output/$output"
  else
    ffmpeg -y -i c_major_scale.wav \
      -t 10 \
      -c:a "$codec" \
      -b:a "$bitrate" \
      -ar 48000 \
      -ac 2 \
      -metadata:s:a:0 language=sca \
      -movflags "$movflags" \
      $frag_args \
      "output/$output"
  fi
done

# Clean up temporary files
rm silent_with_noise.wav c_major_scale.wav
for i in {0..9}; do
  rm "note$i.wav"
done

echo "Audio scale generation completed. Output files: output/audio_scale_128kbps_aac.mp4, output/audio_scale_128_kbps_opus.mp4"
