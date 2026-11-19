package host

import (
	"bytes"
	"sync"
)

// LimitedBuffer is a concurrency-safe io.Writer that retains at most maxBytes
// of written output and marks itself truncated when excess bytes arrive.
type LimitedBuffer struct {
	mu        sync.Mutex
	maxBytes  int
	buf       bytes.Buffer
	truncated bool
}

// NewLimitedBuffer creates a LimitedBuffer bounded to maxBytes.
func NewLimitedBuffer(maxBytes int) *LimitedBuffer {
	if maxBytes < 0 {
		maxBytes = 0
	}
	return &LimitedBuffer{maxBytes: maxBytes}
}

// Write appends up to the remaining capacity in p and discards any excess bytes
// without failing so subprocess pipes never block on full buffers.
func (b *LimitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	remaining := b.maxBytes - b.buf.Len()
	if remaining <= 0 {
		if len(p) > 0 {
			b.truncated = true
		}
		return len(p), nil
	}
	if len(p) > remaining {
		_, _ = b.buf.Write(p[:remaining])
		b.truncated = true
		return len(p), nil
	}
	_, _ = b.buf.Write(p)
	return len(p), nil
}

// Bytes returns a copy of the retained bytes.
func (b *LimitedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]byte, b.buf.Len())
	copy(out, b.buf.Bytes())
	return out
}

// String returns the retained content as a string.
func (b *LimitedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// Truncated reports whether any written bytes exceeded maxBytes.
func (b *LimitedBuffer) Truncated() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.truncated
}
