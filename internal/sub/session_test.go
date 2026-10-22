package sub

import (
	"testing"
	"testing/synctest"

	"github.com/Eyevinn/moqlivemock/internal/testconn"
	"github.com/Eyevinn/moqtransport"
	"github.com/stretchr/testify/require"
)

// The subscriber sends the PATH and AUTHORITY setup options it is given, as
// mlmsub does for a moqt:// -addr, and neither when they are empty.
func TestStartSessionSetupOptions(t *testing.T) {
	for _, tc := range []struct {
		name, path, authority string
	}{
		{"moqt URL", "/anon", "cdn.moq.dev:443"},
		{"none", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				qlog := &testconn.QlogBuffer{}
				h := &Handler{Logfh: qlog, Path: tc.path, Authority: tc.authority}
				sConn, cConn := testconn.Pair()
				server := &moqtransport.Session{Implementation: "sub-test-server"}
				errs := make(chan error, 1)
				go func() { errs <- server.Run(t.Context(), sConn) }()

				session, err := h.startSession(t.Context(), cConn)
				require.NoError(t, err)
				require.NoError(t, <-errs)
				synctest.Wait()

				if tc.path == "" {
					require.False(t, qlog.HasAnySetupOption(testconn.SetupOptionPath))
					require.False(t, qlog.HasAnySetupOption(testconn.SetupOptionAuthority))
				} else {
					require.True(t, qlog.HasSetupOption(testconn.SetupOptionPath, tc.path))
					require.True(t, qlog.HasSetupOption(testconn.SetupOptionAuthority, tc.authority))
				}
				_ = session.Close(moqtransport.SessionErrorNoError, "test over")
				_ = server.Close(moqtransport.SessionErrorNoError, "test over")
			})
		})
	}
}
