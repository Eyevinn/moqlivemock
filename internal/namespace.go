package internal

import "strings"

// NamespaceTuple converts the slash-separated string form of a track
// namespace into the tuple that goes on the wire.
//
// Section 2.4.1 of draft-ietf-moq-transport-18 makes Track Namespace an
// ordered set of length-prefixed fields, and Section 8.4 matches those fields
// one at a time: a namespace sent as the single field "cmsf/clear" never
// matches the prefix ("cmsf"), while the two fields ("cmsf", "clear") do.
// Sending one slash-bearing field therefore costs every relay and every
// namespace subscriber the ability to match on "cmsf" alone.
//
// MSF writes the same tuple as one slash-joined string in the catalog (the
// "namespace" member of Section 5.2.2, whose own example reads
// "conference.example.com/conference123/alice"), so "/" is the separator on
// both sides of the catalog boundary and the two forms round-trip.
//
// Empty fields are dropped: Section 2.4.1 requires each field to carry at
// least one byte, so "cmsf//clear" and "/cmsf/clear" both yield
// ("cmsf", "clear") rather than a field that cannot be encoded.
func NamespaceTuple(s string) []string {
	parts := strings.Split(s, "/")
	tuple := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			tuple = append(tuple, p)
		}
	}
	return tuple
}

// NamespaceString is the inverse of NamespaceTuple: the slash-joined form
// used in catalogs, log lines and command-line flags.
func NamespaceString(tuple []string) string {
	return strings.Join(tuple, "/")
}
