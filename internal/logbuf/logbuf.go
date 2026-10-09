// Package logbuf keeps the most recent server log lines in memory so they
// can be read over the API and the MCP connector without shell access.
package logbuf

import (
	"bytes"
	"io"
	"strings"
	"sync"
)

// Buffer is a ring of log lines. It is also an io.Writer, so it can sit
// behind a slog handler alongside stdout.
type Buffer struct {
	mu    sync.Mutex
	lines []string
	max   int
	part  bytes.Buffer
}

// New returns a buffer that retains the last max lines.
func New(max int) *Buffer { return &Buffer{max: max} }

// Write splits p into lines and appends them.
func (b *Buffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.part.Write(p)
	for {
		data := b.part.Bytes()
		i := bytes.IndexByte(data, '\n')
		if i < 0 {
			break
		}
		line := strings.TrimRight(string(data[:i]), "\r")
		b.part.Next(i + 1)
		b.lines = append(b.lines, line)
		if len(b.lines) > b.max {
			b.lines = b.lines[len(b.lines)-b.max:]
		}
	}
	return len(p), nil
}

// Lines returns up to n most recent lines, oldest first.
func (b *Buffer) Lines(n int) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if n <= 0 || n > len(b.lines) {
		n = len(b.lines)
	}
	out := make([]string, n)
	copy(out, b.lines[len(b.lines)-n:])
	return out
}

// Tee returns a writer that writes to both w and the buffer.
func (b *Buffer) Tee(w io.Writer) io.Writer { return io.MultiWriter(w, b) }
