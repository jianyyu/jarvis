package sidecar

import (
	"bytes"
	"sync"
)

// RingBuffer is a thread-safe circular buffer that stores terminal output lines.
type RingBuffer struct {
	mu    sync.Mutex
	lines [][]byte
	cap   int
	head  int
	count int
	// partial holds an incomplete line (no trailing newline yet)
	partial []byte
}

func NewRingBuffer(capacity int) *RingBuffer {
	return &RingBuffer{
		lines: make([][]byte, capacity),
		cap:   capacity,
	}
}

// Write appends data to the ring buffer, splitting on newlines.
func (rb *RingBuffer) Write(p []byte) {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	data := append(rb.partial, p...)
	rb.partial = nil

	for {
		idx := bytes.IndexByte(data, '\n')
		if idx < 0 {
			rb.partial = make([]byte, len(data))
			copy(rb.partial, data)
			return
		}
		line := make([]byte, idx+1)
		copy(line, data[:idx+1])
		rb.addLine(line)
		data = data[idx+1:]
	}
}

func (rb *RingBuffer) addLine(line []byte) {
	idx := (rb.head + rb.count) % rb.cap
	rb.lines[idx] = line
	if rb.count < rb.cap {
		rb.count++
	} else {
		rb.head = (rb.head + 1) % rb.cap
	}
}

// LastN returns the last n lines (or fewer if not enough stored).
func (rb *RingBuffer) LastN(n int) [][]byte {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	if n > rb.count {
		n = rb.count
	}
	result := make([][]byte, n)
	start := (rb.head + rb.count - n) % rb.cap
	for i := 0; i < n; i++ {
		idx := (start + i) % rb.cap
		result[i] = rb.lines[idx]
	}
	return result
}

// Len returns the number of lines stored.
func (rb *RingBuffer) Len() int {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	return rb.count
}

// Bytes returns all stored content as a single byte slice (for buffer catch-up on attach).
func (rb *RingBuffer) Bytes() []byte {
	lines := rb.LastN(rb.Len())
	var buf bytes.Buffer
	for _, l := range lines {
		buf.Write(l)
	}
	return buf.Bytes()
}
