package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestContentNamespace pins how -nsprefix composes a content namespace. The
// prefix is what lets a subscriber behind a shared relay ask for one
// publisher, so it has to become its own field (or fields) rather than being
// glued onto the first one.
func TestContentNamespace(t *testing.T) {
	for _, c := range []struct {
		name   string
		prefix string
		ns     string
		want   []string
	}{
		{"default prefix", defaultNamespacePrefix, "cmsf/clear", []string{"mlm", "cmsf", "clear"}},
		{"loc namespace", "mlm", "msf/clear", []string{"mlm", "msf", "clear"}},
		{"moq-mi namespace", "mlm", "moq-mi/clear", []string{"mlm", "moq-mi", "clear"}},
		{"protection suffix survives", "mlm", "cmsf/drm-cbcs", []string{"mlm", "cmsf", "drm-cbcs"}},
		{"per-deployment prefix", "demo", "cmsf/clear", []string{"demo", "cmsf", "clear"}},
		// An empty field cannot be encoded, so an empty prefix contributes
		// none and the namespace is published unprefixed.
		{"empty prefix adds no field", "", "cmsf/clear", []string{"cmsf", "clear"}},
		{"multi-field prefix", "eyevinn/demo", "cmsf/clear", []string{"eyevinn", "demo", "cmsf", "clear"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, contentNamespace(c.prefix, c.ns))
		})
	}
}
