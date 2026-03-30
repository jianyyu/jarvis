package sidecar

import (
	"testing"
)

func TestRingBufferBasic(t *testing.T) {
	rb := NewRingBuffer(5)
	rb.Write([]byte("line1\nline2\nline3\n"))

	if rb.Len() != 3 {
		t.Errorf("len: got %d, want 3", rb.Len())
	}

	lines := rb.LastN(2)
	if len(lines) != 2 {
		t.Fatalf("lastN: got %d lines, want 2", len(lines))
	}
	if string(lines[0]) != "line2\n" {
		t.Errorf("got %q, want %q", lines[0], "line2\n")
	}
	if string(lines[1]) != "line3\n" {
		t.Errorf("got %q, want %q", lines[1], "line3\n")
	}
}

func TestRingBufferWraparound(t *testing.T) {
	rb := NewRingBuffer(3)
	rb.Write([]byte("a\nb\nc\nd\ne\n"))

	if rb.Len() != 3 {
		t.Errorf("len: got %d, want 3", rb.Len())
	}

	lines := rb.LastN(3)
	want := []string{"c\n", "d\n", "e\n"}
	for i, l := range lines {
		if string(l) != want[i] {
			t.Errorf("line %d: got %q, want %q", i, l, want[i])
		}
	}
}

func TestRingBufferPartialLines(t *testing.T) {
	rb := NewRingBuffer(10)
	rb.Write([]byte("hello "))
	rb.Write([]byte("world\n"))

	if rb.Len() != 1 {
		t.Errorf("len: got %d, want 1", rb.Len())
	}
	lines := rb.LastN(1)
	if string(lines[0]) != "hello world\n" {
		t.Errorf("got %q, want %q", lines[0], "hello world\n")
	}
}

func TestRingBufferBytes(t *testing.T) {
	rb := NewRingBuffer(10)
	rb.Write([]byte("a\nb\nc\n"))
	got := string(rb.Bytes())
	want := "a\nb\nc\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRingBufferEmpty(t *testing.T) {
	rb := NewRingBuffer(10)
	if rb.Len() != 0 {
		t.Errorf("len: got %d, want 0", rb.Len())
	}
	lines := rb.LastN(5)
	if len(lines) != 0 {
		t.Errorf("lastN: got %d, want 0", len(lines))
	}
}
