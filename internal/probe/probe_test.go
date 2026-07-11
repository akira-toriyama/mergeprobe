package probe

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/akira-toriyama/mergeprobe/internal/core"
)

// z builds -z merge-tree bytes the way git does (every field NUL-terminated),
// so the fake feeds Run exactly what the real adapter would.
func z(fields ...string) []byte {
	var b []byte
	for _, f := range fields {
		b = append(b, f...)
		b = append(b, 0)
	}
	return b
}

// fakeGit is an in-memory Git port. Each behavior is a function field so a test
// overrides only what it exercises; unset methods fail loudly.
type fakeGit struct {
	resolve   func(ref string) (string, error)
	defBase   func() (string, error)
	mergeTree func(base, topic string) ([]byte, bool, error)
	mergeBase func(a, b string) (string, bool, error)
	diffNames func(from, to string) ([]string, error)
	showBlob  func(treeish, path string) ([]byte, error)
}

func (f fakeGit) ResolveCommit(_ context.Context, ref string) (string, error) {
	if f.resolve != nil {
		return f.resolve(ref)
	}
	return ref + "-oid", nil
}
func (f fakeGit) DefaultBase(context.Context) (string, error) {
	if f.defBase != nil {
		return f.defBase()
	}
	return "origin/main", nil
}
func (f fakeGit) MergeTree(_ context.Context, base, topic string) ([]byte, bool, error) {
	return f.mergeTree(base, topic)
}
func (f fakeGit) MergeBase(_ context.Context, a, b string) (string, bool, error) {
	if f.mergeBase != nil {
		return f.mergeBase(a, b)
	}
	return "mergebaseoid0000000000000000000000000000", true, nil
}
func (f fakeGit) DiffNames(_ context.Context, from, to string) ([]string, error) {
	if f.diffNames != nil {
		return f.diffNames(from, to)
	}
	return nil, nil
}
func (f fakeGit) ShowBlob(_ context.Context, treeish, path string) ([]byte, error) {
	if f.showBlob != nil {
		return f.showBlob(treeish, path)
	}
	return nil, nil
}

func conflictBytes() []byte {
	return z(
		"tree1",
		"100644 aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa 1\tf.txt",
		"100644 bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb 2\tf.txt",
		"100644 cccccccccccccccccccccccccccccccccccccccc 3\tf.txt",
		"", "1", "f.txt", "CONFLICT (contents)", "CONFLICT (content): Merge conflict in f.txt\n",
	)
}

func markeredBlob() []byte {
	return []byte("head\n<<<<<<< origin/main\nours\n=======\ntheirs\n>>>>>>> feature\nmid\n" +
		"<<<<<<< origin/main\nours2\n=======\ntheirs2\n>>>>>>> feature\ntail\n")
}

func TestRun_Clean(t *testing.T) {
	g := fakeGit{
		mergeTree: func(base, topic string) ([]byte, bool, error) { return z("cleantree"), false, nil },
		diffNames: func(from, to string) ([]string, error) {
			if to == "origin/main" {
				return []string{"a.go", "b.go"}, nil
			}
			return []string{"b.go", "c.go"}, nil // topic
		},
	}
	r, err := Run(context.Background(), g, Options{Topic: "feature", Base: "origin/main"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !r.Mergeable {
		t.Errorf("clean merge reported not mergeable")
	}
	if len(r.Conflicts) != 0 {
		t.Errorf("clean merge has conflicts: %+v", r.Conflicts)
	}
	if r.CleanMerges != 3 { // union {a,b,c}
		t.Errorf("CleanMerges = %d, want 3", r.CleanMerges)
	}
	if !reflect.DeepEqual(r.BothTouchedClean, []string{"b.go"}) {
		t.Errorf("BothTouchedClean = %v, want [b.go]", r.BothTouchedClean)
	}
}

func TestRun_Conflict(t *testing.T) {
	g := fakeGit{
		mergeTree: func(base, topic string) ([]byte, bool, error) { return conflictBytes(), true, nil },
		showBlob: func(treeish, path string) ([]byte, error) {
			if treeish != "tree1" || path != "f.txt" {
				t.Fatalf("unexpected ShowBlob(%q,%q)", treeish, path)
			}
			return markeredBlob(), nil
		},
		diffNames: func(from, to string) ([]string, error) {
			if to == "origin/main" {
				return []string{"f.txt", "x.go"}, nil
			}
			return []string{"f.txt", "y.go"}, nil
		},
	}
	r, err := Run(context.Background(), g, Options{Topic: "feature", Base: "origin/main"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if r.Mergeable {
		t.Errorf("conflict reported mergeable")
	}
	if len(r.Conflicts) != 1 {
		t.Fatalf("want 1 conflict, got %+v", r.Conflicts)
	}
	c := r.Conflicts[0]
	if c.Path != "f.txt" || c.Class != core.ClassBothModified {
		t.Errorf("conflict = %+v", c)
	}
	if c.Hunks != 2 {
		t.Errorf("Hunks = %d, want 2", c.Hunks)
	}
	if !strings.Contains(c.Sample, "<<<<<<<") || !strings.Contains(c.Sample, ">>>>>>>") {
		t.Errorf("Sample missing markers: %q", c.Sample)
	}
	if c.Binary {
		t.Errorf("text conflict marked binary")
	}
	if len(r.BothTouchedClean) != 0 {
		t.Errorf("BothTouchedClean should exclude the conflicted file: %v", r.BothTouchedClean)
	}
	if r.CleanMerges != 2 { // union {f,x,y} minus the 1 conflict
		t.Errorf("CleanMerges = %d, want 2", r.CleanMerges)
	}
	if r.MergeBase == "" {
		t.Errorf("MergeBase not set")
	}
}

func TestRun_Binary(t *testing.T) {
	g := fakeGit{
		mergeTree: func(base, topic string) ([]byte, bool, error) { return conflictBytes(), true, nil },
		showBlob:  func(treeish, path string) ([]byte, error) { return []byte("bin\x00content"), nil },
	}
	r, err := Run(context.Background(), g, Options{Topic: "feature", Base: "origin/main"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	c := r.Conflicts[0]
	if !c.Binary {
		t.Errorf("binary conflict not flagged")
	}
	if c.Hunks != 0 || c.Sample != "" {
		t.Errorf("binary conflict has hunks/sample: %+v", c)
	}
}

func TestRun_DrillDown(t *testing.T) {
	g := fakeGit{
		mergeTree: func(base, topic string) ([]byte, bool, error) { return conflictBytes(), true, nil },
		showBlob:  func(treeish, path string) ([]byte, error) { return markeredBlob(), nil },
	}
	r, err := Run(context.Background(), g, Options{Topic: "feature", Base: "origin/main", Path: "f.txt"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(r.Conflicts) != 1 || r.Conflicts[0].Path != "f.txt" {
		t.Fatalf("drill-down should isolate f.txt: %+v", r.Conflicts)
	}
	// Drill-down shows all hunks, not just the first.
	if strings.Count(r.Conflicts[0].Sample, "<<<<<<<") != 2 {
		t.Errorf("drill-down sample should include both hunks: %q", r.Conflicts[0].Sample)
	}
}

func TestRun_DrillDown_UnknownPath(t *testing.T) {
	g := fakeGit{
		mergeTree: func(base, topic string) ([]byte, bool, error) { return conflictBytes(), true, nil },
		showBlob:  func(treeish, path string) ([]byte, error) { return markeredBlob(), nil },
	}
	_, err := Run(context.Background(), g, Options{Topic: "feature", Base: "origin/main", Path: "nope.txt"})
	if err == nil {
		t.Fatal("drill-down on a non-conflicted path should error")
	}
	if ce := core.AsError(err); ce == nil || ce.Code != core.CodeNotFound {
		t.Errorf("want CodeNotFound, got %v", err)
	}
}

func TestRun_DefaultBaseResolved(t *testing.T) {
	called := false
	g := fakeGit{
		defBase: func() (string, error) { called = true; return "origin/trunk", nil },
		mergeTree: func(base, topic string) ([]byte, bool, error) {
			if base != "origin/trunk" {
				t.Errorf("base = %q, want resolved default origin/trunk", base)
			}
			return z("t"), false, nil
		},
	}
	r, err := Run(context.Background(), g, Options{Topic: "feature"}) // no Base
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !called {
		t.Error("DefaultBase not consulted when --onto omitted")
	}
	if r.Base != "origin/trunk" {
		t.Errorf("Report.Base = %q, want origin/trunk", r.Base)
	}
	if r.Topic != "feature" {
		t.Errorf("Report.Topic = %q, want feature", r.Topic)
	}
}

func TestRun_TopicDefaultsToHEAD(t *testing.T) {
	g := fakeGit{
		mergeTree: func(base, topic string) ([]byte, bool, error) {
			if topic != "HEAD" {
				t.Errorf("topic = %q, want HEAD", topic)
			}
			return z("t"), false, nil
		},
	}
	r, err := Run(context.Background(), g, Options{Base: "origin/main"}) // no Topic
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if r.Topic != "HEAD" {
		t.Errorf("Report.Topic = %q, want HEAD", r.Topic)
	}
}

func TestRun_UnknownRef(t *testing.T) {
	g := fakeGit{
		resolve: func(ref string) (string, error) {
			if ref == "bogus" {
				return "", core.Validationf("unknown-ref", "cannot resolve %q", ref)
			}
			return ref + "-oid", nil
		},
		mergeTree: func(base, topic string) ([]byte, bool, error) { return z("t"), false, nil },
	}
	_, err := Run(context.Background(), g, Options{Topic: "bogus", Base: "origin/main"})
	if err == nil {
		t.Fatal("unknown ref should error before merge-tree")
	}
	if ce := core.AsError(err); ce == nil || ce.Code != core.CodeValidation {
		t.Errorf("want CodeValidation, got %v", err)
	}
}

// A flag-shaped ref must be rejected before it reaches git (argument injection),
// with no git call made.
func TestRun_DashRefRejected(t *testing.T) {
	g := fakeGit{
		resolve: func(ref string) (string, error) { t.Fatalf("ResolveCommit(%q) should not run", ref); return "", nil },
		mergeTree: func(base, topic string) ([]byte, bool, error) {
			t.Fatal("MergeTree should not run")
			return nil, false, nil
		},
	}
	for _, topic := range []string{"-x", "--upload-pack=evil"} {
		_, err := Run(context.Background(), g, Options{Topic: topic, Base: "origin/main"})
		if ce := core.AsError(err); ce == nil || ce.Code != core.CodeValidation {
			t.Errorf("topic %q: want validation error, got %v", topic, err)
		}
	}
	// A dash-shaped --onto is rejected too.
	g2 := fakeGit{mergeTree: func(base, topic string) ([]byte, bool, error) { return z("t"), false, nil }}
	if _, err := Run(context.Background(), g2, Options{Topic: "feature", Base: "-x"}); core.AsError(err) == nil {
		t.Errorf("dash --onto not rejected")
	}
}

func TestRun_NoMergeBaseUsesEmptyTree(t *testing.T) {
	var diffFrom []string
	g := fakeGit{
		mergeTree: func(base, topic string) ([]byte, bool, error) { return z("t"), false, nil },
		mergeBase: func(a, b string) (string, bool, error) { return "", false, nil },
		diffNames: func(from, to string) ([]string, error) {
			diffFrom = append(diffFrom, from)
			return []string{"shared.txt"}, nil
		},
	}
	r, err := Run(context.Background(), g, Options{Topic: "feature", Base: "origin/main"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, from := range diffFrom {
		if from != core.EmptyTreeOID {
			t.Errorf("diff base = %q, want empty-tree OID for unrelated histories", from)
		}
	}
	if r.MergeBase != "" {
		t.Errorf("MergeBase should be empty for unrelated histories, got %q", r.MergeBase)
	}
	if !reflect.DeepEqual(r.BothTouchedClean, []string{"shared.txt"}) {
		t.Errorf("BothTouchedClean = %v, want [shared.txt]", r.BothTouchedClean)
	}
}
