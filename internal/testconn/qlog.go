package testconn

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
)

// QlogBuffer collects the qlog a session writes from its own goroutines, so a
// test can read what went over the wire. Use it as a handler's qlog writer.
type QlogBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *QlogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *QlogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// Setup option types, draft-ietf-moq-transport-18 Section 10.3.1.
const (
	SetupOptionPath      = 0x01
	SetupOptionAuthority = 0x05
)

// HasSetupOption reports whether a SETUP in the qlog carried the option with
// this value. moqtransport renders an option as {key: K, value: '[bytes]'}.
func (b *QlogBuffer) HasSetupOption(key int, value string) bool {
	return strings.Contains(b.String(), fmt.Sprintf("{key: %d, value: '%v'}", key, []byte(value)))
}

// HasAnySetupOption reports whether a SETUP in the qlog carried the option at
// all, whatever its value.
func (b *QlogBuffer) HasAnySetupOption(key int) bool {
	return strings.Contains(b.String(), fmt.Sprintf("{key: %d, value: ", key))
}
