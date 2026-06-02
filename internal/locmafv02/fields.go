// Package locmafv02 implements the LOCMAF v0.2 wire format defined in
// draft-einarsson-moq-locmaf. It is deliberately kept independent of the
// v0.1 codec in internal/locmaf*.go so the two implementations can run
// side-by-side on different namespaces.
//
// Only the moof side is implemented: the v0.2 catalog ships a raw,
// uncompressed CMAF Header, so there is no moov compression here.
// prft / styp / emsg are out of scope for the publisher (mlmpub's
// content carries none); the wire format leaves their field IDs
// reserved.
package locmafv02

// Version is the wire-format identifier announced in the CMSF catalog
// Track entry (Track.LocmafVersion). The wire format is specified by the
// IETF draft draft-einarsson-moq-locmaf (revision -00 carries spec
// version 0.2). prft, emsg and the documented DRM-box round-trip table
// are not yet carried by mlmpub, but those are additive (new reserved
// field IDs) and do not change the encoding of existing fields, so they
// land under the same "0.2" version. See docs/locmaf_v0_2.md for the
// per-field implementation status.
const Version = "0.2"

// Top-level header IDs from draft §12.
const (
	LocmafFullHeaderID  uint64 = 23
	LocmafDeltaHeaderID uint64 = 25
)

// fieldID enumerates the per-property identifiers defined in draft
// §13. Parity carries semantic weight: even IDs are scalar varints,
// odd IDs are length-prefixed bytes (varint lists or raw blobs).
type fieldID uint64

const (
	// Scalar fields (even IDs). The Go name mirrors the wire symbol
	// from the IETF draft: prefix names the source box inside
	// moof.traf — trun, tfhd, tfdt, or senc.
	idTfhdSampleDescriptionIndex fieldID = 2
	idTfhdDefaultSampleDuration  fieldID = 4
	idTfhdDefaultSampleSize      fieldID = 6
	idTfhdDefaultSampleFlags     fieldID = 8
	idTfdtBaseMediaDecodeTime    fieldID = 10
	idTrunFirstSampleFlags       fieldID = 12
	idTrunSampleCount            fieldID = 14
	idSencPerSampleIVSize        fieldID = 16

	// List / raw fields (odd IDs).
	idTrunSampleSizes                  fieldID = 1
	idTrunSampleDurations              fieldID = 3
	idTrunSampleCompositionTimeOffsets fieldID = 5
	idTrunSampleFlags                  fieldID = 7
	idSencInitializationVector         fieldID = 9
	idSencSubsampleCount               fieldID = 11
	idSencBytesOfClearData             fieldID = 13
	idSencBytesOfProtectedData         fieldID = 15

	// Delta deletion marker (draft §13.6, §16.4). Synthetic field —
	// not from any source box.
	idDeltaDeletedLocmafIDs fieldID = 27
)

// isList reports whether the wire encoding for this ID uses the
// odd-parity length-prefixed form.
func (id fieldID) isList() bool { return uint64(id)%2 == 1 }
