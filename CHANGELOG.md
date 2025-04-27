# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

[Unreleased]: https://github.com/Eyevinn/mp2ts-tools/releases/tag/v0.1.0...HEAD
[0.1.0]: https://github.com/Eyevinn/mp2ts-tools/releases/tag/v0.1.0
