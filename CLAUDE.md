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

- `cmd/mlmpub/` - Publisher application serving media tracks
- `cmd/mlmsub/` - Subscriber application receiving media
- `internal/` - Shared internal packages (asset handling, catalog, media processing)

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

## Testing

The `internal/` package contains unit tests. Run with:
```bash
go test ./internal/...
```
