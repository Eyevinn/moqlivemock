#!/bin/bash

# Create a 10-second base with low-level white noise
ffmpeg -f lavfi -i "anoisesrc=amplitude=0.01:color=white:duration=10" -c:a pcm_s16le -ar 48000 -ac 2 silent_with_noise.wav

# Frequencies for extended C-major scale (C4 to E5)
# C4=261.63, D4=293.66, E4=329.63, F4=349.23, G4=392.00, A4=440.00, B4=493.88, C5=523.25, D5=587.33, E5=659.25
freqs=(261.63 293.66 329.63 349.23 392.00 440.00 493.88 523.25 587.33 659.25)

# Generate each note and mix them
for i in {0..9}; do
  ffmpeg -f lavfi -i "sine=frequency=${freqs[$i]}:duration=0.1" -af "adelay=$((i*1000))|$((i*1000))" "note$i.wav"
done

# Mix all notes with the base that has white noise
ffmpeg -i silent_with_noise.wav \
  $(for i in {0..9}; do echo "-i note$i.wav"; done) \
  -filter_complex "$(for i in {0..10}; do echo "[$i:0]"; done)amix=inputs=11:duration=longest" \
  -c:a pcm_s16le c_major_scale.wav

# Encode as chunked MP4 file with AAC audio codec at 128kbps
# Set language to 'eng' and use CMAF fragmentation
ffmpeg -y -i c_major_scale.wav \
  -c:a aac \
  -b:a 128k \
  -ar 48000 \
  -ac 2 \
  -metadata:s:a:0 language=sca \
  -movflags cmaf+separate_moof+delay_moov+skip_trailer+frag_every_frame \
  audio_scale_128kbps.mp4

# Clean up temporary files
rm silent_with_noise.wav c_major_scale.wav
for i in {0..9}; do
  rm "note$i.wav"
done

echo "Audio scale generation completed. Output file: audio_scale_128kbps.mp4"
