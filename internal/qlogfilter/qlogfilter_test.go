package qlogfilter_test

import (
	"log/slog"
	"testing"

	"github.com/Eyevinn/moqlivemock/internal/qlogfilter"
	"github.com/mengelbart/qlog"
	"github.com/stretchr/testify/require"
)

// stubEvent is a minimal qlog.Event for exercising the filter.
type stubEvent struct{ name string }

func (e stubEvent) Category() string     { return "moqt" }
func (e stubEvent) Name() string         { return e.name }
func (e stubEvent) LogValue() slog.Value { return slog.StringValue(e.name) }

// recorder is a QlogHandler that remembers what reached it.
type recorder struct{ names []string }

func (r *recorder) Log(e qlog.Event) { r.names = append(r.names, e.Name()) }

func TestParseClassesAll(t *testing.T) {
	for _, spec := range []string{"", "all"} {
		keep, err := qlogfilter.ParseClasses(spec)
		require.NoError(t, err)
		require.Nil(t, keep)
	}
}

func TestParseClassesUnknown(t *testing.T) {
	_, err := qlogfilter.ParseClasses("control,bogus")
	require.ErrorContains(t, err, "bogus")
}

func TestHandlerKeepsSelectedClasses(t *testing.T) {
	keep, err := qlogfilter.ParseClasses("control,fetch")
	require.NoError(t, err)
	require.NotNil(t, keep)

	sink := &recorder{}
	h := qlogfilter.Wrap(sink, keep)
	for _, name := range []string{
		"control_message_created",
		"subgroup_object_created",
		"subgroup_header_parsed",
		"fetch_object_parsed",
		"object_datagram_created",
		"stream_type_set",
	} {
		h.Log(stubEvent{name: name})
	}

	require.Equal(t, []string{"control_message_created", "fetch_object_parsed"}, sink.names)
}

func TestWrapWithoutPredicateIsPassthrough(t *testing.T) {
	sink := &recorder{}
	h := qlogfilter.Wrap(sink, nil)
	require.Equal(t, sink, h, "a nil predicate must not add a layer")
}
