# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Repository Overview

This is moqlivemock, a MoQ (Media over QUIC) test service that provides both server/publisher (`mlmpub`) and client/subscriber (`mlmsub`) implementations. It demonstrates live media streaming using the MoQ Transport protocol with CMAF (Common Media Application Format) media segments.

## Common Development Commands

### Building
```bash
# Build both binaries
make build

# Build specific binaries
go build -o out/mlmpub ./cmd/mlmpub
go build -o out/mlmsub ./cmd/mlmsub
```

### Testing
```bash
# Run all tests
make test
go test ./...

# Run specific tests
go test ./internal/...
go test -run TestMediaSyncer ./...
```

### Code Quality
```bash
# Run linting (required before commits)
make lint
golangci-lint run

# Generate coverage reports
make coverage
```

### Running Applications

#### Publisher (mlmpub)
```bash
# Basic QUIC server
go run ./cmd/mlmpub -cert cert.pem -key key.pem -addr :4443

# With custom asset directory
go run ./cmd/mlmpub -cert cert.pem -key key.pem -asset ./assets/test10s

# With fingerprint server for WebTransport
go run ./cmd/mlmpub -cert cert.pem -key key.pem -fingerprintport 8080
```

#### Subscriber (mlmsub) - NEW MODULAR ARCHITECTURE
```bash
# Simple playback
go run ./cmd/mlmsub -addr localhost:4443 -muxout - | ffplay -

# Separate video and audio outputs
go run ./cmd/mlmsub -addr localhost:4443 -videoout video.mp4 -audioout audio.mp4

# Seamless track switching (staircase pattern)
go run ./cmd/mlmsub -addr localhost:4443 -switch-tracks -muxout - | ffplay -

# Debug with verbose logging
go run ./cmd/mlmsub -addr localhost:4443 -loglevel debug
```

## Architecture Overview

### mlmpub (Publisher)
- Serves pre-recorded CMAF segments from asset directories
- Supports both QUIC and WebTransport connections
- Provides WARP catalog for track discovery
- Configurable batching for audio/video samples per MoQ object

### mlmsub (Subscriber) - REFACTORED MODULAR DESIGN

The mlmsub has been completely refactored from a monolithic 767-line handler to a modular architecture:

#### Core Components

1. **SubscriptionManager** (`subscription.go`)
   - Manages MoQ track subscriptions and SUBSCRIBE_UPDATE messages
   - Handles control plane operations
   - Provides track name lookup for switching operations

2. **MediaRouter** (`router.go`)
   - Routes MediaObjects from subscriptions to output pipelines
   - Integrates with TrackSwitcher for seamless transitions
   - Handles duplicate detection during switching

3. **MediaPipeline** (`pipeline.go`)
   - Processes media objects for different output types (mux, video, audio)
   - Handles init segment management
   - Supports concurrent processing

4. **TrackSwitcher** (`switcher.go`)
   - Manages seamless track switching using SUBSCRIBE_UPDATE messages
   - Uses optimal SUBSCRIBE_OK largest_location approach
   - Implements staircase switching pattern (0→1→2→1→0...)

5. **SimpleClient/SwitchingClient** (`client.go`)
   - High-level client implementations
   - Demonstrates component integration
   - Provides both simple playback and switching scenarios

#### Communication Pattern
- **MediaObject structs**: `{trackName, groupID, objectID, mediaType, payload}`
- **Go channels**: Async communication between components
- **Separated concerns**: Clean boundaries between control/media/output

### Key Features

#### Seamless Track Switching
- **Immediate SUBSCRIBE_UPDATE**: Uses largest_group_id from SUBSCRIBE_OK
- **No race conditions**: Clean separation from reception loop
- **Staircase pattern**: Video (0→1→2→1→0...), Audio (0→1→0→1...)
- **Optimal timing**: 2s intervals, 1s initial wait

#### Media Processing
- **CMAF segments**: H.264 video + AAC audio
- **Init segments**: Proper handling from catalog track.InitData
- **Multiplexing**: Real-time mux for synchronized playback
- **Output modes**: Separate video/audio or combined mux

## Development Tips

### Track Switching Testing
```bash
# Test with 3 video tracks (400kbps, 600kbps, 900kbps)
go run ./cmd/mlmsub -addr localhost:4443 -switch-tracks -loglevel debug

# Monitor switching pattern in logs:
# Video: 400kbps→600kbps→900kbps→600kbps→400kbps→600kbps...
# Audio: monotonic→scale→monotonic→scale...
```

### Asset Management
- Default assets in `assets/test10s/`: 10-second looping content
- Video tracks: 400kbps, 600kbps, 900kbps H.264
- Audio tracks: monotonic beeps, scale sequence beeps
- Catalog format: WARP-compatible JSON with base64 init segments

### Debugging
- Use `-loglevel debug` for verbose output
- Check SUBSCRIBE_OK messages for largest_group_id
- Monitor SUBSCRIBE_UPDATE timing
- Verify no duplicate audio streams during switching

### Common Issues
- **Multiple audio streams**: Usually indicates race condition in SUBSCRIBE_UPDATE
- **Video not switching back**: Check staircase direction logic
- **Init segment missing**: Verify catalog track.InitData handling
- **Switch blocking**: Ensure proper switch completion state management

## Testing Workflow

1. **Start publisher**: `go run ./cmd/mlmpub -cert cert.pem -key key.pem`
2. **Test simple playback**: `go run ./cmd/mlmsub -addr localhost:4443 -muxout - | ffplay -`
3. **Test track switching**: `go run ./cmd/mlmsub -addr localhost:4443 -switch-tracks -muxout - | ffplay -`
4. **Verify logs**: Check for clean SUBSCRIBE_UPDATE messages and seamless transitions

## Commit Guidelines

- Always run `golangci-lint` before commits (required by CLAUDE.md)
- Follow conventional commit format: `feat:`, `fix:`, `refactor:`
- Test both simple playback and track switching scenarios
- Update documentation for API changes