package core

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// A machine consumer must be able to index conflicts[] and both_touched_clean[]
// unconditionally, so nil slices normalize to [] (never null), and both lists
// come out sorted by path for stable diffs.
func TestReportNormalize(t *testing.T) {
	r := Report{
		Base:  "origin/main",
		Topic: "feature-x",
		Conflicts: []Conflict{
			{Path: "z.txt", Class: ClassBothModified},
			{Path: "a.txt", Class: ClassAddAdd},
		},
		BothTouchedClean: []string{"y.go", "b.go"},
	}
	r.Normalize()

	if got := []string{r.Conflicts[0].Path, r.Conflicts[1].Path}; got[0] != "a.txt" || got[1] != "z.txt" {
		t.Errorf("conflicts not sorted by path: %v", got)
	}
	if r.BothTouchedClean[0] != "b.go" || r.BothTouchedClean[1] != "y.go" {
		t.Errorf("both_touched_clean not sorted: %v", r.BothTouchedClean)
	}

	empty := Report{Base: "a", Topic: "b"}
	empty.Normalize()
	if empty.Conflicts == nil || empty.BothTouchedClean == nil {
		t.Fatal("Normalize left a nil slice; JSON would render null")
	}
	out, err := json.Marshal(empty)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(out)
	if !strings.Contains(s, `"conflicts":[]`) {
		t.Errorf("conflicts did not render as []: %s", s)
	}
	if !strings.Contains(s, `"both_touched_clean":[]`) {
		t.Errorf("both_touched_clean did not render as []: %s", s)
	}
}

// The Conflict JSON shape is a stable contract; verify field names/omitempty.
func TestConflictJSONShape(t *testing.T) {
	// A marker-free sample keeps this a struct-shape assertion; the EscapeHTML
	// (false) guarantee for real conflict markers lives in the cli funnel tests.
	c := Conflict{Path: "app/Kconfig", Class: ClassBothModified, Hunks: 2, Sample: "ours vs theirs"}
	out, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{`"path":"app/Kconfig"`, `"class":"both-modified"`, `"hunks":2`, `"sample":"ours vs theirs"`} {
		if !bytes.Contains(out, []byte(want)) {
			t.Errorf("missing %s in %s", want, out)
		}
	}
	// binary/truncated are omitempty: absent when false.
	if bytes.Contains(out, []byte("binary")) || bytes.Contains(out, []byte("truncated")) {
		t.Errorf("false flags leaked into JSON: %s", out)
	}
}
