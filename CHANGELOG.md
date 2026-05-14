# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- LOCMAF (Low Overhead CMAF) packaging: a LOC-inspired variant of CMAF that
  encodes only the non-derivable `moof`/`moov` fields as MoQT key-value pairs
  using QUIC varints. The first object of every group is a LOCMAF *full* moof
  (carries every required field); subsequent objects in the group are LOCMAF
  *delta* moofs that only carry the fields that changed since the previous
  moof. Signed fields (composition time offsets, delta differences) use
  zigzag-encoded varints. The catalog `initData` field is the LOCMAF-encoded
  `moov`, so subscribers reconstruct a valid CMAF init segment by decoding
  the LOCMAF fields and merging them into an empty CMAF template
- LOCMAF namespaces in mlmpub: `locmaf/clear` (always), `locmaf/drm-{scheme}`
  (when `-drmpath` is set) and `locmaf/eccp-{scheme}` (when `-kid`/`-iv` are
  set). LOCMAF carries all information needed to reconstruct a valid CMAF
  file and therefore supports both commercial DRM (CPIX) and ClearKey/ECCP
- LOCMAF subscriber support in mlmsub: decodes full and delta moofs against
  the LOCMAF-encoded `moov` from the catalog and rewrites a standard CMAF
  fragment for the mux/video/audio outputs
- `cmd/locmaf` test asset generator: writes a CMAF init segment, the
  matching LOCMAF init, and two LOCMAF objects (full moof + delta moof) so
  other LOCMAF implementations can test against a reference asset. Output is
  encrypted (cbcs) to exercise the encrypted-field path. Input and output
  paths are configurable via `-input` / `-out`
- `cmd/locmaf roundtrip` subcommand: encodes a fragmented MP4 through the
  LOCMAF encoder/decoder, verifies sample-level fidelity (mdat byte-equal
  and matching size / duration / effective flags / composition-time offset
  / decode time per ISO 14496-12 §8.8.8.2), and prints wire-overhead
  statistics for the init segment and per-moof. Useful for validating
  LOCMAF compression and fidelity against arbitrary fMP4 inputs
- `MoofDeltaCompressor` / `MoofDeltaDecompressor` types in `internal` that
  maintain the per-track previous-moof state used to encode and decode
  LOCMAF delta moofs

### Fixed

- CENC IV reuse across CMAF fragments: track per-`ContentTrack` running IV
  and chain it through successive `GenCMAFChunk` calls using mp4ff's new
  `EncryptFragment` return value (mp4ff #499)

### Changed

- Bumped mp4ff to v0.52.0 (new `EncryptFragment` signature returning the
  next IV so callers can avoid IV reuse on cenc)

## [0.8.0] - 2026-05-05

### Added

- `-discover` flag in mlmsub to list announced namespaces on a relay and exit
- `-accept-any` flag in mlmsub to accept any announced namespace (for connecting to external relays)
- `-catalog-track` flag in mlmsub to configure catalog track name (e.g. `catalog.json` for moq-dev hang format)
- Multi-element namespace support in mlmsub `-namespace` flag (space-separated, e.g. `"demo bbb"`)
- Raw catalog payload logging on parse failure for debugging non-CMSF catalog formats
- LOC namespace `msf/clear` in mlmpub: MSF catalog with `packaging=loc` per
  [draft-mzanaty-moq-loc][loc], publishing AVC video (length-prefixed NALUs,
  SPS/PPS prepended to IDR frames) and AAC/Opus audio as raw codec bitstream.
  Each LOC object carries a `Timestamp` (ID 0x06) extension header in
  microseconds since the Unix epoch
- LOC subscriber support in mlmsub: reframes AVC (length-prefixed NALUs →
  AnnexB) and AAC (raw frames → ADTS) for direct playback with ffplay.
  Only AAC-LC (`mp4a.40.2`) is supported
- moq-mi namespace `moq-mi/clear` in mlmpub: catalogless publishing per
  [draft-cenzano-moq-media-interop][moq-mi] with fixed track names
  `video0` (AVC) and `audio0` (AAC-LC preferred, Opus fallback). Each object
  carries moqmi extension headers with codec metadata and (for video) the
  AVCDecoderConfigurationRecord on the first object of each GOP
- moq-mi subscriber support in mlmsub: subscribes to fixed track names,
  parses moqmi extension headers, logs per-object metadata, and writes raw
  payloads through unchanged
- HEVC support for LOC packaging (`msf/clear` namespace) with `hev1.*`
  codec prefix per draft-ietf-moq-loc-02 §2.1.1; in-band VPS+SPS+PPS are
  length-prefixed and prepended to every IRAP frame via
  `HEVCData.GenLOCVideoConfig`
- Accurate per-packaging catalog bitrate that reflects actual wire
  footprint, differentiating clear CMAF, encrypted CMAF (cenc/cbcs), and
  LOC. `calcCmafBitrate` measures real chunk size via `GenCMAFChunk` for
  the track's batch / encryption / subsample configuration; `calcLOCBitrate`
  accounts for VPS/SPS/PPS prepended to IRAP frames and the LOC Timestamp
  extension. Both add a per-object MoQ wire-overhead constant
  (`cmafObjectOverheadBytes = 8`) modelling ObjectID, payload-length,
  extension-count and status varints per draft-ietf-moq-transport-16
- `CalcSample` exported on `ContentTrack` (previously unexported)
- `GenAVCDecoderConfigurationRecord` and `GenLOCVideoConfig` on `AVCData`
- `GenLOCVideoConfig` on `HEVCData`
- `SampleRate` / `ChannelConfig` accessors on `AACData` and `OpusData`
- Unit tests covering LOC/moq-mi writers, namespace detection, moqmi track
  map building, LOC catalog generation (AVC + HEVC), AVC/AAC/Opus metadata
  helpers, and per-packaging bitrate accuracy

### Changed

- Bumped moqtransport to v0.8.1 (moqmi extension header helpers)
- LOC AAC writer now uses `mp4ff/aac.NewADTSHeader` instead of a local
  implementation
- `GenCMAFChunk` enables mp4ff `OptimizeTrun`, promoting constant
  per-sample fields (duration, flags) into `tfhd` defaults so the `trun`
  only carries what actually varies. Per-sample audio overhead drops from
  16 B to 4 B (the variable `sample-size`); CBR codecs with constant
  sample size can drop to 0 B
- Default `-audiobatch` reduced from 2 to 1 so CMAF audio chunking matches
  LOC (one frame per object), making per-packaging wire-cost differences
  directly comparable (e.g. AAC LOC ~+4 %, CMAF clear ~+33 %,
  CMAF cenc ~+54 %)
- mlmsub WebTransport client now advertises
  `SETTINGS_WEBTRANSPORT_MAX_SESSIONS=1` (0xc671706a) on its HTTP/3 SETTINGS
  frame so it can negotiate WT against deployed `web-transport-quinn`-backed
  relays (Cloudflare's MoQ interop relay, Luke Curley's `cdn.moq.dev`,
  Lorenzo Miniero's imquic relay) that require this on the client side.
  Pulled in via a `replace` directive pointing at the patched
  `github.com/Eyevinn/webtransport-go` (branch `feat/additional-settings`);
  the replace can be dropped once an equivalent `AdditionalSettings` field
  is upstreamed in `quic-go/webtransport-go`

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

[Unreleased]: https://github.com/Eyevinn/moqlivemock/releases/tag/v0.8.0...HEAD
[0.8.0]: https://github.com/Eyevinn/moqlivemock/releases/tag/v0.7.0...v0.8.0
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
[loc]: https://datatracker.ietf.org/doc/html/draft-mzanaty-moq-loc
[moq-mi]: https://datatracker.ietf.org/doc/html/draft-cenzano-moq-media-interop
[moqt-d11]: https://datatracker.ietf.org/doc/draft-ietf-moq-transport/11/
[moqt-d14]: https://datatracker.ietf.org/doc/draft-ietf-moq-transport/14/
[moqtransport]: https://github.com/Eyevinn/moqtransport
[moqtransport-eyevinn]: https://github.com/Eyevinn/moqtransport
[interop-runner]: https://github.com/englishm/moq-interop-runner
[wp]: https://github.com/Eyevinn/warp-player
