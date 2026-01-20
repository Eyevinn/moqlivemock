# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with this repository.

## Project Overview

moqlivemock is a Go-based MoQ (Media over QUIC) live streaming mock implementation. It provides:

- **mlmpub**: A publisher that serves live media content over MoQ Transport
- **mlmsub**: A subscriber that receives and processes media streams

## Build Commands

```bash
go build ./...              # Build all packages
go test ./...               # Run all tests
go mod tidy                 # Update dependencies
go mod vendor               # Vendor dependencies
```

## Architecture

### Key Components

- `cmd/mlmpub/` - Publisher application serving media tracks (video, audio, subtitles)
- `cmd/mlmsub/` - Subscriber application receiving media
- `internal/` - Shared internal packages:
  - `asset.go` - Asset loading and track management
  - `catalog.go` - WARP catalog generation
  - `subtitle.go` - Dynamic subtitle generation (WVTT/STPP)
  - `moqgroup.go` - MoQ group/object handling

### MoQ Transport Dependency

Uses a fork of moqtransport with draft-14 support:
```go
replace github.com/mengelbart/moqtransport => github.com/Eyevinn/moqtransport v0.5.1-...
```

The fork maintains API compatibility while updating wire protocol to draft-14:
- Session creation: `&moqtransport.Session{Handler: ..., SubscribeHandler: ...}`
- Call `session.Run(conn)` to start
- Subscriptions use separate `SubscribeHandler` interface

### Handler Pattern

Publishers implement:
- `Handler` - for ANNOUNCE messages
- `SubscribeHandler` - for SUBSCRIBE messages (returns `*SubscribeResponseWriter`)

Subscribers implement:
- `Handler` - for ANNOUNCE messages (accept/reject)
- `SubscribeHandler` - for SUBSCRIBE messages (typically reject as they don't publish)

### Subtitle Tracks

Subtitles are dynamically generated (not loaded from files) showing UTC time and group number:
- **WVTT** (WebVTT in CMAF) - codec: `wvtt`, uses `mp4.VttcBox`/`mp4.PaylBox`
- **STPP** (TTML in CMAF) - codec: `stpp.ttml.im1t`, uses Go templates (`stpptime.xml`)

Key types in `internal/subtitle.go`:
- `SubtitleTrack` - track configuration (format, language, timing)
- `SubtitleData` - implements `CodecSpecificData` interface for init segments
- `GenSubtitleGroup()` - generates MoQ group with CMAF media segment

Configuration via mlmpub flags:
- `-subswvtt "en,sv"` - comma-separated WVTT languages (default: "sv")
- `-subsstpp "en,sv"` - comma-separated STPP languages (default: "en")

Track naming: `subs_wvtt_{lang}`, `subs_stpp_{lang}`

## Testing

The `internal/` package contains unit tests. Run with:
```bash
go test ./internal/...
```
