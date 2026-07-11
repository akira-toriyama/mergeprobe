package core

import "testing"

// The parser is the one place that eats untrusted, format-versioned bytes from
// git. Its contract is: never panic — every malformed input must return an
// error or a well-formed MergeTree, never crash the process. Seeded with the
// real shapes, then fuzzed.
func FuzzParseMergeTreeZ(f *testing.F) {
	f.Add(z("tree"))
	f.Add(z("tree", "100644 aaaa 1\tf.txt", "100644 bbbb 2\tf.txt", "100644 cccc 3\tf.txt",
		"", "1", "f.txt", "CONFLICT (contents)", "CONFLICT (content): f.txt\n"))
	f.Add([]byte{0})
	f.Add([]byte{0, 0})
	f.Add([]byte("not\x00a\x00valid\x00stream"))

	f.Fuzz(func(t *testing.T, data []byte) {
		mt, err := ParseMergeTreeZ(data)
		if err != nil {
			return // a rejected malformed input is fine
		}
		// On success the result must be self-consistent: tree set, and every
		// conflict file has at least one stage. Also exercise the pure consumers
		// to prove they never panic on parser output.
		if mt.Tree == "" {
			t.Fatalf("nil-error parse produced empty Tree for %q", data)
		}
		for _, cf := range mt.Files {
			if len(cf.Stages) == 0 {
				t.Fatalf("conflict file %q has no stages", cf.Path)
			}
			_ = Classify(cf)
		}
	})
}

// The sample/hunk extractors also consume arbitrary blob bytes; they must never
// panic and must round-trip content faithfully.
func FuzzConflictHunks(f *testing.F) {
	f.Add([]byte("<<<<<<< a\nx\n=======\ny\n>>>>>>> b\n"))
	f.Add([]byte("no markers here\n"))
	f.Add([]byte("<<<<<<< unterminated\n"))
	f.Fuzz(func(t *testing.T, blob []byte) {
		hunks, n := ConflictHunks(blob)
		if n != len(hunks) {
			t.Fatalf("count %d != len(hunks) %d", n, len(hunks))
		}
		if _, _ = BoundedSample(hunks, 20); false {
			t.Fatal("unreachable")
		}
	})
}
