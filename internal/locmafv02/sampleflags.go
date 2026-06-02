package locmafv02

import "fmt"

// LOCMAF v0.2 packs the five varying sample_flags bits into a 5-bit
// transport value (draft §17):
//
//	bit 0    : sample_is_non_sync_sample
//	bits 1-2 : sample_depends_on (0..3)
//	bits 3-4 : sample_is_depended_on (0..3)
//
// The receiver expands the 5-bit transport into a 32-bit ISO BMFF
// sample_flags using:
//
//	sample_flags = (is_depended_on << 22)
//	             | (depends_on     << 24)
//	             | (non_sync       << 16)
//
// is_leading, has_redundancy, sample_padding_value and
// sample_degradation_priority are reconstructed as zero.

const (
	bmfSampleFlagsIsNonSync     uint32 = 1 << 16
	bmfSampleFlagsDependsOnMask uint32 = 0x03000000
	bmfSampleFlagsDependsOnLo   uint32 = 24
	bmfSampleFlagsDependedOnMsk uint32 = 0x00C00000
	bmfSampleFlagsDependedOnLo  uint32 = 22

	// Combined mask of the five bits LOCMAF v0.2 preserves; any other
	// bit set in the source sample_flags makes the source out-of-scope
	// for v0.2 (draft §17.3 encoder constraint).
	bmfSampleFlagsSupportedMask = bmfSampleFlagsIsNonSync |
		bmfSampleFlagsDependsOnMask | bmfSampleFlagsDependedOnMsk
)

// packSampleFlags reduces a 32-bit ISO BMFF sample_flags to the 5-bit
// transport. It validates that no out-of-scope bits are set.
func packSampleFlags(sf uint32) (uint64, error) {
	if sf&^bmfSampleFlagsSupportedMask != 0 {
		return 0, fmt.Errorf("locmafv02: sample_flags=0x%08x sets bits outside LOCMAF v0.2 scope", sf)
	}
	nonSync := (sf >> 16) & 0x1
	dependsOn := (sf >> bmfSampleFlagsDependsOnLo) & 0x3
	dependedOn := (sf >> bmfSampleFlagsDependedOnLo) & 0x3
	return uint64(nonSync) | uint64(dependsOn)<<1 | uint64(dependedOn)<<3, nil
}

// unpackSampleFlags expands a 5-bit transport back to a 32-bit ISO
// BMFF sample_flags.
func unpackSampleFlags(v uint64) uint32 {
	nonSync := uint32(v & 0x1)
	dependsOn := uint32((v >> 1) & 0x3)
	dependedOn := uint32((v >> 3) & 0x3)
	return (nonSync << 16) | (dependsOn << 24) | (dependedOn << 22)
}
