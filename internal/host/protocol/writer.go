package protocol

import (
	"io"
	"sync"
)

// SafeWriter wraps Writer with a mutex so multiple goroutines can emit events
// to stdout without interleaving JSON lines.
type SafeWriter struct {
	mu sync.Mutex
	w  *Writer
}

// NewSafeWriter returns a SafeWriter writing to w.
func NewSafeWriter(w io.Writer) *SafeWriter {
	return &SafeWriter{w: NewWriter(w)}
}

// Write serialises v as one NDJSON line, safe for concurrent use.
func (sw *SafeWriter) Write(v any) error {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	return sw.w.Write(v)
}
