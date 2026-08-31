package qlogfilter_test

import (
	"bytes"
	"testing"

	"github.com/Eyevinn/moqlivemock/internal/qlogfilter"
	"github.com/stretchr/testify/require"
)

const (
	headerRecord  = "\x1e{\"file_schema\":\"urn:ietf:params:qlog:file:sequential\",\"title\":\"MoQ QLOG\"}\n"
	controlRecord = "\x1e{\"time\":1.2,\"name\":\"moqt:control_message_created\",\"data\":{}}\n"
	objectRecord  = "\x1e{\"time\":1.3,\"name\":\"moqt:subgroup_object_created\",\"data\":{}}\n"
	sgHeadRecord  = "\x1e{\"time\":1.4,\"name\":\"moqt:subgroup_header_parsed\",\"data\":{}}\n"
	fetchRecord   = "\x1e{\"time\":1.5,\"name\":\"moqt:fetch_object_parsed\",\"data\":{}}\n"
)

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

func TestFilterKeepsSelectedClasses(t *testing.T) {
	keep, err := qlogfilter.ParseClasses("control,fetch")
	require.NoError(t, err)
	require.NotNil(t, keep)

	var buf bytes.Buffer
	w := qlogfilter.New(&buf, keep)
	for _, record := range []string{
		headerRecord, controlRecord, objectRecord, sgHeadRecord, fetchRecord,
	} {
		n, err := w.Write([]byte(record))
		require.NoError(t, err)
		require.Equal(t, len(record), n, "a dropped record still reports full length")
	}

	out := buf.String()
	require.Contains(t, out, "file_schema", "the header record always passes")
	require.Contains(t, out, "control_message_created")
	require.Contains(t, out, "fetch_object_parsed")
	require.NotContains(t, out, "subgroup_object_created")
	require.NotContains(t, out, "subgroup_header_parsed")
}

func TestNewWithoutPredicateIsPassthrough(t *testing.T) {
	var buf bytes.Buffer
	w := qlogfilter.New(&buf, nil)
	_, err := w.Write([]byte(objectRecord))
	require.NoError(t, err)
	require.Equal(t, objectRecord, buf.String())
}
