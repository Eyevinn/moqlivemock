# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with this repository.

## Project Overview

moqlivemock is a Go-based MoQ (Media over QUIC) live streaming mock implementation. It provides:

- **mlmpub**: A publisher that serves live media content over MoQ Transport
- **mlmsub**: A subscriber that receives and processes media streams

## Architecture

### LOCMAF Codec Module

- LOCMAF encode/decode comes from `github.com/Eyevinn/locmaf` (the reusable
  codec module; one packaging version at a time, reported by `locmaf.Version`).
  See `docs/LOCMAF.md`; the normative spec is the IETF draft
  draft-einarsson-moq-locmaf

### MoQ Transport Dependency

Uses `github.com/Eyevinn/moqtransport`, on **draft-18**. Drafts 14 and 16 are
gone: draft-17 changed the varint encoding, moved SETUP onto a pair of
unidirectional streams and gave every request its own bidirectional stream, so
nothing of the older wire format survives.

The negotiated protocol identifier is the whole of version negotiation from
draft-17 on — SETUP carries no version field — so `moqtransport.SupportedALPNs()`
is the single source for both the QUIC ALPN list and the WebTransport
subprotocol list. Do not hard-code `moqt-18` anywhere.

Consequences worth knowing:

- There is no UNSUBSCRIBE, UNANNOUNCE or ANNOUNCE_CANCEL. A request lasts as
  long as its stream, and ending that stream is the message. A publisher learns
  a subscriber has gone from `Subscription.Context()` being cancelled.
- Extension headers are now Object Properties, with a registry and per-property
  scope. The LOC Timestamp property is **0x0A** (draft-ietf-moq-loc-03), not
  0x06: MOQT allocates 0x06 to SUBGROUP_DELIVERY_TIMEOUT, which is Track scope
  only, so a 0x06 Object Property makes the track malformed.
- The PROPERTIES bit lives in the subgroup header and covers every Object on
  the stream, so a subgroup carrying properties must be opened with
  `moqtransport.WithObjectProperties()`.

While the transport's draft-18 branch is in flight, `go.mod` has a
`replace github.com/Eyevinn/moqtransport => ../moqtransport`. That must not
reach main.

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
- `msf/clear` — LOC packaging (AVC, HEVC, and AV1 video + AAC/Opus audio)
- `moq-mi/clear` — moq-mi packaging with fixed track names `video0` / `audio0`

### Subtitle Tracks

Subtitles are dynamically generated (not loaded from files), showing UTC time
and group number, as WVTT (WebVTT in CMAF) or STPP (TTML in CMAF).

Track naming: `subs_wvtt_{lang}`, `subs_stpp_{lang}`

### Content Protection

ClearKey/ECCP (explicit `-kid`/`-iv` flags) and commercial DRM (a CPIX config
via `-drmpath`) are two independent modes: both are optional and both can be
active at the same time, in which case a rendition is published three times.

Track naming: clear tracks have no suffix, DRM tracks get `_drm`, ECCP tracks get `_eccp`.

### Video Codecs

Video tracks use `avc1` (H.264), `hvc1` (HEVC), and `av01` (AV1) sample
descriptors. AVC/HEVC store parameter sets (SPS/PPS for AVC, VPS/SPS/PPS for
HEVC) in the init segment rather than inlining them in each sample; this is
required for FairPlay DRM compatibility in Safari 26.4+. AV1 similarly carries
its decoder configuration (the sequence header OBU) in the `av1C` box of the
init segment (`internal/media.go` `AV1Data`, built via mp4ff's
`SetAV1Descriptor`). AV1 is offered through the CMSF namespaces (CMAF + LOCMAF variants) and the LOC
`msf/clear` namespace. For LOC the `av01` codec string is kept as-is (unlike
AVC/HEVC which switch to `avc3`/`hev1`) because the sequence header OBU already
travels in each keyframe temporal unit; `AV1Data.GenLOCVideoConfig` returns nil
when keyframes are self-contained and the sequence header OBU to prepend
otherwise. AV1 test assets are generated by `utils/contentgen` in low-delay CBR
mode (I/P only, no reordering).
Encryption works for AV1 too — the protection path is codec-agnostic and mp4ff
implements the AV1 CENC binding (only tile data encrypted, OBU headers clear),
so ClearKey/ECCP (`cenc`/`cbcs`) and commercial DRM all produce `_eccp`/`_drm`
AV1 tracks that round-trip correctly.

### Interop Testing (mlmtest)

`mlmtest` is an interop test client for [moq-interop-runner](https://github.com/englishm/moq-interop-runner).
It connects to a server/relay and runs protocol-level test cases against every
draft the build speaks, outputting TAP v14 results.
`go run ./cmd/mlmtest -l` lists the test cases.

moq-interop-runner drives it through environment variables rather than flags:

```bash
RELAY_URL=moqt://relay:443 TESTCASE=setup-only TLS_DISABLE_VERIFY=1 go run ./cmd/mlmtest
```

