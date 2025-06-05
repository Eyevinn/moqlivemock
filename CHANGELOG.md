# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixes

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

- utils/videogen to generated test content
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

[Unreleased]: https://github.com/Eyevinn/mp2ts-tools/releases/tag/v0.3.0...HEAD
[0.3.0]: https://github.com/Eyevinn/mp2ts-tools/releases/tag/v0.2.0...v0.2.0
[0.2.0]: https://github.com/Eyevinn/mp2ts-tools/releases/tag/v0.1.0...v0.2.0
[0.1.0]: https://github.com/Eyevinn/mp2ts-tools/releases/tag/v0.1.0

[catalog]: https://moq-wg.github.io/warp-streaming-format/draft-ietf-moq-warp.html
[moqt-d11]: https://datatracker.ietf.org/doc/draft-ietf-moq-transport/11/
[moqtransport]: https://github.com/mengelbart/moqtransport
[wp]: https://github.com/Eyevinn/warp-player
