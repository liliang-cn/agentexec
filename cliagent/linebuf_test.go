package cliagent

import (
	"reflect"
	"testing"
)

func TestLineBufferCompleteLines(t *testing.T) {
	var lb LineBuffer
	got := lb.Feed([]byte("a\nb\nc\n"))
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Feed = %v, want %v", got, want)
	}
}

func TestLineBufferBuffersPartialTail(t *testing.T) {
	var lb LineBuffer
	got := lb.Feed([]byte("a\nb\npart"))
	want := []string{"a", "b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Feed = %v, want %v (partial 'part' must be buffered)", got, want)
	}
	// The buffered tail is completed by the next feed.
	got2 := lb.Feed([]byte("ial\n"))
	want2 := []string{"partial"}
	if !reflect.DeepEqual(got2, want2) {
		t.Fatalf("Feed = %v, want %v", got2, want2)
	}
}

func TestLineBufferStripsCarriageReturn(t *testing.T) {
	var lb LineBuffer
	got := lb.Feed([]byte("a\r\nb\r\n"))
	want := []string{"a", "b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Feed = %v, want %v (\\r must be stripped)", got, want)
	}
}

func TestLineBufferFlushReturnsTail(t *testing.T) {
	var lb LineBuffer
	lb.Feed([]byte("a\ntail"))
	got := lb.Flush()
	want := []string{"tail"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Flush = %v, want %v", got, want)
	}
	// Second flush is empty.
	if got := lb.Flush(); len(got) != 0 {
		t.Fatalf("second Flush = %v, want empty", got)
	}
}

func TestLineBufferFlushEmpty(t *testing.T) {
	var lb LineBuffer
	if got := lb.Flush(); len(got) != 0 {
		t.Fatalf("Flush of empty buffer = %v, want empty", got)
	}
}

func TestLineBufferEmptyLinesPreserved(t *testing.T) {
	var lb LineBuffer
	got := lb.Feed([]byte("a\n\nb\n"))
	want := []string{"a", "", "b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Feed = %v, want %v", got, want)
	}
}
