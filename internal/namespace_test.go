package internal

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNamespaceTuple(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"cmsf/clear", []string{"cmsf", "clear"}},
		{"msf/clear", []string{"msf", "clear"}},
		{"moq-mi/clear", []string{"moq-mi", "clear"}},
		{"cmsf/eccp-cbcs", []string{"cmsf", "eccp-cbcs"}},
		{"moq-test/interop", []string{"moq-test", "interop"}},
		{"single", []string{"single"}},
		{"a/b/c", []string{"a", "b", "c"}},
		// A field must carry at least one byte, so empty ones are dropped
		// rather than encoded (draft-ietf-moq-transport-18, Section 2.4.1).
		{"/cmsf/clear", []string{"cmsf", "clear"}},
		{"cmsf//clear", []string{"cmsf", "clear"}},
		{"cmsf/clear/", []string{"cmsf", "clear"}},
		{"", []string{}},
		{"/", []string{}},
	}
	for _, c := range cases {
		require.Equal(t, c.want, NamespaceTuple(c.in), "NamespaceTuple(%q)", c.in)
	}
}

func TestNamespaceStringRoundTrip(t *testing.T) {
	for _, s := range []string{"cmsf/clear", "moq-test/interop", "single", "a/b/c"} {
		require.Equal(t, s, NamespaceString(NamespaceTuple(s)))
	}
}
