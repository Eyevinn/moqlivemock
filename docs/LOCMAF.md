# LOCMAF in moqlivemock

LOCMAF (Low Overhead CMAF for Media over QUIC) is specified by the
IETF Internet-Draft
[draft-einarsson-moq-locmaf](https://datatracker.ietf.org/doc/draft-einarsson-moq-locmaf/),
which is the normative reference. The codec lives in the reusable Go
module [github.com/Eyevinn/locmaf](https://github.com/Eyevinn/locmaf),
together with the `locmaf` CLI (round-trip aligner, golden-vector
corpus) and the locmaf.dev explainer site.

moqlivemock implements the draft's current packaging version
(`locmafVersion` as reported by `locmaf.Version`):

- **mlmpub** encodes each CMSF rendition as both a `cmaf` track and a
  `locmaf` track (`<name>_locmaf`) sharing one `initDataList` entry,
  using `locmaf.EncodeCanonical` per chunk with one in-group state per
  MoQ group (`internal/asset.go`, `internal/moqgroup.go`).
- **mlmsub** expands received LOCMAF Objects back to CMAF chunks with
  `locmaf.Decode` + `locmaf.ReconstructCanonical`
  (`internal/sub/sub.go`) and rejects tracks whose `locmafVersion` it
  does not implement.
- DRM (cbcs and cenc) round-trips through the packaging: the
  per-sample `senc` metadata rides as header fields and `saiz`/`saio`
  are regenerated on reconstruction.

Earlier in-repo documents describing LOCMAF v0.1 and v0.2 were removed
when the specification moved to the Internet-Draft; they remain
available in the git history (and v0.2 at the `locmaf-v0.2` tag).
