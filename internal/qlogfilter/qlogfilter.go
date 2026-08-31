// Package qlogfilter narrows a qlog JSON-SEQ stream to selected event
// classes.
//
// qlog has no notion of levels -- it is an event log, and both the qlog
// library and moqtransport write every event unconditionally. moqtransport
// also takes the concrete *qlog.Logger, so the io.Writer handed to it is the
// application's one seam: the slog JSON handler underneath emits exactly one
// record per Write, and this writer drops the records whose event name
// belongs to an unselected class. The file header (the only record without a
// name) always passes.
package qlogfilter

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// classes maps a selectable class to the event-name prefixes it covers, with
// the "moqt:" category already stripped. The object class covers subgroup
// headers and objects; datagram and fetch cover their status/header variants
// through the shared prefix.
var classes = map[string][]string{
	"control":  {"control_message_"},
	"stream":   {"stream_type_set"},
	"object":   {"subgroup_"},
	"datagram": {"object_datagram_"},
	"fetch":    {"fetch_"},
}

// ClassNames returns the selectable class names, sorted.
func ClassNames() []string {
	names := make([]string, 0, len(classes))
	for name := range classes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ParseClasses turns a comma-separated class list into a keep predicate over
// full event names (e.g. "moqt:subgroup_object_created"). The spec "all" (or
// an empty one) selects everything and returns a nil predicate, meaning no
// filtering is needed at all.
func ParseClasses(spec string) (func(name string) bool, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" || spec == "all" {
		return nil, nil
	}
	var prefixes []string
	for class := range strings.SplitSeq(spec, ",") {
		class = strings.TrimSpace(class)
		p, ok := classes[class]
		if !ok {
			return nil, fmt.Errorf("unknown qlog event class %q (want all or any of %s)",
				class, strings.Join(ClassNames(), ","))
		}
		prefixes = append(prefixes, p...)
	}
	return func(name string) bool {
		// Strip the category ("moqt:...") before matching.
		if i := strings.IndexByte(name, ':'); i >= 0 {
			name = name[i+1:]
		}
		for _, p := range prefixes {
			if strings.HasPrefix(name, p) {
				return true
			}
		}
		return false
	}, nil
}

// recordSeparator starts every JSON-SEQ record (RFC 7464).
var recordSeparator = []byte{0x1e}

// New wraps w so that qlog records failing the keep predicate are dropped.
// A nil keep returns w unchanged.
func New(w io.Writer, keep func(name string) bool) io.Writer {
	if keep == nil {
		return w
	}
	return &writer{w: w, keep: keep}
}

type writer struct {
	w    io.Writer
	keep func(string) bool
}

func (f *writer) Write(p []byte) (int, error) {
	var record struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(bytes.TrimPrefix(p, recordSeparator), &record); err == nil &&
		record.Name != "" && !f.keep(record.Name) {
		return len(p), nil
	}
	return f.w.Write(p)
}
