<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="assets/logo-dark.svg">
    <source media="(prefers-color-scheme: light)" srcset="assets/logo-light.svg">
    <img alt="LOCMAF — Low Overhead CMAF for MOQ" src="assets/logo-light.svg" width="640">
  </picture>
</p>

# LOCMAF v0.2 — implementation notes

The normative LOCMAF v0.2 specification is the IETF Internet-Draft
**[draft-einarsson-moq-locmaf](https://datatracker.ietf.org/doc/draft-einarsson-moq-locmaf/)**
("Low Overhead CMAF for Media over QUIC (LOCMAF)"). The wire format —
field IDs, varint and zigzag encodings, full/delta `moof` property
blocks, the `senc`/`saio`/`saiz` round-trip — is defined there and that
document is authoritative.

This file previously held the v0.2 working draft. That text has moved to
the Internet-Draft; the full working-draft history remains in this
repository's git log. What stays here is only the part that does not
belong in an IETF draft: how **moqlivemock** implements v0.2 today.

The frozen v0.1 specification at [LOCMAF.md](LOCMAF.md) remains
authoritative for deployed v0.1 (`locmafVersion: "0.1"`) implementations.

## Versioning

The LOCMAF wire-format version and the IETF draft revision advance
independently:

- The **wire-format version** (`locmafVersion`, currently `"0.2"`)
  follows the LOCMAF specification and is what receivers compare against.
- The **draft revision** (`-00`, `-01`, …) follows IETF submission
  cadence. Revision `-00` carries specification version 0.2.

Cite the version-independent draft URL above rather than a pinned
revision, so references do not go stale across submissions.

## Implementation status in moqlivemock

The v0.2 codec lives in `internal/locmafv02` and runs side-by-side with
the v0.1 codec (`internal/locmaf*.go`), one per namespace. The publisher
announces `locmaf-v0.2/clear`, `locmaf-v0.2/drm-{scheme}`, and
`locmaf-v0.2/eccp-{scheme}`; the wire version is tracked by
`locmafv02.Version` (`"0.2"`).

**Implemented and interop-tested:**

- Full and delta `moof` property blocks.
- Signed composition time offsets carried as zigzag varints in both
  chunk kinds (`trun` version 1 — the common CMAF / B-frame case).
- The cenc `senc` / `saio` / `saiz` round-trip for both `cbcs` and
  `cenc`, validated by `TestLocmafV02EncryptedSencRoundTrip`.
- ClearKey / ECCP decrypt on the mlmsub side
  (`TestDecompressLocmafV02ClearKeyRoundTrip`, audio + video, both
  schemes).
- A raw, uncompressed CMAF Header in the v0.2 catalog (no `moov`
  compression).

**Not yet implemented:**

- `prft`, `styp`, and `emsg` field carriage. mlmpub content carries none
  today; the field IDs are reserved in the wire format.
- A documented DRM-box round-trip table for the `senc` / `saio` / `saiz`
  field-ID assignments.

These are additive — they reserve new field IDs without changing the
encoding of existing ones, so they land under the same `"0.2"` wire
version. Their absence is not a compatibility concern: a receiver simply
never sees those field IDs from content that does not carry them.

## Revision history

| Version    | Date       | Changes |
|------------|------------|---------|
| 0.2        | 2026-06-02 | Specification moved to IETF draft [draft-einarsson-moq-locmaf-00](https://datatracker.ietf.org/doc/draft-einarsson-moq-locmaf/). This file reduced to implementation notes. |
| 0.1        | 2026-05-17 | Initial release. See [LOCMAF.md](LOCMAF.md). |
