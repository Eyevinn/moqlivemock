# LOCMAF: Low Overhead CMAF for MOQ

LOCMAF is a way to stream low-latency CMAF over [MOQT][MOQT] with an
overhead comparable to [LOC][LOC]. It is intended as a
packaging format in [CMSF][CMSF] and was developed as part of
the Master Thesis work by Hugo Björs under the supervision of Torbjörn
Einarsson at Eyevinn 2026.

A central motivation is **DRM-protected live streaming**. LOC carries
raw codec frames and cannot transport the per-sample encryption
metadata (CENC `senc` IVs, subsample maps, `tenc` defaults) that the
browser EME / CDM pipeline needs to decrypt. LOCMAF preserves the
CMAF structure that carries this metadata, so a CMAF-protected source
survives the LOCMAF round-trip and decrypts in MSE / EME with the
standard Widevine / PlayReady / FairPlay / ClearKey systems. The CMSF
catalog signals DRM exactly as it does for plain CMAF
(`contentProtections` array + per-track `contentProtectionRefIDs`).
See [DRM with LOCMAF](#drm-with-locmaf) for the pipeline and the
measured cenc / cbcs wire-cost impact.

LOCMAF provides a way to reconstruct full CMAF headers, segments, and chunks
which are semantically equivalent to the sender side input.
They may differ in some syntactic details and total size, but all samples
and associated metadata are identical.

LOCMAF has been implemented by Hugo in a working MOQ server and client
available publicly at [moqlivemock.demo.osaas.io][mlm-demo] and on
GitHub in the two projects [Eyevinn/moqlivemock][moqlivemock] and
[Eyevinn/warp-player][warp-player].

This is a first version and may evolve depending on feedback and experience.

## Background

One reason that [LOC (Low Overhead Container)][LOC] was developed in
connection to [MOQ Transport][MOQT] is that
CMAF has a very high overhead for transporting individual
data samples, such as video or audio frames.

The minimal CMAF header for a single sample chunk is
more than 100B (bytes), while LOC is typically 9B.
In both cases there is an MOQT Object header of ~4B size.

However, consecutive CMAF chunk headers are almost identical,
so it should be possible to predict them from previous headers
That would result in a compact wire representation
of a series of CMAF chunks that can be translated back into
full CMAF chunks on the receiving end and fed into an MSE/EME player.

LOCMAF (Low Overhead CMAF) is such a compact wire
representation of CMAF media fragments designed for streaming over MOQT
that works well together with the [CMSF] catalog.
It targets the same low-latency workload as Low-Latency HLS
(LL-HLS) Parts and DASH Chunked CMAF — short segments built from CMAF chunks
that, at the limit, contain a single sample — but extends the same idea further
by exploiting the CMSF catalog side-channel and the predictability of consecutive
`moof` boxes.

LOCMAF can also be used to compress CMAF init segments (aka CMAF headers),
but since these are only sent once, the gain is smaller. In that
limited context of compressing one CMAF file, the general ISOBMFF/MP4
compression proposed by Luke Curley in his
[Compressed MP4][compressed-mp4] Internet Draft is an alternative.

This document explains how LOCMAF achieves its very high compression
ratio, the structural assumptions it makes about how MoQ groups are
laid out, and how the format is positioned to evolve.

## How MoQ groups map to CMAF

In LL-HLS Parts and DASH Chunked CMAF, each segment is a sequence of CMAF
chunks (one `moof` + one `mdat` per chunk). The chunk duration determines
the glass-to-glass latency floor, but a shorter duration produces a
higher total chunk overhead — which is exactly the cost LOCMAF is built to
remove.

The natural mapping to MoQ Transport is:

- **One MoQ group per CMAF segment.** Group boundaries align with random
  access points (RAPs), so a new subscriber can tune in at the start of any
  group and decode immediately. For video, every group starts on an IDR
  (an independent part in LL-HLS terms).
- **One MoQ object per CMAF chunk.** Each object carries one `moof` + `mdat`
  pair. At the extreme (sample-level fragmentation), every video frame and
  every audio frame is one MoQ object.
- **Audio groups aligned to video groups.** Most audio codecs (AAC, Opus,
  AC-3, AC-4) have a RAP on essentially every frame, so audio doesn't need
  alignment for tune-in. But tune-in is a *joint* operation: when a
  subscriber starts a video group at wall-clock T, it also wants audio
  starting at T so the two can be muxed and synchronised. We therefore
  target the case where audio MOQ groups have the same duration as the video
  ones.

We want each group to be independent, but inside each group we assume
that all objects are delivered in order. The main target of LOCMAF
is to compress the series of moof boxes inside a group.

## CMSF catalog signalling: `locmafVersion`

While the LOCMAF wire format is still evolving, the CMSF catalog Track
entry carries an explicit version string in a field named
`locmafVersion`. The current value is **`"0.1"`**.

```json
{
  "name": "video_400kbps_avc",
  "packaging": "locmaf",
  "locmafVersion": "0.1",
  "initData": "...",
  "...": "..."
}
```

The field is present only when `packaging == "locmaf"` and is omitted
otherwise. Receivers should compare against their highest supported
version and fall back to a non-LOCMAF packaging (or refuse the track)
when the encoder is ahead.

The reason for an explicit catalog-level version is that the header-ID
space + skip-unknown rule only handles *additive* new top-level object
kinds. Behavioural changes inside existing kinds — like the absolute
`moofBaseMediaDecodeTime` override that was added to the delta moof —
would silently mis-decode at an older receiver, because the same field
ID is being reinterpreted. The version field lets a receiver detect
that case before it starts reading wire bytes.

When the format stabilises the version can be frozen at `"1.0"` and
treated as informational; further evolution falls back to the header-ID
space as the additive extensibility mechanism. If there is interest in
LOCMAF beyond the current implementations, it would make sense to
publish the wire format and the catalog signalling as an IETF Internet
Draft so that the IDs and version semantics become a coordinated spec
rather than an in-tree convention.

## Where LOCMAF wins: the moof delta stream

A typical `moof` box for N samples of unencrypted content is
around `100 + 4*N` bytes in size, meaning `500B` for 100 samples,
but `104B` for 1 sample, varying in overhead per sample from 5B to 104B.
After the moof there is a 4B length and 4B `mdat` box type that add 8B
to the overhead.

In the moqlivemock test content, we have 1s segments/groups of 25 video frames and 46 or 47
audio frames. The typical overhead from encoding the moofs for audio is therefore around 290B
if grouping all audio frames into one object, but 4800B if sending them individually. The latter
should be compared to 420B when using LOC.

However, due to to the similarity of all the values in the moof boxes
we can transfer the CMAF information much more efficiently.


Looking more closely, consecutive raw `moof` boxes have very small differences in their boxes' values:

- `tfhd` defaults (sample duration, size, flags) — usually identical across
  every fragment in the group.
- `tfdt.base_media_decode_time` — monotonically advances by the previous
  fragment's duration; derivable from sum of sample durations.
- `trun.samples[i].flags` — same value for every non-IDR sample; differs
  for the first sample of each IDR-containing fragment.
- `trun.samples[i].duration` - typically not sent, but instead uses default value from `trex` or `tfhd`
  (relies on a commensurate media timescale — see "Prerequisite: commensurate media timescales" below).
- `trun.samples[i].composition_time_offset` — only present for video with
  B-frames and is then a small signed integer with a
  repetitive pattern that follows the encoder's B-frame structure.
- `trun.samples[i].size` — the only field that genuinely varies per sample.

LOCMAF exploits this in two stages:

1. **Tfhd against trex defaults.** When `tfhd` carries a value that
   already matches the `trex` default in the moov, LOCMAF omits it.[^cmf2-defaults]
2. **Delta encoding within a group.** The first `moof` object of every
   group is a *full* LOCMAF moof. Each subsequent `moof` is a *delta* moof
   that carries only the fields whose values differ from the previous moof
   in the same group, plus a flag identifying which fields were deleted.
   `base_media_decode_time` is derived implicitly by adding the previous
   moof's total sample duration.

Furthermore, after the `moof`, there is always an `mdat` box, carrying
the actual metadata. That start with an 8B header `size (4B) + mdat`.
In LOCMAF, these 8B are not sent, but the size is derived as
the remaining size of the MOQT object, and the 4B `mdat` is not needed.

Empirically, on sample-level fragmented streams across AVC, HEVC, AAC, Opus,
AC-3, and AVC3 sources with no B-frames:

- Full moof: ~8 B (single-sample group start with codec-specific overhead).
- Delta moof: **2 B in steady state** — just the `MoofDeltaHeader` varint
  plus a zero-length payload varint when no field changed.
- Aggregate moof overhead: **~2.3% of CMAF wire bytes**, i.e. a 45:1
  compression ratio on the `moof` headers themselves.

A small jitter of 4–5 B appears on the delta carrying an IDR change in
the sample-flags array, which is correct: the random-access flag flip is a
real bit of information that must be transmitted.

For *dense* moofs (livesim2-style segments holding 50–375 samples in a
single moof), LOCMAF still removes around 50% of the moof overhead. But the
delta stream's compounding benefit per-fragment is much larger when each
fragment is small and similar to its neighbour — exactly the low-latency
regime.

### Catalog `bitrate` impact on the bundled assets

The CMSF catalog `bitrate` field reports the wire bitrate of the track,
including the relevant per-object framing overhead. On the bundled
`assets/test10s` content at one sample per MoQ object (the default for
`mlmpub`), the LOCMAF and CMAF reports look like this:

| track | sample [bps] | cmaf [bps] | locmaf [bps] | saved [bps] | saved % |
| ----- | -----------: | ---------: | -----------: | ----------: | ------: |
| `audio_monotonic_128kbps_aac`  | 128 001 | 171 501 | 131 887 | 39 614 | 23.1 % |
| `audio_monotonic_128kbps_opus` | 128 400 | 174 800 | 132 536 | 42 264 | 24.2 % |
| `audio_monotonic_192kbps_ac3`  | 192 000 | 221 000 | 194 636 | 26 364 | 11.9 % |
| `video_400kbps_avc`            | 373 200 | 396 400 | 376 488 | 19 912 |  5.0 % |
| `video_400kbps_hevc`           | 299 392 | 322 592 | 302 680 | 19 912 |  6.2 % |
| `video_600kbps_avc`            | 559 505 | 582 705 | 562 793 | 19 912 |  3.4 % |
| `video_600kbps_hevc`           | 408 785 | 431 985 | 412 073 | 19 912 |  4.6 % |
| `video_900kbps_avc`            | 844 504 | 867 704 | 847 792 | 19 912 |  2.3 % |
| `video_900kbps_hevc`           | 610 182 | 633 382 | 613 470 | 19 912 |  3.1 % |

The savings figure is **per object, not per byte of mdat**: 19 912 bps
saved / 8 / 25 fps ≈ 99.6 bytes saved per moof, matching the
"~100 B CMAF moof becomes a 2 B LOCMAF delta moof" headline. The
per-track *percentage* therefore looks smaller on high-bitrate video
(the mdat dominates) and larger on low-bitrate audio (the moof header
is a much bigger fraction of the wire cost). For the 128 kbps AAC track
that the user typically asks about, the catalog bitrate drops from
171.5 kbps (CMAF) to 131.9 kbps (LOCMAF) — a 23% saving against the
CMAF-reported wire bitrate, and only ~3% above the raw sample bitrate
(the remaining LOCMAF wire cost is ~2 B/object × ~47 obj/s ≈ 750 bps
plus 8 B/object MoQ framing).

These numbers come from the `internal.calcLocmafBitrate` measurement
path: one full + one delta LOCMAF chunk generated with a fresh
`MoofDeltaCompressor`, amortised over a 1 s MoQ group. Re-runnable via
`go test ./internal/ -run TestLocmafBitrateIsLowerThanCmaf -v`.

### Scope: CMAF-shaped MP4 only

LOCMAF's "one MoQ object = one `moof` + one `mdat`" wire mapping, and
most of the elisions described above, rely on the structural
restrictions that CMAF (ISO/IEC 23000-19) places on ISOBMFF. LOCMAF
targets the **`cmfc` structural brand** at minimum; the stricter `cmf2`
brand (which §7.7.3 requires for self-decodable fragments) is a
superset — see the `tfhd`-vs-`trex` defaults footnote[^cmf2-defaults].
The specific rules LOCMAF leans on:

- **Exactly one media track** per CMAF Track / CMAF Header
  (§7.3.2: "The MovieBox SHALL contain exactly one track containing
  media data"). LOCMAF therefore encodes a single track per LOCMAF
  stream, with the moov stamped at catalog time and `track_id` dropped
  on the wire — the catalog already names the track.
- **Exactly one `trun`** per `TrackFragmentBox`
  (Table 4 — Boxes Contained in CMAF Fragment; Format Req. = 1). Both
  the delta-moof field list and the implicit-BMDT derivation assume a
  single, total ordering of samples per moof; the moof field IDs 1, 3,
  5, 7 are single per-sample lists rather than per-trun lists.
- **Exactly one `mdat`** per CMAF Chunk
  (§7.3.3.2: "A CMAF Chunk SHALL contain one ISOBMF segment …
  constrained to include one MovieFragmentBox followed by one
  MediaDataBox"). LOCMAF objects map 1:1 to CMAF Chunks at sample-level
  fragmentation, so the `mdat` is the suffix of the MoQ object and its
  8-byte `size + 'mdat'` header is dropped — its length is derived
  from the MoQ object length minus the LOCMAF wrapper.
- **Sample byte offsets are moof-relative**
  (§7.3.2.3 point 5 and §7.5.16: `data-offset-present` SHALL be 1 with
  `data_offset` relative to the start of the `MovieFragmentBox`).
  LOCMAF can therefore reconstruct the offset deterministically and
  never carries `data_offset` over the wire.
- **Encrypted-content `saio` has `entry_count == 1`** (§8.2.2). The
  per-sample auxiliary information for an entire chunk is one
  contiguous block keyed off a single offset, which matches what
  LOCMAF carries: one `moofInitializationVector` (id 9) blob plus one
  `moofSubsampleCount` / `moofBytesOfClearData` / `moofBytesOfProtectedData`
  list per fragment, never multiple groups of auxiliary info.

General fragmented MP4 may contain multiple `traf` / `trun` boxes per
moof, multiple `mdat` boxes per fragment, or multiple tracks multiplexed
into one file (and `mdat` need not be tightly packed). LOCMAF does not
address those layouts directly: source content must be CMAF-conformant
— or trivially repackaged into CMAF — before LOCMAF encoding.

### Prerequisite: commensurate media timescales

The two-byte steady state described above depends on one assumption from
the source side: consecutive `moof` boxes must actually be near-identical
at the field level. The most important precondition is that **every frame
has an exact integer duration in the chosen media timescale**, so the
`trex`/`tfhd` default duration covers them and the delta moof can omit the per-sample duration array entirely. Two canonical examples:

- **48 kHz AAC audio:** use timescale **48 000**, which makes one AAC
  frame exactly **1024** ticks.
- **60000/1001 fps video** (NTSC family): use timescale **60 000**, which
  makes one video frame exactly **1001** ticks.

With a commensurate timescale all sample durations equal a single integer
constant. With a *mis*matched timescale (e.g. a generic 1000-tick-per-second
timescale for 60000/1001 fps video), each frame's duration drifts by ±1
tick to track the fractional rate; the per-sample duration array must
then be sent on every fragment and the 2-byte steady-state delta moof is
no longer achievable.

## Media segment wire format

A LOCMAF media segment is one MoQ object that carries one CMAF chunk —
i.e. one `moof` + one `mdat` pair, encoded as a LOCMAF *object*. Every MoQ
object is self-delimiting (the MoQ Transport layer already provides object
length), so LOCMAF does not need a total-length field; it only needs to
know where the moof properties end and the mdat begins.

### Object framing

```
+-----------------------------+
| header_id        (varint)   |   top-level object kind
+-----------------------------+
| properties_length (varint)  |   length of the properties block in bytes
+-----------------------------+
| properties      (variable)  |   sequence of (field_id, value) tuples
+-----------------------------+
| mdat raw payload (rest)     |   sample data, length = MoQ-object-len - above
+-----------------------------+
```

`header_id` and `properties_length` are variable-length integers. This
implementation uses the same varint as MoQ Transport draft-16.

MoQ Transport draft-17 and later replace the QUIC varint with a different
variable-length integer encoding. A LOCMAF implementation that runs over
draft-17+ MoQT MUST use the draft-17 varint instead, so the same wire
format applies consistently across the MoQT control and data planes for a
given session. All wire-cost numbers in this document assume the
draft-16 / RFC 9000 varint.

The mdat payload is the *contents* of the CMAF `mdat` box — the sample
data, without the surrounding `size + 'mdat'` box header. The receiver
reconstructs a standard `mdat` box by wrapping these bytes in an 8-byte
ISO BMFF header.

### Top-level object IDs

| ID | Symbol            | Object kind                              |
| -- | ----------------- | ---------------------------------------- |
|  21 | `MoovHeader`      | LOCMAF moov (init data in the catalog)   |
|  23 | `MoofHeader`      | full moof + mdat                         |
|  25 | `MoofDeltaHeader` | delta moof + mdat                        |

The numeric IDs **start at 21** so they do not collide with LOC's already
defined property IDs (1–16 in draft-ietf-moq-loc-02). A LOCMAF object can
therefore live alongside other LOC properties in a MoQ object's
property/payload split: the LOCMAF init / full moof / delta moof object
occupies an LOC *private* property slot (carried in the MoQ object
payload), and other LOC public properties (e.g. Timestamp, Timescale,
Video Frame Marking) ride in the MoQ object's properties field as usual.
Storing LOCMAF as a public property is also allowed by the spec but is
not the default.

Receivers MUST skip (and log) unrecognised `header_id` values rather than
abort. The `properties_length` lets a skipper advance past the properties
block, and the MoQ object length terminates the unknown object cleanly.
(See `MoofDeltaDecompressor.DecompressMoof` for the reference
implementation.) This makes it possible to later add extra boxes such
as `prft`.

### Properties: field-tuple encoding

The properties block is a flat sequence of `(field_id, value)` tuples.
Field IDs are also MOQT varints, and the value encoding is determined by
the **parity of the ID**:

- **Even ID → scalar varint.** The value is a single MOQT varint. Length
  is self-describing (1, 2, 4 or 8 bytes). No length prefix.
- **Odd ID → length-prefixed bytes.** The tuple is `field_id | value_length
  | value_bytes`, all three being varints / variable-length bytes.

Field IDs may appear in any order; receivers MUST tolerate any ordering.
The reference encoder emits IDs in ascending order so the wire bytes are
deterministic.

### Moof field reference

All fields used in `MoofHeader` and `MoofDeltaHeader` objects. The "Source"
column names the ISO BMFF source field; "Kind" indicates how the value is
encoded on the wire.

| ID | Symbol                                | Source ISO BMFF field                    | Kind        | Notes                                    |
| -- | ------------------------------------- | ---------------------------------------- | ----------- | ---------------------------------------- |
|  1 | `moofSampleSizes`                     | `trun.samples[i].sample_size`            | varint list | one entry per sample                     |
|  2 | `moofSampleDescriptionIndex`          | `tfhd.sample_description_index`          | scalar      |                                          |
|  3 | `moofSampleDurations`                 | `trun.samples[i].sample_duration`        | varint list | one entry per sample                     |
|  4 | `moofDefaultSampleDuration`           | `tfhd.default_sample_duration`           | scalar      |                                          |
|  5 | `moofSampleCompositionTimeOffsets`    | `trun.samples[i].sample_composition_time_offset` | signed varint list | zigzag-encoded; signed in source |
|  6 | `moofDefaultSampleSize`               | `tfhd.default_sample_size`               | scalar      |                                          |
|  7 | `moofSampleFlags`                     | `trun.samples[i].sample_flags`           | varint list | one entry per sample, 32-bit `sample_flags` packed as uint32 |
|  8 | `moofDefaultSampleFlags`              | `tfhd.default_sample_flags`              | scalar      |                                          |
|  9 | `moofInitializationVector`            | `senc.samples[i].InitializationVector`   | raw bytes   | concatenated IVs, length = `per_sample_iv_size × sample_count` |
| 10 | `moofBaseMediaDecodeTime`             | `tfdt.base_media_decode_time`            | scalar      | always in full moof; in delta moof only when the actual BMDT diverges from the derived value (discontinuity / re-anchor) |
| 11 | `moofSubsampleCount`                  | `senc.samples[i].subsample_count`        | varint list | one entry per sample                     |
| 12 | `moofFirstSampleFlags`                | `trun.first_sample_flags`                | scalar      | only if `tr_flags.first_sample_flags_present` |
| 13 | `moofBytesOfClearData`                | `senc.samples[i].subsamples[j].BytesOfClearData` | varint list | flat sequence across all sub-samples |
| 14 | `moofSampleCount`                     | `trun.sample_count`                      | scalar      |                                          |
| 15 | `moofBytesOfProtectedData`            | `senc.samples[i].subsamples[j].BytesOfProtectedData` | varint list | flat sequence across all sub-samples |
| 16 | `moofPerSampleIVSize`                 | `senc.per_sample_iv_size`                | scalar      | only when it overrides `tenc.default_per_sample_iv_size` |
| 17 | `moofDeltaDeletedLocmafIDs`           | —                                        | varint list | delta-moof only; IDs of fields removed since previous moof |

The ID space is kept structurally aligned with the parity rule: every
"default" / "scalar" field has an even ID, and every per-sample list field
has an odd ID. The one exception is `moofInitializationVector` (ID 9, odd
but raw bytes rather than a varint list), because IVs are uniformly-sized
opaque byte runs.

### Full moof: when each field is emitted

The encoder produces a full moof by walking the source `moof`/`moov` pair
and emitting only the fields whose values are NOT derivable from the
catalog's moov (the trex defaults). The exact rules:

| Field                              | Emitted when                                                                                            |
| ---------------------------------- | ------------------------------------------------------------------------------------------------------- |
| `moofSampleDescriptionIndex`       | `tfhd.HasSampleDescriptionIndex()` AND value ≠ `trex.default_sample_description_index`                  |
| `moofDefaultSampleDuration`        | `tfhd.HasDefaultSampleDuration()` AND value ≠ `trex.default_sample_duration`                            |
| `moofDefaultSampleSize`            | `tfhd.HasDefaultSampleSize()` AND value ≠ `trex.default_sample_size` AND `sample_count > 1`             |
| `moofDefaultSampleFlags`           | `tfhd.HasDefaultSampleFlags()` AND value ≠ `trex.default_sample_flags`                                  |
| `moofBaseMediaDecodeTime`          | always                                                                                                  |
| `moofSampleCount`                  | always                                                                                                  |
| `moofFirstSampleFlags`             | `trun.HasFirstSampleFlags()`                                                                            |
| `moofSampleSizes`                  | `trun.HasSampleSize()` AND `sample_count > 1`                                                           |
| `moofSampleDurations`              | `trun.HasSampleDuration()`                                                                              |
| `moofSampleCompositionTimeOffsets` | `trun.HasSampleCompositionTimeOffset()`                                                                 |
| `moofSampleFlags`                  | `trun.HasSampleFlags()`                                                                                 |
| `moofPerSampleIVSize`              | senc present AND `per_sample_iv_size ≠ tenc.default_per_sample_iv_size`                                 |
| `moofInitializationVector`         | senc present AND `per_sample_iv_size > 0` AND `len(senc.IVs) > 0`                                       |
| `moofSubsampleCount`               | senc present AND `len(senc.subsamples) > 0`                                                             |
| `moofBytesOfClearData`             | same as `moofSubsampleCount`                                                                            |
| `moofBytesOfProtectedData`         | same as `moofSubsampleCount`                                                                            |

The `moofSampleSizes` omission rule for single-sample fragments deserves
note: when `sample_count == 1` and no per-sample size is sent, the receiver
infers the single sample's size from the MoQ object's mdat-payload length
(MoQ-object-length minus the object framing already consumed). This saves
a few bytes per fragment in the sample-level fragmentation regime, which is
the common case.

### Delta moof: incremental encoding

A delta moof carries only the fields whose effective values differ from
the previous moof in the same group. Three rules govern the payload:

1. **`moofBaseMediaDecodeTime` (ID 10) is normally derived, not emitted.**
   The receiver computes the new BMDT as
   `previous_bmdt + sum(previous_sample_durations)`, where
   `sample_durations` are taken from either the previous moof's
   `moofSampleDurations` list (if present) or the previous moof's
   effective `default_sample_duration` × `sample_count`.

   When the actual BMDT in the source diverges from the derived value
   (audio pre-roll, splicing, stream-tear / re-anchor), the encoder
   **emits ID 10 as an absolute value** in the delta moof to override
   the derivation. The receiver checks for the field first and uses its
   value when present; otherwise it falls back to the derivation. The
   absolute override is encoded the same way as in a full moof — a
   plain unsigned varint, not a zigzag delta.

2. **Each field value is a *signed delta* of its previous representation**,
   keyed by `moofDeltaValueKind(id)`:

   | Kind             | When                                  | Encoding on wire                                                                  | Reconstruction                                                            |
   | ---------------- | ------------------------------------- | --------------------------------------------------------------------------------- | ------------------------------------------------------------------------- |
   | scalar           | even ID                               | single zigzag-encoded signed varint = `current_value − previous_value`            | `current_value = previous_value + delta`; re-encoded as the field's value |
   | varint-list      | odd ID (except 9)                     | zigzag-signed deltas concatenated, one per element                                | element-wise sum with the previous list                                   |
   | raw bytes        | ID 9 (`moofInitializationVector`)     | full IV bytes verbatim                                                            | overwrite previous IV bytes                                               |

3. **`moofDeltaDeletedLocmafIDs` (ID 17) lists fields removed since the
   previous moof.** It is a varint list of locmaf IDs. The decoder applies
   the deletion before applying the deltas in (2). Used for transitions
   like "first fragment had `moofFirstSampleFlags`, subsequent ones don't".

An empty delta payload (`properties_length = 0`) is valid and means "no
field changed since previous moof" — the steady-state case for
sample-level fragmented streams. The on-wire object reduces to just
`MoofDeltaHeader | 0 | mdat`, which is **2 bytes plus the mdat**.

### Worked example: a one-sample-per-moof delta stream

Consider an AVC video track fragmented one frame per moof, with the
encoder having chosen `trex` defaults that match the steady-state per-frame
values (typical for an mp4ff-encoded stream). A raw CMAF moof is ~100 B per
fragment. The LOCMAF objects for the first three fragments of a group are:

**Fragment 0 — full moof (IDR / group start), ~8–20 bytes depending on flags:**

```
+----------------------------+
| MoofHeader=23 (1 B)        |
+----------------------------+
| properties_length (1 B)    |
+----------------------------+
| moofSampleCount=1          |   2 B  (ID 14 + 1-byte varint)
| moofBaseMediaDecodeTime    |   ≈2 B (ID 10 + bmdt varint, longer as bmdt grows)
| moofFirstSampleFlags       |   2–5 B (ID 12 + sample_flags varint; the
|                            |          sync-sample value 0x02000003 fits in 4 B)
+----------------------------+
| mdat raw payload …         |
+----------------------------+
```

Only `moofSampleCount` and `moofBaseMediaDecodeTime` are mandatory; every
other field is omitted when its source value matches the catalog's trex
default or its `HasDefault*` flag is unset. For sample-level fragmentation
where `tfhd` carries no overrides and `trun.first_sample_flags` is absent
(per-sample flag carries the sync bit instead), the full moof shrinks to
just the count + bmdt pair plus the LOCMAF framing — around 6 B.

**Fragment 1 — delta moof, IDR → non-sync transition, ~4–5 bytes:**

The only difference from the previous moof is that `first_sample_flags` is
no longer present (the new fragment's leading frame is non-sync, with flags
matching `tfhd.default_sample_flags`). The encoder emits:

```
+-------------------------+
| MoofDeltaHeader=25 (1B) |
+-------------------------+
| properties_length (1 B) |
+-------------------------+
| moofDeltaDeletedLocmafIDs       ← ID 17 (odd → length-prefixed)
|   length=1, payload=[12]        ← "field 12 removed since previous"
+-------------------------+
| mdat raw payload …      |
+-------------------------+
```

**Fragments 2..N-1 — delta moof, no field changed, 2 bytes:**

```
+-------------------------+
| MoofDeltaHeader=25 (1B) |
+-------------------------+
| properties_length = 0   |   1 B
+-------------------------+
| mdat raw payload …      |
+-------------------------+
```

This is the steady state. `moofBaseMediaDecodeTime` advances implicitly,
all sample-level fields are unchanged from the previous moof, the
two-byte LOCMAF wrapper carries the entire `moof` worth of information.

Empirically measured against
`assets/test10s/video_400kbps_avc.mp4` (250 fragments, 1 sample each):

| metric                          | bytes  |
| ------------------------------- | -----: |
| CMAF moof total                 | 26 040 |
| LOCMAF object total (no mdat)   |   592  |
| LOCMAF full moof                |   19 (one)   |
| LOCMAF delta moof, average      |   2.3  |

Re-runnable via `go run ./cmd/locmaf roundtrip -verbose`.

### Round-trip semantics

For both full and delta moof reconstruction, the decoder reuses the moov
parsed from the catalog's `initData`:

- default values from `trex` in the reconstructed moov are used as
  basis, but overruled by default values in `tfhd`.
- `track_id` is taken from `moov.trak.tkhd.track_id`
- The reconstructed sample's effective `sample_flags` for sample 0 follows
  `first_sample_flags` (if present)

These choices make the LOCMAF representation lossy at the byte level (the
reconstructed `moof.Size()` may differ from the source) but lossless at
the *playback* level (every sample has the same `size`, `dts`, `cts`,
`flags`, and encryption metadata as the source) — *provided* the source
also satisfies the structural assumptions below.

#### `trun.tr_flags` is implicit

LOCMAF carries no `trun.tr_flags` value. Encoding-side, `tr_flags` is
consulted only to gate which `moofSampleXxx` fields are emitted;
decoding-side, the reconstructed `trun` uses a fixed `tr_flags = 0xf01`
plus conditional bits for `composition_time_offset_present` and
`first_sample_flags_present`.[^trflags-impl] The implication: the reconstructed `trun` may
declare per-sample arrays as "present" where the source omitted them via
`tr_flags = 0`. Effective sample-level fidelity is preserved; byte-level
identity of the `trun` box is not.

#### Implicit `tfdt.baseMediaDecodeTime`

A delta moof normally omits `moofBaseMediaDecodeTime`; the receiver
derives the new BMDT as
`previous_bmdt + sum(previous_sample_durations)`. If the prediction is
wrong — audio pre-roll, splicing, or stream-tear / re-anchor — the
encoder emits `moofBaseMediaDecodeTime` (id 10) as an absolute value in
the delta moof, and the receiver uses that value instead of the
derivation.

### Moov field reference

For completeness, the field-ID table used inside a `MoovHeader` object. The
same parity rule applies (even ID → scalar varint, odd ID → length-prefixed
bytes). The catalog Track object supplies `role`, `timescale` (per the
catalog's `timescale` field, mapped to `mdhd.timescale`), `width`,
`height`, `samplerate`, and `codec`, so those fields are *not* present in
the LOCMAF moov payload — they live in the catalog instead.

| ID | Symbol                          | Source box | Description                                    |
| -- | ------------------------------- | ---------- | ---------------------------------------------- |
|  1 | `moovColr`                      | stsd       | colour information box (raw bytes)             |
|  2 | `moovMovieTimescale`            | mvhd       | movie timescale                                |
|  3 | `moovPasp`                      | stsd       | pixel aspect ratio box (raw bytes)             |
|  4 | `moovTkhdFlags`                 | tkhd       | track header flags                             |
|  5 | `moovChnl`                      | stsd       | audio channel layout box (raw bytes)           |
|  6 | `moovMediaTime`                 | elst       | edit-list media_time of the first entry (signed zigzag varint) |
|  7 | `moovCodecConfigurationBox`     | stsd       | codec config record (avcC, hvcC, esds, …; raw bytes) |
|  8 | `moovFormat`                    | stsd       | sample-entry FourCC packed as uint32           |
|  9 | `moovDefaultKID`                | tenc       | default Key ID (16 bytes)                      |
| 10 | `moovChannelCount`              | stsd       | audio channel count                            |
| 11 | `moovDefaultConstantIV`         | tenc       | default constant IV bytes                      |
| 12 | `moovSchemeType`                | schm       | protection scheme FourCC (cenc / cbcs)         |
| 14 | `moovTencVersion`               | tenc       | tenc box version                               |
| 16 | `moovDefaultCryptByteBlock`     | tenc       | pattern encryption: encrypted block count      |
| 18 | `moovDefaultSkipByteBlock`      | tenc       | pattern encryption: skipped block count        |
| 20 | `moovDefaultPerSampleIVSize`    | tenc       | per-sample IV size                             |
| 22 | `moovDefaultConstantIVSize`     | tenc       | constant IV size                               |
| 24 | `moovDefaultSampleDuration`     | trex       | track extension default sample duration        |
| 26 | `moovDefaultSampleSize`         | trex       | track extension default sample size            |
| 28 | `moovDefaultSampleFlags`        | trex       | track extension default sample flags           |

The trex defaults at IDs 24/26/28 are the same defaults that the moof
encoder compares against when deciding whether to omit a matching tfhd
field — once the receiver decodes the moov it has the trex values needed
to interpret incoming moofs.

For CMSF without DRM, only IDs 2, 4, 7, 8 (and rarely 1, 3, 5, 10) are
emitted; the moov payload typically lands at 50–80 B plus the codec config
box. For encrypted content the tenc-related IDs (9, 11, 12, 14, 16, 18,
20, 22) appear, and the moov grows by the size of the codec-specific keys
and IVs.

## DRM with LOCMAF

LOCMAF's primary motivation is **low-latency, low-overhead DRM-protected
streaming over MoQ**. Encrypted CMAF content survives the LOCMAF
compression and reconstruction round-trip unchanged at the bytes that
matter for decryption, so the standard CMSF / MSE / EME / CDM pipeline
takes over on the receiver as if the content had arrived as plain CMAF.

### End-to-end pipeline

```
   ┌─────────────────┐
   │ Encrypted CMAF  │  (mdat = ciphertext; senc has per-sample IVs +
   │ at the encoder  │   subsample maps; tenc carries default KID / IV)
   └────────┬────────┘
            │  encode
            ▼
   ┌─────────────────┐
   │ LOCMAF on wire  │  mdat bytes carried verbatim (no re-encryption);
   │ (moof + mdat)   │  senc data packed into LOCMAF moof field IDs
   │                 │  9, 11, 13, 15, 16; tenc defaults carried in
   │                 │  catalog initData via IDs 9, 11, 12, 14, 16, 18,
   │                 │  20, 22
   └────────┬────────┘
            │  decode
            ▼
   ┌─────────────────┐
   │ Reconstructed   │  mdat byte-equal to source; senc rebuilt
   │ CMAF fragment   │  from LOCMAF fields; trex/tenc rebuilt from
   │                 │  catalog initData
   └────────┬────────┘
            │  MSE append
            ▼
   ┌─────────────────┐
   │ MSE / EME       │  Browser CDM (Widevine, PlayReady, FairPlay,
   │ + CDM           │  ClearKey) decrypts using the per-sample IV +
   │                 │  KID, same as any CMAF stream
   └─────────────────┘
```

The crucial property: **the mdat payload is byte-equal end-to-end**.
LOCMAF is "byte-lossy at the trun level but playback-lossless" for
unprotected content, and the same applies to encrypted content — the
moof structure may differ between source and reconstruction, but the
ciphertext bytes the CDM sees do not.

### Catalog DRM signalling

CMSF carries DRM information at the catalog level (root-level
`contentProtections` array, plus a per-track `contentProtectionRefIDs`
that points at one or more entries). The structures used in this
project mirror DASH-IF IOP 6:

```json
{
  "contentProtections": [
    {
      "refID": "widevine",
      "defaultKID": ["abcdef0123456789abcdef0123456789"],
      "scheme": "cbcs",
      "drmSystem": {
        "systemID": "edef8ba9-79d6-4ace-a3c8-27dcd51d21ed",
        "robustness": "SW_SECURE_CRYPTO",
        "laURL": { "url": "https://lic.example.com/widevine", "type": "POST" },
        "pssh": "base64-pssh-box"
      }
    }
  ],
  "tracks": [
    {
      "name": "video_400kbps_avc_drm",
      "packaging": "locmaf",
      "locmafVersion": "0.1",
      "contentProtectionRefIDs": ["widevine", "playready", "fairplay"],
      "initData": "..."
    }
  ]
}
```

`refID` allows a single DRM-system description to be reused across many
tracks. A receiver picks the first `refID` whose `drmSystem.systemID`
matches a CDM it can talk to, then uses the named `pssh` / `laURL` /
`authzURL` / `certURL` to set up the MediaKeySession exactly as it
would for a standard CMSF stream. LOCMAF is invisible to the EME layer
— it is purely a transport-side compression.

The `locmafVersion` field discussed in
[CMSF catalog signalling](#cmsf-catalog-signalling-locmafversion)
applies to DRM-protected tracks too: a receiver that doesn't support
the encoder's LOCMAF revision should refuse the track regardless of
whether it can handle the DRM scheme.

### `cenc` vs `cbcs` on the wire

The two main CMAF protection schemes differ in how they handle the
initialization vector, but both use the same sub-sample encryption for
video (where only the NAL unit payloads are encrypted; the NAL headers
and slice prefixes stay in the clear so a parser can still walk the
stream). The LOCMAF wire-cost implications:

| scheme | per-sample IV (id 9) | subsample maps (ids 11/13/15) on video | typical extra wire cost per delta moof |
| ------ | -------------------- | -------------------------------------- | -------------------------------------- |
| `cenc` | per-sample, 8 or 16 B; raw bytes, no delta | per-sample, ~3 B/subsample (clear+protected) | **8–16 B IV + subsample bytes per sample** |
| `cbcs` | none — constant IV from `tenc.default_constant_iv` carried once in the moov | per-sample, ~3 B/subsample | **subsample bytes only**, no IV per sample |

Two important consequences:

- **Audio is sub-sample-encryption-free under both schemes** (the whole
  sample is a single fully-encrypted block, no clear NAL prefixes), so
  cbcs audio has *no* per-fragment encryption signalling at all and
  the cbcs audio LOCMAF wire bytes equal the clear audio LOCMAF wire
  bytes. cenc audio still carries the per-sample IV.
- **Video carries the subsample maps under both schemes**, so the
  IDs 11 / 13 / 15 cost (~10 B per sample) applies to cbcs video too.
  The only cbcs-vs-cenc per-fragment delta on video is the per-sample
  IV (~16 B per sample) — i.e. cenc video pays for both the IV *and*
  the subsamples, while cbcs video pays for the subsamples only.

This is why Hugo's thesis §6.2.2 shows the cbcs delta moof carrying
`bytesOfClearData` / `bytesOfProtectedData` (for video) while the cenc
delta moof additionally carries `initializationVector` on every sample.

### Catalog `bitrate` impact on the bundled DRM-protected assets

The catalog `bitrate` field reflects this end-to-end. Measured on
`assets/test10s` at one sample per MoQ object with ECCP (ClearKey /
explicit-key) protection in both schemes:

**`cenc` (per-sample IV):**

| track | sample [bps] | cmaf [bps] | locmaf [bps] | saved [bps] | saved % |
| ----- | -----------: | ---------: | -----------: | ----------: | ------: |
| `audio_monotonic_128kbps_aac_eccp`  | 128 001 | 197 376 | 138 637 | 58 739 | 29.8 % |
| `audio_monotonic_128kbps_opus_eccp` | 128 400 | 202 400 | 139 736 | 62 664 | 31.0 % |
| `audio_monotonic_192kbps_ac3_eccp`  | 192 000 | 238 250 | 199 136 | 39 114 | 16.4 % |
| `video_400kbps_avc_eccp`            | 373 200 | 412 000 | 382 096 | 29 904 |  7.3 % |
| `video_400kbps_hevc_eccp`           | 299 392 | 338 192 | 308 096 | 30 096 |  8.9 % |
| `video_600kbps_avc_eccp`            | 559 505 | 598 305 | 568 417 | 29 888 |  5.0 % |
| `video_900kbps_avc_eccp`            | 844 504 | 883 304 | 853 416 | 29 888 |  3.4 % |
| `video_900kbps_hevc_eccp`           | 610 182 | 648 982 | 618 902 | 30 080 |  4.6 % |

**`cbcs` (constant IV):**

| track | sample [bps] | cmaf [bps] | locmaf [bps] | saved [bps] | saved % |
| ----- | -----------: | ---------: | -----------: | ----------: | ------: |
| `audio_monotonic_128kbps_aac_eccp`  | 128 001 | 191 376 | 131 887 | 59 489 | 31.1 % |
| `audio_monotonic_128kbps_opus_eccp` | 128 400 | 196 000 | 132 536 | 63 464 | 32.4 % |
| `audio_monotonic_192kbps_ac3_eccp`  | 192 000 | 234 250 | 194 636 | 39 614 | 16.9 % |
| `video_400kbps_avc_eccp`            | 373 200 | 408 800 | 378 496 | 30 304 |  7.4 % |
| `video_400kbps_hevc_eccp`           | 299 392 | 334 992 | 304 504 | 30 488 |  9.1 % |
| `video_600kbps_avc_eccp`            | 559 505 | 595 105 | 564 817 | 30 288 |  5.1 % |
| `video_900kbps_avc_eccp`            | 844 504 | 880 104 | 849 816 | 30 288 |  3.4 % |
| `video_900kbps_hevc_eccp`           | 610 182 | 645 782 | 615 294 | 30 488 |  4.7 % |

A few observations:

- **Audio cbcs LOCMAF == audio clear LOCMAF** on the moof side. AAC
  128 kbps clear LOCMAF was 131 887 bps; AAC 128 kbps cbcs LOCMAF is
  also 131 887 bps. Audio doesn't use sub-sample encryption, so the
  constant IV lives once in the moov and the per-fragment LOCMAF
  payload is identical to the clear case.
- **Video cbcs LOCMAF carries the subsample signalling** (IDs 11 / 13
  / 15) on every fragment, so it pays an extra ~10 B per object
  relative to clear video LOCMAF — about 2 kbps at 25 fps. Compare
  `video_400kbps_avc` clear LOCMAF (376 488 bps) with the cbcs
  counterpart `video_400kbps_avc_eccp` (378 496 bps): the 2 008 bps
  difference is exactly that subsample carriage.
- **Video pays a small per-group IDR cost** that audio doesn't,
  independent of DRM. The first sample of every group is an IDR (sync
  sample) with different `sample_flags` than the non-sync samples that
  follow, so the first delta moof of each group carries a
  `moofSampleFlags` delta of about 3 B more than a steady-state delta.
  At 1-second groups and 25 fps that's around 25 bps additional video
  bitrate (one event per group). It shows up in both clear and
  protected video, on top of the per-fragment subsample signalling for
  the protected case.
- **`cenc` LOCMAF costs roughly 6–7 kbps more than `cbcs`** for audio
  (47 objects/s × 16 B per-sample IV × 8 ≈ 6 kbps) and a similar extra
  ~3 kbps for 25 fps video — exactly the per-sample IV overhead that
  cbcs avoids.
- **LOCMAF saves *more* relative to CMAF on DRM-protected content than
  on clear content** (AAC: 31% on cbcs / 30% on cenc vs 23% on clear)
  because the CMAF moof grows more under encryption (senc box + saio
  + saiz) while the LOCMAF moof only adds what it actually needs to
  carry.

For CENC the **counter-prediction optimisation** described in
[Possible improvements → Omit CENC per-sample IVs via counter prediction](#omit-cenc-per-sample-ivs-via-counter-prediction)
would close the cenc/cbcs gap by deriving the per-sample IV from the
counter rule rather than transmitting it.

### Per-component breakdown for one video group

Decomposing one MoQ group of `video_400kbps_avc` (25 fragments at 25 fps
= 1 second of video, 1 sample per object) into the actual bytes emitted
per LOCMAF field. Numbers are measured by walking the wire bytes of
each LOCMAF object and counting each field tuple's cost — the framing
column is the `header_id` varint + `properties_length` varint pair
(2 B per object); the rest are the per-field tuples that appear in
`### Moof field reference`. The `mdat` payload is excluded — these are
the bytes the LOCMAF wrapping adds on top of the sample data.

| field (group total)             | clear  | cbcs eccp | cenc eccp |
| ------------------------------- | -----: | --------: | --------: |
| framing (header_id + length)    |   50 B |     50 B  |     50 B  |
| `moofBaseMediaDecodeTime`       |    2 B |      2 B  |      2 B  |
| `moofSampleCount`               |    2 B |      2 B  |      2 B  |
| `moofSampleDurations`           |    4 B |      4 B  |      4 B  |
| `moofSampleCompositionTimeOffsets` | 3 B |      3 B  |      3 B  |
| `moofSampleFlags`               |   12 B |     12 B  |     12 B  |
| `moofSubsampleCount`            |      — |      3 B  |      3 B  |
| `moofBytesOfClearData`          |      — |     20 B  |     71 B  |
| `moofBytesOfProtectedData`      |      — |     99 B  |     92 B  |
| `moofInitializationVector`      |      — |       —   |    450 B  |
| **total per 1-s group**         | **73 B** | **195 B** | **689 B** |

Per-fragment kinds in the same data set:

| fragment kind            | clear | cbcs eccp | cenc eccp |
| ------------------------ | ----: | --------: | --------: |
| full moof (group start)  |  19 B |     30 B  |     48 B  |
| first delta (IDR → P)    |   8 B |     18 B  |     36 B  |
| steady-state delta       |   2 B |     ~6 B  |    ~27 B  |

The `sampleFlags` 12 B in every column splits as 6 B on the full moof
(carries the IDR's effective flags as a per-sample list) and 6 B on the
first delta moof (carries the IDR → P flags transition); subsequent
deltas omit it. That is the per-group IDR cost.

`moofInitializationVector` is the dominant cenc overhead at 450 B per
group: 18 B per sample × 25 samples, never compressed or delta-coded.
The [CENC IV counter-prediction optimisation](#omit-cenc-per-sample-ivs-via-counter-prediction)
would drop that to zero, bringing cenc video close to cbcs video.

`moofBytesOfProtectedData` (id 15) is the surprise contributor for both
cbcs and cenc — about 4 B per sample regardless of scheme, because the
encoded P-frame size varies per fragment and the delta is small but
non-zero. `moofBytesOfClearData` (id 13) is sparser because NAL header
sizes are mostly stable, so the encoder elides the per-sample list when
it matches the previous moof.

**Note on the catalog-bitrate measurement**: the `calcLocmafBitrate`
function generates one full + one delta chunk and applies the delta
size to all 24 deltas in the second. The first delta is the larger
post-IDR delta (with the `sampleFlags` transition), so the function
slightly overestimates the steady-state video LOCMAF bitrate. The
real video LOCMAF wire bitrate is roughly **1.1–3.7 kbps lower** than
the catalog reports (varying with the protection scheme); audio is not
affected because audio has no IDR/P distinction.

### Why byte-lossy moof reconstruction is safe for DRM

The CDM decrypts a fragment using `tenc.default_KID` (or per-sample
key info), the per-sample `senc.InitializationVector`, the
subsample `BytesOfClearData` / `BytesOfProtectedData` ranges, and the
ciphertext bytes from the `mdat`. LOCMAF carries every one of these
verbatim — there is no transformation of the encrypted bytes and no
loss of crypto metadata. The encoder's choices that *are* lossy
(`tr_flags` packing, omitted `tfhd` defaults that match `trex`, the
implicit mdat box header) all live in the parts of the moof that
don't participate in decryption. So a fragment that decrypts on the
source side decrypts identically on the receiver side.

This holds for both `cenc` and `cbcs`, for pattern encryption (the
`tenc.default_crypt_byte_block` / `default_skip_byte_block` ride in
the catalog moov payload at IDs 16 / 18), and for sub-sample
encryption.

## Init segments: less critical, but still compressible

CMAF init segments (the `ftyp + moov` pair) are sent once per track per
subscriber session. In CMSF (Common Media MoQ Streaming Format) the init
bytes ride inside the catalog Track object as a base64 `initData` field, so
the subscriber sees them on subscribe rather than at every fragment.

For a typical video stream that runs for minutes, the init is a one-time
cost amortised over thousands of moof bytes. The wire-budget pressure is
*not* the init — it is the per-fragment moof stream. So a simpler init
encoding is acceptable.

That said, LOCMAF does compress the init meaningfully:

- For most moovs measured (650–1100 B), LOCMAF gets to **8–20% of CMAF**,
  because it can drop fields that the catalog already carries (track role,
  timescale, dimensions, sample rate, codec four-cc) and rebuild them on
  the receiver from the catalog Track object.
- For codec-config-heavy moovs (HEVC ~3 KB, where the bulk is VPS/SPS/PPS),
  LOCMAF can only reach ~50–76% because the parameter-set bytes are opaque
  blobs that have to be transmitted verbatim. In those cases plain gzip
  actually compresses better because it entropy-codes the parameter-set
  bitstream.

Two reasonable simpler alternatives that could replace or coexist with
LOCMAF-encoded init:

- **Gzip-wrapped `ftyp + moov`.** Hits 45–55% of CMAF regardless of
  content. Cheap to implement, codec-agnostic, but never as small as the
  catalog-aware LOCMAF init on simple moovs.
- **Standard ISO BMFF compressed-box wrapping** (ISO/IEC 14496-12 has a
  generic mechanism for compressed box payloads). Carries any box,
  including future ones, with a documented compression algorithm. Lets the
  init encoding stay a strict subset of standard ISO BMFF.

Because the catalog `initData` is a self-describing opaque blob from the
catalog's point of view, all three encodings can coexist behind a header-ID
varint as the first byte of the payload: `MoovHeader=21` for LOCMAF,
`MoovGzipHeader=27` for gzip-wrapped CMAF, etc. Decoders dispatch on the
leading varint before parsing the payload — no schema change to CMSF is
required to support an additional encoding.

## Forward extensibility

LOCMAF object framing is uniform: every object is a `header_id` varint
followed by a `payload_length` varint and the payload bytes. Receivers
that encounter an unrecognised `header_id` log it and skip the payload
by its declared length, so a sender can introduce new object types
without breaking older readers.[^skip-impl]

The obvious candidate for the next object type is **`prft` (Producer
Reference Time)**[^prft-spec] for wall-clock signalling:

- Low-latency live streaming benefits from explicit wall-clock mapping at
  each fragment so receivers can compute the producer-to-glass latency
  and align audio/video tune-in to absolute time.
- `prft` carries `reference_track_ID` (uint32), a 64-bit NTP timestamp
  (`NTP64`: upper 32 bits = seconds since 1900, **lower 32 bits =
  fixed-point fraction** where the full 32-bit range represents one
  second), and a `media_time` (uint32 or uint64). For a 60-fps stream at
  sample-level fragmentation that's an extra object every ~16.667 ms.
- The same delta-coding pattern applies: the first `prft` of a group is
  full (track ID + absolute NTP + absolute media_time), subsequent ones
  carry only the deltas. **Both deltas must be signed (zigzag-encoded
  varints)** rather than unsigned, because:
  - The producer's wall clock can be corrected backward (NTP sync
    adjustment), making the NTP delta negative.
  - With B-frames the encoder's composition-time-offset reordering means
    a fragment's anchor in presentation order can precede the previous
    fragment's; depending on how the implementation defines the prft
    anchor (decode-time vs. presentation-time), media_time deltas can
    therefore be negative.
  - Stream re-anchoring (the same scenarios that justify the absolute
    BMDT override for delta moofs) can move time backward.
  Signed zigzag costs the same number of bytes as unsigned for typical
  positive deltas — it just permits negative ones when needed.
- The NTP deltas are still not tiny, though, because of the 32-bit NTP
  fraction: a 16.667 ms increment at 60 fps is
  `16.667 ms × 2³² / 1000 ms ≈ 71.6 million NTP ticks`; a 40 ms
  increment at 25 fps is ~171.8 million; an AAC frame at 48 kHz
  (≈ 21.33 ms) is ~91.6 million. Each of these fits in a 4-byte QUIC
  varint (signed zigzag puts the magnitude in the same band), so the
  NTP-delta payload alone is **~4 B per `prft` object**.
- `media_time` delta is typically very small in the natural track
  timescale (e.g. 1024 for AAC at 48 kHz, 1001 for 60000/1001 fps
  video), encoding as a 1- or 2-byte zigzag varint.
- Total steady-state cost per `prft` object: ~4 B NTP-delta + ~1–2 B
  media_time-delta + 2 B LOCMAF wrapper = **~7–8 B**.

A second-derivative optimisation would buy back some of that: if the
frame rate is stable, the NTP delta is constant across consecutive
fragments, so the *delta of the delta* is zero (or near-zero — small
clock-jitter values). Encoding NTP as a delta-of-delta would shrink the
steady-state `prft` to ~3 B per object. Worth keeping in mind, but only
the simple delta scheme is needed to start.

A third option is to **carry an approximate timestamp** instead of full
NTP precision. Tune-in and audio/video sync rarely need sub-millisecond
wall-clock accuracy; a microsecond-resolution timestamp (or even
millisecond) is enough in practice. A LOCMAF `prft` variant that carries
the high N bits of the NTP fraction (or replaces NTP with vi64
microseconds-since-Unix-epoch, mirroring the LOC Timestamp property at
id 6) shrinks the per-fragment NTP-delta from ~4 B to ~2 B: a 16.667 ms
delta at microsecond resolution is 16 667, a 40 ms delta is 40 000 — both
2-byte varints. Combined with delta-of-delta, the per-`prft` cost lands
near the LOCMAF framing floor.

Other plausible additions — `sidx` for in-band index, `emsg` for DASH events,
`tkhd`/`mfhd` extensions — fit the same pattern: allocate a new top-level
`header_id`, document the payload field IDs, and old decoders skip
gracefully.

## Possible improvements

A few directions in which LOCMAF could be refined further, listed roughly
by potential per-fragment byte savings.

### Pack `sample_flags` more compactly

The 32-bit ISO BMFF `sample_flags` field (ISO/IEC 14496-12:2022 §8.8.3.1)
is bit-packed:

| bits | field |
| --- | --- |
| 31–28 | reserved (zero) |
| 27–26 | `is_leading` (2 bits) |
| 25–24 | `sample_depends_on` (2 bits) |
| 23–22 | `sample_is_depended_on` (2 bits) |
| 21–20 | `sample_has_redundancy` (2 bits) |
| 19–17 | `sample_padding_value` (3 bits) |
| 16 | `sample_is_non_sync_sample` (1 bit) |
| 15–0 | `sample_degradation_priority` (16 bits) |

Information-theoretically a meaningful `sample_flags` value uses only
about 5–7 bits — but the populated bits live high. A typical IDR encodes
to `0x02000000` (≈ 33.5 million), a typical P-frame to `0x01010000`
(≈ 16.8 million), so each per-sample entry lands in the **4-byte QUIC
varint band** even though all the real information would fit in a single
byte. The `sample_degradation_priority` low-16-bit field is essentially
always zero in modern content (it is a legacy QoS hint).

Two approaches would shrink the per-sample `moofSampleFlags` cost from
4 B to 1 B:

1. **Bit-reorder for transport.** Pack the populated fields into the low
   bits before emitting the varint and re-expand to 32-bit on decode.
   For instance: `(sample_is_non_sync_sample << 6) | (sample_depends_on << 4) | (sample_is_depended_on << 2) | is_leading` — fits in 7 bits, single-byte varint.
2. **Per-track flag dictionary.** Most CMAF tracks see only 2–3 distinct
   flag values (sync vs non-sync, with/without `is_depended_on`). The
   first occurrence of each unique value gets a small index and
   subsequent samples reference the index. With a 2-bit dictionary index
   in the varint payload, a per-sample flag value collapses to 1 B.

Either approach drops the IDR→P transition delta from ~4 B to ~1 B.
Not currently the dominant cost, but it would close the gap for video
streams that interleave IDRs and P-frames frequently (e.g. 1-second GOPs
at 60 fps).

### Carry `prft` with delta-coding

Already sketched in "Forward extensibility": NTP-timestamp + media-time
deltas as a separate top-level LOCMAF object. Realistic steady-state
cost is **~7–8 B per `prft` object** with full NTP precision (NTP
fraction deltas of tens of millions still need a 4-byte varint), ~3 B
with delta-of-delta, or **down toward the framing floor with an
approximate timestamp** (microsecond-resolution vi64, mirroring the LOC
Timestamp property; per-fragment deltas of 16 667 or 40 000 µs fit in
2-byte varints). Adds explicit wall-clock signalling without inflating
the moof object itself.

### Omit CENC per-sample IVs via counter prediction

For `cenc` (and `cens`) protection schemes the per-sample IV is a
big-endian integer counter, advanced sample-by-sample by exactly
`ceil(total_encrypted_bytes_in_sample / 16)` per
ISO/IEC 23001-7. Both endpoints know the previous IV and the
previous sample's `bytesOfProtectedData` totals (LOCMAF IDs 13 and 15
are already in the payload), so the receiver can derive the next IV
deterministically. When the source follows the standard CENC counter,
the encoder can omit `moofInitializationVector` (ID 9) entirely from
both full and delta moofs — the receiver computes it.

At 1 sample per fragment with 8-byte IVs that's **~480 B/s saved at
60 fps**, and **~960 B/s at 16-byte IVs**, dropping to zero on the
wire. Today the same data sits in id 9 as raw, non-delta-able bytes.

When the encoder doesn't follow CENC counter semantics — random IVs,
mid-track counter restart, a non-conformant per-track scheme — emit
the absolute IV as today. This is the same predict-and-fallback
pattern as the BMDT discontinuity override, just applied per-sample
within the moof rather than per-moof.

Caveats:

- `cbcs` uses a constant IV from `tenc.default_constant_iv` and emits
  no per-sample IV at all. Already optimal; no-op.
- The counter is over **encrypted** bytes, which for sub-sample
  encryption (most video) is the sum of `bytesOfProtectedData`, not
  the full sample size.
- `per_sample_iv_size` (8 or 16 bytes, from `tenc`) determines how
  the counter wraps; 8-byte IVs are zero-extended to 128 bits before
  arithmetic, but only the high or low 8 bytes appear on the wire.
- First IV of a track is the anchor — carried in the full moof, same
  as today.

### Drop the per-object framing overhead floor

Every LOCMAF object carries a header-ID varint (≥ 1 B) and a
properties-length varint (≥ 1 B), so the minimum per-object cost is 2 B
even with an empty payload. For ultra-low-bitrate audio (where the mdat
is itself only a few hundred bytes) this 2 B floor is a measurable
fraction of total overhead. Two possible mitigations:

1. **Implicit framing for the most common case.** Treat a zero-byte
   object payload (a MoQ object with no LOCMAF data) as an implicit
   "delta moof, no field changed". This drops the floor to 0 B but
   creates an ambiguity with reserved IDs and would need a careful
   spec wording. Hugo's thesis §6.4 notes this trade-off explicitly.
2. **Promote LOCMAF properties to LOC public properties.** Today
   LOCMAF lives in the MoQ object payload (LOC's "private property"
   slot). If LOCMAF properties were promoted to LOC public properties
   in the MoQ object's properties field, the LOC framing would
   identify the LOCMAF property directly and the redundant LOCMAF
   header-ID varint could be dropped.

### Carry codec config records in the catalog instead of the moov

For codec-config-heavy moovs (HEVC `hvcC` ~2.5 KB, MPEG-H `mhaC`,
VVC `vvcC`), the LOCMAF moov payload is dominated by the opaque
codec config bytes. Moving them out of the LOCMAF moov and into the
catalog Track as a base64 field would let the LOCMAF moov stay small
across all codecs. This is a CMSF schema change rather than a LOCMAF
wire-format change, so it's a separate decision, but worth flagging
because LOCMAF's "we drop derivable fields" principle naturally
extends here.

### Pre-flight source validation

Several LOCMAF assumptions are structural rather than enforced by the
CMAF spec: contiguous `tfdt.baseMediaDecodeTime` across fragments,
commensurate timescales (integer per-frame duration), single-track moov,
stable `trex` defaults across the stream, and (for full `cmf2`
conformance) `tfhd`-resident defaults. None of these are checked today —
the encoder simply emits LOCMAF.

An encoder mode that validates the source before turning LOCMAF on
would catch:

1. **Non-integer per-frame duration.** Verify that
   `media_timescale × frame_period` is integer for the configured frame
   rate / sample rate. Reject `1000 / 60000-over-1001` etc. (see the
   "Prerequisite: commensurate media timescales" section).
2. **Mismatched trex defaults.** Verify that the source's actual
   per-fragment defaults (after `AddSampleDefaultValues`) genuinely match
   the moov's `trex` — i.e. that the encoder isn't about to omit a
   field that shouldn't be omitted.
3. **Multi-track moov.** Reject any moov with more than one track (CMAF
   allows only one anyway, but the LOCMAF code path assumes it).

(BMDT contiguity is *not* a precondition: the encoder already re-anchors
the timeline in-band by emitting an absolute `moofBaseMediaDecodeTime`
in the delta moof whenever the derived value would be wrong. A warning
log when this happens is useful operationally, but does not block
LOCMAF encoding.)

If validation fails, the publisher should either fix the source (e.g.
re-package with the correct timescale) or fall back to a non-LOCMAF
packaging — see next item.

### Full CMAF chunk header as a fallback object kind

Today the only top-level object kinds for media segments are
`MoofHeader = 23` (full LOCMAF moof) and `MoofDeltaHeader = 25` (delta
LOCMAF moof). BMDT discontinuities are handled in-band via the absolute
`moofBaseMediaDecodeTime` override (see the Round-trip semantics
section), so they do not need a fallback object kind. But other
structural mismatches — fragments with field combinations the encoder
can't express in the current vocabulary, or content with multi-entry
edit lists — leave no clean escape hatch. Allocating two more IDs
along the same lines as the init-side gzip discussion would close that
gap:

| ID (proposed) | Symbol | Object kind |
| --- | --- | --- |
| 31 | `MoofRawHeader` | raw CMAF `moof` (uncompressed) + mdat |
| 33 | `MoofGzipHeader` | gzip-wrapped CMAF `moof` + mdat |

`MoofRawHeader` carries the original CMAF `moof` bytes verbatim in the
LOCMAF payload, followed by mdat. `MoofGzipHeader` carries the
gzip-deflated `moof` bytes. Either is fully self-contained — no reliance
on `trex` defaults, BMDT contiguity, or any other LOCMAF-side
assumption. The encoder can switch on a per-fragment basis: emit the
delta moof for fragments that fit, and a raw / gzip-wrapped CMAF
fragment when the validator says no.

The decoder dispatches on the header ID exactly as it already does for
unknown objects — the `MoofDeltaDecompressor` would gain two new cases
that take the LOCMAF payload, run the appropriate decompression (none
for raw, gzip-inflate for the wrapped case), and return the
reconstructed `moof` directly.

It also gives an escape hatch for future LOCMAF revisions that
introduce new object kinds: a sender talking to an older receiver can
fall back to `MoofRawHeader` / `MoofGzipHeader` whenever the receiver
doesn't recognise the new vocabulary.

### Strict `cmf2` self-containment mode

As noted in the "Where LOCMAF wins" section, the encoder currently
omits `tfhd` defaults that match `trex`, which corresponds to what ffmpeg and
other common packagers emit but does not strictly conform to CMAF
`cmf2` (§7.7.3 requires each fragment to be decodable without the
CMAF header). An encoder mode that always emits `tfhd` defaults
would cost ~6 B per full moof (once per group) and produce fragments
that survive isolated decoding after LOCMAF→CMAF reconstruction.

## Summary

- LOCMAF's biggest contribution is **per-fragment `moof` compression** in
  the regime where MoQ groups are aligned to CMAF segments and each MoQ
  object is one CMAF chunk. In sample-level fragmentation, delta moofs
  collapse to 2 B in steady state — a 45:1 reduction in `moof` overhead.
- **Init compression is a bonus** rather than the main goal. LOCMAF init
  compresses simple moovs well (down to ~10% of CMAF), but on
  codec-config-heavy moovs a generic compressor like gzip can match or
  beat it. Alternative init encodings can coexist behind the header-ID
  dispatch.
- The **header-ID varint** acts as the type tag and is also where any
  future format extension would live. Skipping unknown IDs is
  implemented, so the format can grow (`prft`, `sidx`, future
  additions) without coordinated upgrades while endpoints remain
  in-house.

[^cmf2-defaults]: The CMAF `cmf2` structural brand
    (ISO/IEC 23000-19:2023 §7.7.3) requires that `default_sample_flags`,
    sample duration, sample size, and sample description index
    "shall be stored in each CMAF chunk's `trun` and/or `tfhd`" box
    and explicitly notes that the corresponding values in the `trex`
    "**can be set and ignored** … so each CMAF fragment is decodable
    without access to that track CMAF header." In other words, a
    strictly spec-compliant CMAF stream carries the defaults in `tfhd`,
    not (only) in `trex`. In practice many encoders and packagers —
    including common ffmpeg configurations — do not follow this and
    place the defaults only in `trex`, producing fragments that are
    not self-decodable. LOCMAF's reuse of `trex` values therefore
    matches what those tools emit on the wire today; the reconstructed
    CMAF fragments are still playable because the LOCMAF decoder seeds
    `tfhd` from the catalog's `trex` during reconstruction. An encoder
    flag to always emit the defaults in `tfhd` (full `cmf2`
    self-containment) would cost a few extra bytes per full moof and is
    straightforward to add if strict conformance becomes important.

[^trflags-impl]: Reference implementation: `internal/locmaf.go:300` and
    `mp4.TrunBox.SetFirstSampleFlags`.

[^skip-impl]: Reference implementation of the skip-and-log path:
    `internal.MoofDeltaDecompressor.DecompressMoof` in this repository.

[^prft-spec]: `prft` is defined in ISO/IEC 14496-12 (the ISO Base Media
    File Format) and is available as `mp4.PrftBox` in the `mp4ff`
    library.

## References

### MoQ and CMAF specifications

- [draft-ietf-moq-transport][MOQT] — Media over QUIC Transport
- [draft-ietf-moq-cmsf][CMSF] — CMAF MoQ Streaming Format
- [draft-ietf-moq-loc][LOC] — Low Overhead Container (LOC)
- [draft-lcurley-compressed-mp4][compressed-mp4] — Compressed MP4
  (Luke Curley)
- [RFC 9000][rfc9000] — QUIC: A UDP-Based Multiplexed and Secure
  Transport
- **ISO/IEC 14496-12** — Information technology, Coding of audio-visual
  objects, Part 12: ISO base media file format (ISO BMFF)
- **ISO/IEC 23000-19:2023** — Common Media Application Format (CMAF)
  for segmented media
- **ISO/IEC 23001-7** — Common Encryption in ISO Base Media File Format
  files (CENC)

### Implementations and demos

- [Eyevinn/moqlivemock][moqlivemock] — Go MoQ server and subscriber
  used to prototype LOCMAF
- [Eyevinn/warp-player][warp-player] — Browser MoQ player with LOCMAF
  and EME support
- [moqlivemock.demo.osaas.io][mlm-demo] — public demo of the LOCMAF +
  DRM stack

### Related work

- **Efficient DRM in MoQ using Low Overhead CMAF** — Hugo Björs, KTH
  Master Thesis, 2026 (to appear)

[LOC]: https://datatracker.ietf.org/doc/draft-ietf-moq-loc/
[MOQT]: https://datatracker.ietf.org/doc/draft-ietf-moq-transport/
[CMSF]: https://datatracker.ietf.org/doc/draft-ietf-moq-cmsf/
[compressed-mp4]: https://datatracker.ietf.org/doc/draft-lcurley-compressed-mp4/
[rfc9000]: https://datatracker.ietf.org/doc/html/rfc9000
[mlm-demo]: https://moqlivemock.demo.osaas.io
[moqlivemock]: https://github.com/Eyevinn/moqlivemock
[warp-player]: https://github.com/Eyevinn/warp-player