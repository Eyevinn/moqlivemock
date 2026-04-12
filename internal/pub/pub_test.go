package pub

import (
	"testing"

	"github.com/Eyevinn/moqlivemock/internal"
)

func TestTupleEqual(t *testing.T) {
	tests := []struct {
		name string
		a, b []string
		want bool
	}{
		{"both empty", nil, nil, true},
		{"equal", []string{"a", "b"}, []string{"a", "b"}, true},
		{"different length", []string{"a"}, []string{"a", "b"}, false},
		{"different values", []string{"a", "b"}, []string{"a", "c"}, false},
		{"single equal", []string{"x"}, []string{"x"}, true},
		{"one nil", nil, []string{"a"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tupleEqual(tt.a, tt.b); got != tt.want {
				t.Errorf("tupleEqual(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestIsInteropNamespace(t *testing.T) {
	tests := []struct {
		name string
		ns   []string
		want bool
	}{
		{"interop namespace", []string{"moq-test", "interop"}, true},
		{"wrong prefix", []string{"other", "interop"}, false},
		{"wrong suffix", []string{"moq-test", "other"}, false},
		{"too short", []string{"moq-test"}, false},
		{"too long", []string{"moq-test", "interop", "extra"}, false},
		{"empty", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isInteropNamespace(tt.ns); got != tt.want {
				t.Errorf("isInteropNamespace(%v) = %v, want %v", tt.ns, got, tt.want)
			}
		})
	}
}

func TestFindNamespace(t *testing.T) {
	catalog := &internal.Catalog{}
	h := &Handler{
		Namespaces: []NamespaceEntry{
			{Namespace: []string{"cmsf", "clear"}, Catalog: catalog},
			{Namespace: []string{"cmsf", "drm-cenc"}, Catalog: catalog},
		},
	}

	tests := []struct {
		name  string
		ns    []string
		found bool
	}{
		{"found first", []string{"cmsf", "clear"}, true},
		{"found second", []string{"cmsf", "drm-cenc"}, true},
		{"not found", []string{"cmsf", "other"}, false},
		{"empty", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := h.findNamespace(tt.ns)
			if tt.found && result == nil {
				t.Errorf("findNamespace(%v) = nil, want non-nil", tt.ns)
			}
			if !tt.found && result != nil {
				t.Errorf("findNamespace(%v) = %v, want nil", tt.ns, result)
			}
		})
	}
}
