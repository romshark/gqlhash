package parser

import "testing"

// TestNoopHash covers the discard hash used to parse-but-not-hash ignored
// sections. Its Reset/Sum are never invoked by the parser itself, so they need
// this white-box test (noopHash is unexported).
func TestNoopHash(t *testing.T) {
	var h noopHash
	h.Reset()
	if n, err := h.Write([]byte("abc")); n != 3 || err != nil {
		t.Fatalf("Write: n=%d err=%v", n, err)
	}
	if got := string(h.Sum([]byte("x"))); got != "x" {
		t.Fatalf("Sum: %q", got)
	}
}
