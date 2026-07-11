package core

import (
	"strings"
	"testing"
)

// A merged-tree blob for a text conflict carries standard conflict markers. The
// hunk extractor must find each <<<<<<< … >>>>>>> region and count them.
func TestConflictHunks(t *testing.T) {
	blob := []byte("prefix\n" +
		"<<<<<<< base\n" +
		"ours line\n" +
		"=======\n" +
		"theirs line\n" +
		">>>>>>> topic\n" +
		"middle\n" +
		"<<<<<<< base\n" +
		"ours 2\n" +
		"=======\n" +
		"theirs 2\n" +
		">>>>>>> topic\n" +
		"suffix\n")
	hunks, count := ConflictHunks(blob)
	if count != 2 {
		t.Fatalf("hunk count = %d, want 2", count)
	}
	if len(hunks) != 2 {
		t.Fatalf("len(hunks) = %d, want 2", len(hunks))
	}
	if !strings.HasPrefix(hunks[0], "<<<<<<< base") || !strings.Contains(hunks[0], "ours line") || !strings.HasSuffix(strings.TrimRight(hunks[0], "\n"), ">>>>>>> topic") {
		t.Errorf("hunk[0] not a full region: %q", hunks[0])
	}
	if strings.Contains(hunks[0], "ours 2") {
		t.Errorf("hunk[0] leaked into the second region: %q", hunks[0])
	}
}

// No markers → zero hunks (a clean-but-both-touched file, or binary).
func TestConflictHunks_None(t *testing.T) {
	if hunks, count := ConflictHunks([]byte("just\ntext\n")); count != 0 || len(hunks) != 0 {
		t.Errorf("no-marker blob returned %d hunks", count)
	}
}

// An unterminated marker (defensive: truncated/odd input) still counts and
// captures to end of input rather than panicking or dropping it.
func TestConflictHunks_Unterminated(t *testing.T) {
	blob := []byte("<<<<<<< base\nours\n=======\ntheirs\n")
	hunks, count := ConflictHunks(blob)
	if count != 1 || len(hunks) != 1 {
		t.Fatalf("unterminated: count=%d hunks=%d", count, len(hunks))
	}
	if !strings.Contains(hunks[0], "theirs") {
		t.Errorf("unterminated hunk lost trailing content: %q", hunks[0])
	}
}

// BoundedSample returns the first hunk, capped to maxLines with a trimmed
// marker, and reports truncation. A short hunk passes through verbatim.
func TestBoundedSample(t *testing.T) {
	short := []string{"<<<<<<< base\nours\n=======\ntheirs\n>>>>>>> topic\n"}
	s, trunc := BoundedSample(short, 20)
	if trunc {
		t.Errorf("short hunk marked truncated")
	}
	if s != short[0] {
		t.Errorf("short hunk altered: %q", s)
	}

	var big strings.Builder
	big.WriteString("<<<<<<< base\n")
	for i := 0; i < 100; i++ {
		big.WriteString("ours line\n")
	}
	big.WriteString(">>>>>>> topic\n")
	s, trunc = BoundedSample([]string{big.String()}, 10)
	if !trunc {
		t.Fatalf("large hunk not marked truncated")
	}
	if strings.Count(s, "\n") > 12 { // ~10 kept lines + trimmed marker, generous ceiling
		t.Errorf("bounded sample kept too many lines:\n%s", s)
	}
	if !strings.Contains(s, "trimmed") {
		t.Errorf("bounded sample lacks a trimmed marker: %q", s)
	}
	if !strings.Contains(s, "<<<<<<< base") || !strings.Contains(s, ">>>>>>> topic") {
		t.Errorf("bounded sample dropped the conflict markers: %q", s)
	}
}

// No hunks → empty sample, not truncated.
func TestBoundedSample_Empty(t *testing.T) {
	if s, trunc := BoundedSample(nil, 20); s != "" || trunc {
		t.Errorf("empty hunks: sample=%q truncated=%v", s, trunc)
	}
}

// BoundedSampleAll (drill-down) concatenates every hunk, unlike BoundedSample
// which stops at the first, and bounds the whole thing.
func TestBoundedSampleAll(t *testing.T) {
	hunks := []string{
		"<<<<<<< base\nours1\n=======\ntheirs1\n>>>>>>> topic\n",
		"<<<<<<< base\nours2\n=======\ntheirs2\n>>>>>>> topic\n",
	}
	s, trunc := BoundedSampleAll(hunks, 400)
	if trunc {
		t.Errorf("small drill-down marked truncated")
	}
	if strings.Count(s, "<<<<<<<") != 2 || strings.Count(s, ">>>>>>>") != 2 {
		t.Errorf("BoundedSampleAll dropped a hunk: %q", s)
	}
	if !strings.Contains(s, "ours1") || !strings.Contains(s, "ours2") {
		t.Errorf("BoundedSampleAll lost hunk content: %q", s)
	}
	if s2, _ := BoundedSampleAll(nil, 400); s2 != "" {
		t.Errorf("empty hunks: %q", s2)
	}
}
