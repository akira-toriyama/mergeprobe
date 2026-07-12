package probe

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/akira-toriyama/mergeprobe/internal/core"
	"github.com/akira-toriyama/mergeprobe/internal/gittest"
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
	resolve    func(ref string) (string, error)
	defBase    func() (string, error)
	mergeTree  func(base, topic string) ([]byte, bool, error)
	mergeBase  func(a, b string) (string, bool, error)
	diffNames  func(from, to string) ([]string, error)
	showBlob   func(treeish, path string) ([]byte, error)
	blobSize   func(treeish, path string) (int64, error)
	fetch      func(source, ref string) (string, error)
	remotes    func() (map[string]string, error)
	markerSize func(tree, path string) (int, error)
	commits    func(base, topic string) ([]core.Commit, error)
	mergeTree3 func(mergeBase, ours, theirs string) ([]byte, bool, error)
	emptyTree  func() (string, error)
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
func (f fakeGit) BlobSize(_ context.Context, treeish, path string) (int64, error) {
	if f.blobSize != nil {
		return f.blobSize(treeish, path)
	}
	// Default: report the size ShowBlob would return, so size-capping tests that
	// don't care about the cap behave like an uncapped read.
	if f.showBlob != nil {
		b, err := f.showBlob(treeish, path)
		return int64(len(b)), err
	}
	return 0, nil
}

func (f fakeGit) Fetch(_ context.Context, source, ref string) (string, error) {
	if f.fetch != nil {
		return f.fetch(source, ref)
	}
	return source + "/" + ref + "-oid", nil
}
func (f fakeGit) Remotes(context.Context) (map[string]string, error) {
	if f.remotes != nil {
		return f.remotes()
	}
	return nil, nil
}
func (f fakeGit) ConflictMarkerSize(_ context.Context, tree, path string) (int, error) {
	if f.markerSize != nil {
		return f.markerSize(tree, path)
	}
	return core.DefaultMarkerSize, nil
}
func (f fakeGit) CommitsToReplay(_ context.Context, base, topic string) ([]core.Commit, error) {
	if f.commits != nil {
		return f.commits(base, topic)
	}
	return nil, nil
}
func (f fakeGit) MergeTree3(_ context.Context, mergeBase, ours, theirs string) ([]byte, bool, error) {
	return f.mergeTree3(mergeBase, ours, theirs)
}
func (f fakeGit) EmptyTree(context.Context) (string, error) {
	if f.emptyTree != nil {
		return f.emptyTree()
	}
	return gittest.EmptyTreeSHA1, nil
}

// fakeForge is an in-memory Forge port. baseRef/ok/err drive the base-resolution
// branches; the default (nil funcs) reports the forge unavailable so the fallback
// path is exercised.
type fakeForge struct {
	baseRef func(owner, repo string, num int) (string, bool, error)
}

func (f fakeForge) PRBaseRef(_ context.Context, owner, repo string, num int) (string, bool, error) {
	if f.baseRef != nil {
		return f.baseRef(owner, repo, num)
	}
	return "", false, nil
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

// In PR mode the resolved refs are OIDs, but the report should show the
// human-facing labels (#123 / origin/main), so Options carries display
// overrides the report prefers over the raw refs.
func TestRun_LabelsOverrideDisplay(t *testing.T) {
	g := fakeGit{
		mergeTree: func(base, topic string) ([]byte, bool, error) {
			if base != "baseoid" || topic != "topicoid" {
				t.Errorf("merge-tree got (%q,%q), want resolved OIDs", base, topic)
			}
			return z("t"), false, nil
		},
	}
	r, err := Run(context.Background(), g, Options{
		Topic: "topicoid", Base: "baseoid", TopicLabel: "#123", BaseLabel: "origin/main",
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if r.Topic != "#123" {
		t.Errorf("Report.Topic = %q, want the label #123", r.Topic)
	}
	if r.Base != "origin/main" {
		t.Errorf("Report.Base = %q, want the label origin/main", r.Base)
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

// clean_merges must be computed by subtracting the actual conflict footprint
// (stage paths + info-message paths), NOT by |union| - |conflictedSet|. git
// parks file/dir and some rename conflicts under synthetic stage paths (e.g.
// "X~ours") that appear in NEITHER diff, so blind cardinality subtraction is
// wrong. Here two synthetic stages map to one real path X (named in the info
// message): naive gives |{X,c1,c2}| - |{X~ours,X~theirs}| = 1; the correct
// footprint answer is |{X,c1,c2} - {X~ours,X~theirs,X}| = 2.
func TestRun_CleanMergesUsesConflictFootprint(t *testing.T) {
	g := fakeGit{
		mergeTree: func(base, topic string) ([]byte, bool, error) {
			return z(
				"tree",
				"100644 aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa 2\tX~ours",
				"100644 bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb 3\tX~theirs",
				"", "1", "X", "CONFLICT (file/directory)", "CONFLICT (file/directory): ... moving it to X~ours instead.\n",
			), true, nil
		},
		showBlob: func(treeish, path string) ([]byte, error) { return []byte("parked\n"), nil },
		diffNames: func(from, to string) ([]string, error) {
			if to == "origin/main" { // base
				return []string{"X", "c1"}, nil
			}
			return []string{"X", "c2"}, nil // topic
		},
	}
	r, err := Run(context.Background(), g, Options{Topic: "feature", Base: "origin/main"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if r.CleanMerges != 2 {
		t.Errorf("CleanMerges = %d, want 2 (footprint {X~ours,X~theirs,X} excluded from union {X,c1,c2})", r.CleanMerges)
	}
	// X is named in a conflict, so it must not show up as both_touched_clean even
	// though both sides changed it.
	for _, p := range r.BothTouchedClean {
		if p == "X" {
			t.Errorf("both_touched_clean leaked the conflicted path X: %v", r.BothTouchedClean)
		}
	}
}

// The footprint must include only CONFLICT-type message paths, NOT
// "Auto-merging <path>" ones. A file both sides changed that 3-way auto-merges
// cleanly (git emits "Auto-merging B" for it) is the exact both_touched_clean
// blind spot — it must survive even when another file conflicts in the same
// merge.
func TestRun_AutoMergedBothTouchedFileSurvives(t *testing.T) {
	g := fakeGit{
		mergeTree: func(base, topic string) ([]byte, bool, error) {
			return z(
				"tree",
				"100644 a1aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa 1\tA",
				"100644 a2aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa 2\tA",
				"100644 a3aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa 3\tA",
				"",
				"1", "A", "Auto-merging", "Auto-merging A\n",
				"1", "A", "CONFLICT (contents)", "CONFLICT (content): Merge conflict in A\n",
				"1", "B", "Auto-merging", "Auto-merging B\n", // B merged CLEANLY
			), true, nil
		},
		showBlob: func(treeish, path string) ([]byte, error) {
			return []byte("<<<<<<< ours\nx\n=======\ny\n>>>>>>> theirs\n"), nil
		},
		diffNames: func(from, to string) ([]string, error) { return []string{"A", "B"}, nil }, // both touched A and B
	}
	r, err := Run(context.Background(), g, Options{Topic: "feature", Base: "origin/main"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !reflect.DeepEqual(r.BothTouchedClean, []string{"B"}) {
		t.Errorf("both_touched_clean = %v, want [B] (B auto-merged cleanly and both sides touched it)", r.BothTouchedClean)
	}
	if r.CleanMerges != 1 { // union {A,B} minus footprint {A}
		t.Errorf("CleanMerges = %d, want 1 (only A is conflicted)", r.CleanMerges)
	}
}

// A merged blob larger than the cap must not be read into memory (OOM guard):
// buildConflict consults BlobSize first and, when over the cap, omits the sample
// and flags Truncated without ever calling ShowBlob.
func TestRun_LargeBlobNotRead(t *testing.T) {
	g := fakeGit{
		mergeTree: func(base, topic string) ([]byte, bool, error) { return conflictBytes(), true, nil },
		blobSize:  func(treeish, path string) (int64, error) { return 1 << 30, nil }, // 1 GiB
		showBlob: func(treeish, path string) ([]byte, error) {
			t.Fatalf("ShowBlob must not be called for an over-cap blob (%s)", path)
			return nil, nil
		},
	}
	r, err := Run(context.Background(), g, Options{Topic: "feature", Base: "origin/main"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	c := r.Conflicts[0]
	if c.Sample != "" || c.Hunks != 0 {
		t.Errorf("over-cap blob should have no sample/hunks: %+v", c)
	}
	if !c.Truncated {
		t.Errorf("over-cap blob should be marked truncated: %+v", c)
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

// A conflicted blob mergeprobe cannot read (a modify/delete leaves no content;
// BlobSize errors or reports 0) must degrade to no sample, NOT fail the probe —
// the documented resilience contract. This pins it so a refactor that propagated
// the error instead of swallowing it would be caught.
func TestRun_UnreadableBlobDegrades(t *testing.T) {
	for _, tc := range []struct {
		name string
		size func(treeish, path string) (int64, error)
	}{
		{"size-zero", func(string, string) (int64, error) { return 0, nil }},
		{"size-error", func(string, string) (int64, error) { return 0, core.Internalf("cat-file", "gone") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := fakeGit{
				mergeTree: func(base, topic string) ([]byte, bool, error) { return conflictBytes(), true, nil },
				blobSize:  tc.size,
				showBlob: func(string, string) ([]byte, error) {
					t.Fatal("ShowBlob must not run once BlobSize signals no content")
					return nil, nil
				},
			}
			r, err := Run(context.Background(), g, Options{Topic: "feature", Base: "origin/main"})
			if err != nil {
				t.Fatalf("an unreadable blob must not fail the probe: %v", err)
			}
			if len(r.Conflicts) != 1 {
				t.Fatalf("conflict still listed: got %+v", r.Conflicts)
			}
			c := r.Conflicts[0]
			if c.Sample != "" || c.Hunks != 0 || c.Truncated {
				t.Errorf("degraded conflict should be sample-less: %+v", c)
			}
			if c.Path != "f.txt" || c.Class != core.ClassBothModified {
				t.Errorf("path/class still reported from stages: %+v", c)
			}
		})
	}
}

// One unreadable blob must not lose the OTHER conflicts' data: a ShowBlob error
// for one path degrades only that path while the rest of the report stands.
func TestRun_OneUnreadableBlobDoesNotAbortReport(t *testing.T) {
	two := z(
		"tree",
		"100644 aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa 1\ta.txt",
		"100644 bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb 2\ta.txt",
		"100644 cccccccccccccccccccccccccccccccccccccccc 3\ta.txt",
		"100644 dddddddddddddddddddddddddddddddddddddddd 1\tb.txt",
		"100644 eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee 2\tb.txt",
		"100644 ffffffffffffffffffffffffffffffffffffffff 3\tb.txt",
		"", "1", "a.txt", "CONFLICT (contents)", "CONFLICT (content): a.txt\n",
		"1", "b.txt", "CONFLICT (contents)", "CONFLICT (content): b.txt\n",
	)
	g := fakeGit{
		mergeTree: func(base, topic string) ([]byte, bool, error) { return two, true, nil },
		showBlob: func(treeish, path string) ([]byte, error) {
			if path == "a.txt" {
				return nil, core.Internalf("cat-file", "boom")
			}
			return []byte("<<<<<<< ours\nx\n=======\ny\n>>>>>>> theirs\n"), nil
		},
	}
	r, err := Run(context.Background(), g, Options{Topic: "feature", Base: "origin/main"})
	if err != nil {
		t.Fatalf("one bad blob aborted the whole report: %v", err)
	}
	byPath := map[string]core.Conflict{}
	for _, c := range r.Conflicts {
		byPath[c.Path] = c
	}
	if c := byPath["a.txt"]; c.Sample != "" || c.Hunks != 0 {
		t.Errorf("a.txt should degrade to no sample: %+v", c)
	}
	if c := byPath["b.txt"]; c.Hunks != 1 || !strings.Contains(c.Sample, "<<<<<<<") {
		t.Errorf("b.txt must still be fully reported: %+v", c)
	}
}

// Every hard failure from the Git port must abort the probe and surface as the
// returned error, never a fabricated verdict. Each seam is exercised by making
// one fake method fail. MergeBase is the sharpest: a dropped error there would
// silently fall back to the empty tree and emit a plausible-but-wrong verdict.
func TestRun_GitPortErrorsPropagate(t *testing.T) {
	boom := core.Internalf("git", "boom")
	base := func() fakeGit {
		return fakeGit{
			mergeTree: func(b, tp string) ([]byte, bool, error) { return z("t"), false, nil },
		}
	}
	tests := []struct {
		name   string
		mutate func(*fakeGit)
	}{
		{"default-base", func(g *fakeGit) { g.defBase = func() (string, error) { return "", boom } }},
		{"resolve-base", func(g *fakeGit) {
			g.resolve = func(ref string) (string, error) {
				if ref == "origin/main" {
					return "", boom
				}
				return ref + "-oid", nil
			}
		}},
		{"merge-tree", func(g *fakeGit) { g.mergeTree = func(b, tp string) ([]byte, bool, error) { return nil, false, boom } }},
		{"merge-base", func(g *fakeGit) { g.mergeBase = func(a, b string) (string, bool, error) { return "", false, boom } }},
		{"empty-tree", func(g *fakeGit) {
			// Reachable only on the unrelated-histories path, so both mutations:
			// no merge base AND the empty-tree resolution failing.
			g.mergeBase = func(a, b string) (string, bool, error) { return "", false, nil }
			g.emptyTree = func() (string, error) { return "", boom }
		}},
		{"diff-names", func(g *fakeGit) { g.diffNames = func(from, to string) ([]string, error) { return nil, boom } }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := base()
			opts := Options{Topic: "feature", Base: "origin/main"}
			if tc.name == "default-base" {
				opts.Base = "" // force DefaultBase to be consulted
			}
			tc.mutate(&g)
			r, err := Run(context.Background(), g, opts)
			if err == nil {
				t.Fatalf("a %s failure must abort the probe, got report %+v", tc.name, r)
			}
			if ce := core.AsError(err); ce == nil || ce.Code != core.CodeInternal {
				t.Errorf("want the propagated internal error, got %v", err)
			}
		})
	}
}

// Unrelated histories diff against the repository's own empty tree, obtained
// through the EmptyTree port (object-format aware) — not the sha1 constant,
// which does not resolve in a sha256 repository (t-rr68).
func TestRun_NoMergeBaseUsesEmptyTree(t *testing.T) {
	const repoEmpty = "empty-tree-oid-in-this-repos-format"
	var diffFrom []string
	g := fakeGit{
		mergeTree: func(base, topic string) ([]byte, bool, error) { return z("t"), false, nil },
		mergeBase: func(a, b string) (string, bool, error) { return "", false, nil },
		emptyTree: func() (string, error) { return repoEmpty, nil },
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
		if from != repoEmpty {
			t.Errorf("diff base = %q, want the port's empty tree %q", from, repoEmpty)
		}
	}
	if r.MergeBase != "" {
		t.Errorf("MergeBase should be empty for unrelated histories, got %q", r.MergeBase)
	}
	if !reflect.DeepEqual(r.BothTouchedClean, []string{"shared.txt"}) {
		t.Errorf("BothTouchedClean = %v, want [shared.txt]", r.BothTouchedClean)
	}
}
