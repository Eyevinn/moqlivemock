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
- `cmd/mlmtest/` - Interop test client for [moq-interop-runner](https://github.com/englishm/moq-interop-runner)
- `internal/` - Shared internal packages:
  - `asset.go` - Asset loading and track management
  - `catalog.go` - MSF/CMSF catalog generation (CMAF, LOC, LOCMAF packaging)
  - `subtitle.go` - Dynamic subtitle generation (WVTT/STPP)
  - `moqgroup.go` - MoQ group/object handling
- LOCMAF encode/decode comes from `github.com/Eyevinn/locmaf` (the reusable
  codec module; one packaging version at a time, reported by `locmaf.Version`).
  See `docs/LOCMAF.md`; the normative spec is the IETF draft
  draft-einarsson-moq-locmaf

### MoQ Transport Dependency

Uses `github.com/Eyevinn/moqtransport` (forked from mengelbart/moqtransport) with draft-14 and draft-16 support.
Version negotiation uses ALPN (`moqt-16` for draft-16, `moq-00` for draft-14) and `WT-Available-Protocols` for WebTransport.
- Session creation: `&moqtransport.Session{Handler: ..., SubscribeHandler: ...}`
- Call `session.Run(conn)` to start
- Subscriptions use separate `SubscribeHandler` interface

### Multi-Namespace Architecture

mlmpub announces one or more namespaces. The CMSF namespaces carry a unified
catalog (draft-ietf-moq-msf-01) filtered by protection type; LOC uses an MSF
catalog; moq-mi is catalogless.

CMSF (unified CMAF + LOCMAF catalog):
- `cmsf/clear` — always present, clear (unencrypted) tracks
- `cmsf/drm-{scheme}` — when `-drmpath` is set, commercial DRM tracks (`_drm` suffix)
- `cmsf/eccp-{scheme}` — when `-kid`/`-iv` are set, ClearKey/ECCP tracks (`_eccp` suffix)

Each rendition appears twice in a CMSF catalog: a CMAF track `<name>`
(`packaging: "cmaf"`) and a LOCMAF track `<name>_locmaf`
(`packaging: "locmaf"`, `locmafVersion` from `locmaf.Version`, currently "0.3"), as alternates in the same
altGroup. Because LOCMAF init data is the raw CMAF init segment, both
variants reference one shared entry in the catalog `initDataList` via `initRef`
(draft-ietf-moq-msf-01). The serve path (`pub.PublishTrack`) picks the encoding
per track and strips the `_locmaf` suffix to resolve the underlying content
track.

Follow-up (not implemented): draft-ietf-moq-msf-01 §5.5/§12 define an OPTIONAL
`MSF_COMPRESSION` property to compress the catalog object payload. Catalogs are
emitted uncompressed today, which is fully conformant; the shared-init dedup
already removes most of the redundancy compression targets.

### LOCMAF specification versioning

One LOCMAF packaging version is supported at a time — the one implemented by
the `github.com/Eyevinn/locmaf` module and reported by `locmaf.Version`
(currently `"0.3"`); v0.2 remains reachable at the `locmaf-v0.2` tag. The
normative spec is the IETF Internet-Draft
[draft-einarsson-moq-locmaf](https://datatracker.ietf.org/doc/draft-einarsson-moq-locmaf/);
see `docs/LOCMAF.md` for how moqlivemock uses the codec. The packaging version
and the IETF draft revision advance independently — cite the
version-independent draft URL, not a pinned revision.

LOC (raw codec frames, one per object) and moq-mi (catalogless):
- `msf/clear` — LOC packaging (AVC + AAC/Opus, HEVC for `hev1.*`)
- `moq-mi/clear` — moq-mi packaging with fixed track names `video0` / `audio0`

Key types in `internal/pub/pub.go`:
- `NamespaceEntry` — pairs a namespace tuple with its catalog
- `Handler.Namespaces []NamespaceEntry` — all announced namespaces

Key types in `internal/asset.go`:
- `ProtectionType` — enum: `ProtectionNone`, `ProtectionDRM`, `ProtectionECCP`
- `ContentTrack.Protection` — identifies how a track is encrypted
- `Asset.Drm` / `Asset.Eccp` — independent DRM configs

`GenCMAFCatalogEntry(namespace, protectionType, timestamp)` generates a unified
CMSF catalog (CMAF + LOCMAF variants, shared init data) filtered to the
specified protection type.

### Handler Pattern

Publishers implement:
- `Handler` - for ANNOUNCE messages
- `SubscribeHandler` - for SUBSCRIBE messages (returns `*SubscribeResponseWriter`)
- `FetchHandler` - for FETCH messages (returns `*FetchResponseWriter`)

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

### Content Protection

Two independent encryption modes, both optional and can be active simultaneously:

**ClearKey/ECCP** (explicit key flags):
- `-kid` — key ID (32 hex chars)
- `-iv` — initialization vector (16 or 32 hex chars)
- `-cenckey` — encryption key (32 hex chars, defaults to kid if omitted)
- `-scheme` — `cenc` or `cbcs`
- `-laurl` — external license URL for the catalog (falls back to `http://localhost:{sideport}/clearkey`)
- `-sideport` — HTTP port serving `/fingerprint` and `/clearkey` endpoints

**Commercial DRM** (CPIX config file):
- `-drmpath` — path to config JSON (format: `assets/testdrm/drm_config_test.json`)

Track naming: clear tracks have no suffix, DRM tracks get `_drm`, ECCP tracks get `_eccp`.

### Video Codecs

Video tracks use `avc1` (H.264), `hvc1` (HEVC), and `av01` (AV1) sample
descriptors. AVC/HEVC store parameter sets (SPS/PPS for AVC, VPS/SPS/PPS for
HEVC) in the init segment rather than inlining them in each sample; this is
required for FairPlay DRM compatibility in Safari 26.4+. AV1 similarly carries
its decoder configuration (the sequence header OBU) in the `av1C` box of the
init segment (`internal/media.go` `AV1Data`, built via mp4ff's
`SetAV1Descriptor`). AV1 is currently offered only through the CMSF namespaces
(CMAF + LOCMAF variants); the LOC catalog path skips it. AV1 test assets are
generated by `utils/contentgen` in low-delay CBR mode (I/P only, no reordering).
Encryption works for AV1 too — the protection path is codec-agnostic and mp4ff
implements the AV1 CENC binding (only tile data encrypted, OBU headers clear),
so ClearKey/ECCP (`cenc`/`cbcs`) and commercial DRM all produce `_eccp`/`_drm`
AV1 tracks that round-trip correctly.

### Interop Testing (mlmtest)

`mlmtest` is an interop test client for [moq-interop-runner](https://github.com/englishm/moq-interop-runner).
It connects to a server/relay and runs protocol-level test cases with both draft-14 and draft-16, outputting TAP v14 results.

**Test cases:**
1. `setup-only` — connect, exchange SETUP, close
2. `announce-only` — PUBLISH_NAMESPACE, wait for REQUEST_OK, close
3. `publish-namespace-done` — PUBLISH_NAMESPACE, REQUEST_OK, PUBLISH_NAMESPACE_DONE, close
4. `subscribe-error` — SUBSCRIBE to non-existent track, expect REQUEST_ERROR
5. `announce-subscribe` — publisher announces, subscriber subscribes via relay
6. `subscribe-before-announce` — subscriber subscribes first, publisher announces 500ms later

**Usage:**
```bash
# Run all tests against a relay
go run ./cmd/mlmtest -r moqt://relay:443 -tls-disable-verify

# Run a specific test
go run ./cmd/mlmtest -r moqt://relay:443 -t setup-only

# List available tests
go run ./cmd/mlmtest -l

# Environment variables (used by moq-interop-runner)
RELAY_URL=moqt://relay:443 TESTCASE=setup-only TLS_DISABLE_VERIFY=1 go run ./cmd/mlmtest
```

## Testing

The `internal/` package contains unit tests. Run with:
```bash
go test ./internal/...
```

## References

IETF draft specifications are stored in `references/` for offline reference:

- `draft-ietf-moq-transport-14.txt` - MoQ Transport protocol (draft-14)
- `draft-ietf-moq-transport-16.txt` - MoQ Transport protocol (draft-16), the primary wire protocol used by this project
- `draft-ietf-moq-msf-01.txt` - MoQ Streaming Format (MSF), defines how media is mapped to MoQ tracks/groups/objects (catalog with initDataList/initRef)
- `draft-ietf-moq-cmsf-00.txt` - CMAF MoQ Streaming Format (CMSF), defines CMAF-based media packaging for MoQ
