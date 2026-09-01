// Package qlogfilter narrows a session's qlog to selected event classes.
//
// qlog has no notion of levels -- it is an event log, and moqtransport
// writes every event unconditionally -- so verbosity control means selecting
// event classes. Handler implements moqtransport.QlogHandler and drops
// unselected events before anything is serialized. The qlog file header is
// written by qlog.NewQLOGHandler itself when the logger is built, so it is
// never subject to filtering.
package qlogfilter

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Eyevinn/moqtransport"
	"github.com/mengelbart/qlog"
)

// classes maps a selectable class to the event-name prefixes it covers. The
// object class covers subgroup headers and objects; datagram and fetch cover
// their status/header variants through the shared prefix.
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
// qlog events. The spec "all" (or an empty one) selects everything and
// returns a nil predicate, meaning no filtering is needed at all.
func ParseClasses(spec string) (func(qlog.Event) bool, error) {
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
	return func(e qlog.Event) bool {
		name := e.Name()
		for _, p := range prefixes {
			if strings.HasPrefix(name, p) {
				return true
			}
		}
		return false
	}, nil
}

// Handler filters the events bound for Next.
type Handler struct {
	Next moqtransport.QlogHandler
	Keep func(qlog.Event) bool
}

func (h Handler) Log(e qlog.Event) {
	if h.Keep == nil || h.Keep(e) {
		h.Next.Log(e)
	}
}

// Wrap returns next filtered by keep. A nil keep selects everything and
// returns next unchanged.
func Wrap(next moqtransport.QlogHandler, keep func(qlog.Event) bool) moqtransport.QlogHandler {
	if keep == nil {
		return next
	}
	return Handler{Next: next, Keep: keep}
}
