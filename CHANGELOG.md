# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.7.0] - 2026-04-12

MoQ Transport draft-16 support and [moq-interop-runner](https://github.com/englishm/moq-interop-runner) preparation.

### Added

- MoQ Transport draft-16 support via moqtransport v0.7.0
  - ALPN-based version negotiation (`moqt-16` for draft-16, `moq-00` for draft-14)
  - Delta-encoded parameters and version-aware message formats
  - WebTransport protocol negotiation via `WT-Available-Protocols` / `WT-Protocol` headers (per draft-16 Section 3.1)
- `mlmtest` interop test client for [moq-interop-runner][interop-runner]
  - 6 test cases: setup-only, announce-only, publish-namespace-done, subscribe-error, announce-subscribe, subscribe-before-announce
  - TAP v14 output, dual draft-14/16 support via `-draft` flag and `DRAFT` env var
  - `Dockerfile.mlmtest` and GitHub Actions workflow for GHCR publishing
- `-draft` flag in mlmsub for draft-14/16 selection
- Interop namespace `["moq-test", "interop"]` in mlmpub: accepts ANNOUNCE and SUBSCRIBE from clients, and announces it to subscribers
- Integration tests for mlmtest (all 6 test cases with both draft-14 and draft-16)
- Unit tests for pub package (interop namespace helpers)

### Changed

- Bumped moqtransport to v0.7.0 (draft-16 wire format)
- mlmpub advertises both `moqt-16` and `moq-00` ALPNs for raw QUIC
- mlmpub WebTransport server advertises `ApplicationProtocols: ["moqt-16", "moq-00"]`
- mlmsub WebTransport dialer passes ALPN based on `-draft` flag

## [0.6.1] - 2026-04-11

### Added

- Multi-namespace support: `cmsf/clear` (always), `cmsf/drm-{scheme}` (CPIX), `cmsf/eccp-{scheme}` (ClearKey)
- Independent encryption for DRM and ECCP tracks with separate keys (`_drm` and `_eccp` suffixes)
- `-laurl` flag for external ClearKey license URL (for reverse proxy deployments)
- `-sideport` flag replacing `-fingerprintport`, serving both `/fingerprint` and `/clearkey`

### Changed

- Namespace prefix changed from `moqlivemock` to `cmsf/clear` (and `cmsf/drm-*`, `cmsf/eccp-*`)
- Video tracks sorted AVC before HEVC for Widevine compatibility in Chrome
- Protected track suffix changed from `_protected` to `_drm` (commercial DRM) and `_eccp` (ClearKey)
- Default namespace in mlmsub changed to `cmsf/clear`
- `ParseCENCflags` now takes a license URL string instead of a port number

## [0.6.0] - 2026-04-11

Full [MOQ Transport draft-14][moqt-d14] compliance release.

### Added

- FETCH support for catalog retrieval as an alternative to SUBSCRIBE (`-fetchcatalog` flag in mlmsub)
- Configurable MoQ namespace via `-namespace` flag in mlmsub
- Default port (443) when no port is specified in mlmsub address
- Deterministic integration tests using in-memory transport and `synctest` (catalog, video, audio, subtitles, muxing)
- `internal/pub` package with exported publisher handler logic
- `internal/sub` package with exported subscriber handler, CMAF muxer, and DRM decryption logic
- `build-linux` Makefile target for cross-compiling mlmpub to linux/amd64
- CENC encryption support (`cenc` and `cbcs` schemes) for video and audio tracks
- ClearKey DRM with key/IV via CLI flags (`-kid`, `-cenckey`, `-iv`, `-scheme`)
- Commercial DRM support via CPIX config file (`-drmpath`), including Widevine and FairPlay
- DRM information included in the MSF/CMSF catalog

### Fixed

- Object ID delta encoding in subgroup streams per draft-14 spec (moqtransport v0.6.2)
- Inverted Unannounce condition that returned error for known namespaces (moqtransport v0.6.3)
- Safari 26.4 WebTransport support by adding newer SETTINGS codepoints
  ([warp-player#88](https://github.com/Eyevinn/warp-player/issues/88))

### Changed

- Use `role` instead of `mimeType` in catalog per CMSF/MSF spec
- Refactored `cmd/mlmpub` and `cmd/mlmsub` into thin wrappers over `internal/pub` and `internal/sub`
- Bumped Go version to 1.25
- Bumped moqtransport to v0.6.3
- Publisher goroutines now use proper context propagation instead of `context.TODO()`
- Switched video sample descriptors from `avc3`/`hev1` to `avc1`/`hvc1` to support
  FairPlay streaming in Safari 26.4+. With `avc1`/`hvc1`, parameter sets (SPS/PPS
  for AVC, VPS/SPS/PPS for HEVC) are stored in the init segment rather than
  inlined in each sample
- CI: added coverage workflow, updated Go to 1.25, aligned workflows with hi264

## [0.5.0] - 2026-01-27

### Changed

- Include SEI NAL units in AVC output
- Renamed asset files to include codec suffix (`video_*_avc.mp4`, `audio_*_aac.mp4`)
- Renamed `utils/videogen` to `utils/contentgen`
- Audio generation moved to shell scripts (`gen_audio_monotonic.sh`, `gen_audio_scale.sh`)
- Improved audio levels for monotonic and scale content (0.5s beeps with fadeout)
- Default track selection in mlmsub now prefers AVC video and AAC audio (lowest bitrate)
- Catalog aligned with [MSF/CMSF draft-00][msf-00]

### Added

- HEVC support including converted test content
- Time-aligned subtitle tracks in `stpp` and `wvtt` format
  - Listed in catalog and generated by `mlmpub`
  - Can be parsed and written to file by `mlmsub`
- `-catalogout` option in mlmsub to write received catalog JSON to file
  - Supports appending multiple catalog updates to the same file
- Opus audio codec support (CMAF packaging)
  - Bundled Opus test content in `assets/test10s`
- AC-3 and E-AC-3 (EC-3) audio codec support

### Fixed

- Track selection bug where multiple tracks matching substring caused duplicate init segments

## [0.4.0] - 2026-01-09

### Changed

- Upgraded to MoQ Transport [draft-14][moqt-d14] via [Eyevinn/moqtransport][moqtransport-eyevinn] fork
- Updated handler pattern to use separate `SubscribeHandler` for subscription handling
- Session creation now uses struct initialization with `session.Run(conn)`
- Dependencies now use published Eyevinn fork instead of local path

### Fixed

- RequestID setting in mlmsub (#19)
- MaxRequestID from server in mlmsub

## [0.3.0] - 2025-05-25

### Changed

- Catalog is now based on Github [catalog] of Feb. 28 2025
- Now follows [draft-11 of MoQ Transport][moqt-d11] via [moqtransport][moqtransport] update
- mlmsub now autodetects webtransport from `-addr` argument starting with https://

### Added

- Configuration options for `audiobatch` and `videobatch` to control how many frames should be sent in every MoQ object/CMAF chunk
- systemd service script and helpers for mlmpub
- fingerprint endpoint of mlmpub to be used with WebTransport browser clients like [warp-player[wp]
- Certificate validation and auto-generation for WebTransport-compatible certificates (ECDSA, 14-day validity)

## [0.2.0] - 2025-04-28

### Added

- utils/contentgen to generate test content
- WARP catalog generation and parsing
- wall-clock-synchronized media soursce
- multiplexing received video and audio for direct playback via ffplay
- audio track with monotonic beeps and other track with scale sequence beeps
- track selection based on name substring
- loglevel in mlmsub

### Changed

- configurable qlog destination
- application log to stderr

### Deleted

- The clock namespace and code


## [0.1.0] - 2025-04-15

### Added

- initial version of the repo

[Unreleased]: https://github.com/Eyevinn/moqlivemock/releases/tag/v0.7.0...HEAD
[0.7.0]: https://github.com/Eyevinn/moqlivemock/releases/tag/v0.6.1...v0.7.0
[0.6.1]: https://github.com/Eyevinn/moqlivemock/releases/tag/v0.6.0...v0.6.1
[0.6.0]: https://github.com/Eyevinn/moqlivemock/releases/tag/v0.5.0...v0.6.0
[0.5.0]: https://github.com/Eyevinn/moqlivemock/releases/tag/v0.4.0...v0.5.0
[0.4.0]: https://github.com/Eyevinn/moqlivemock/releases/tag/v0.3.0...v0.4.0
[0.3.0]: https://github.com/Eyevinn/moqlivemock/releases/tag/v0.2.0...v0.3.0
[0.2.0]: https://github.com/Eyevinn/moqlivemock/releases/tag/v0.1.0...v0.2.0
[0.1.0]: https://github.com/Eyevinn/moqlivemock/releases/tag/v0.1.0

[catalog]: https://moq-wg.github.io/warp-streaming-format/draft-ietf-moq-warp.html
[msf-00]: https://datatracker.ietf.org/doc/draft-ietf-moq-msf/00/
[moqt-d11]: https://datatracker.ietf.org/doc/draft-ietf-moq-transport/11/
[moqt-d14]: https://datatracker.ietf.org/doc/draft-ietf-moq-transport/14/
[moqtransport]: https://github.com/Eyevinn/moqtransport
[moqtransport-eyevinn]: https://github.com/Eyevinn/moqtransport
[interop-runner]: https://github.com/englishm/moq-interop-runner
[wp]: https://github.com/Eyevinn/warp-player
