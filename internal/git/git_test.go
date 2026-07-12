package git

import (
	"context"
	"testing"

	"github.com/akira-toriyama/mergeprobe/internal/core"
	"github.com/akira-toriyama/mergeprobe/internal/gittest"
)

// scenario is the shared three-way-conflict fixture (main/ours/theirs).
func scenario(t *testing.T) string { return gittest.ConflictRepo(t) }

// The adapter's -z output must parse via core into the exact three-way stage
// picture — proving the real git contract matches the parser's fixtures.
func TestMergeTree_Conflict(t *testing.T) {
	dir := scenario(t)
	r := New(dir)
	out, conflicted, err := r.MergeTree(context.Background(), "ours", "theirs")
	if err != nil {
		t.Fatalf("MergeTree: %v", err)
	}
	if !conflicted {
		t.Fatal("expected a conflict")
	}
	mt, err := core.ParseMergeTreeZ(out)
	if err != nil {
		t.Fatalf("parse real -z: %v", err)
	}
	byPath := map[string]core.ConflictFile{}
	for _, f := range mt.Files {
		byPath[f.Path] = f
	}
	if got := core.Classify(byPath["f.txt"]); got != core.ClassBothModified {
		t.Errorf("f.txt class = %q, want both-modified", got)
	}
	if got := core.Classify(byPath["addonly.txt"]); got != core.ClassAddAdd {
		t.Errorf("addonly.txt class = %q, want add-add", got)
	}
	if got := core.Classify(byPath["d.txt"]); got != core.ClassModifyDelete {
		t.Errorf("d.txt class = %q, want modify-delete", got)
	}
	// The merged tree carries markers for the text conflict.
	blob, err := r.ShowBlob(context.Background(), mt.Tree, "f.txt")
	if err != nil {
		t.Fatalf("ShowBlob f.txt: %v", err)
	}
	if _, n := core.ConflictHunks(blob, core.DefaultMarkerSize); n != 1 {
		t.Errorf("f.txt hunks = %d, want 1", n)
	}
}

func TestMergeTree_Clean(t *testing.T) {
	dir := scenario(t)
	r := New(dir)
	// main is an ancestor of ours → merging them is clean.
	out, conflicted, err := r.MergeTree(context.Background(), "main", "ours")
	if err != nil {
		t.Fatalf("MergeTree: %v", err)
	}
	if conflicted {
		t.Fatal("main..ours should merge clean")
	}
	mt, err := core.ParseMergeTreeZ(out)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !mt.Clean() || mt.Tree == "" {
		t.Errorf("clean merge parse: %+v", mt)
	}
}

func TestResolveCommit(t *testing.T) {
	dir := scenario(t)
	r := New(dir)
	oid, err := r.ResolveCommit(context.Background(), "ours")
	if err != nil {
		t.Fatalf("resolve ours: %v", err)
	}
	if len(oid) != 40 {
		t.Errorf("resolved oid = %q, want a 40-char sha", oid)
	}
	_, err = r.ResolveCommit(context.Background(), "no-such-ref")
	if ce := core.AsError(err); ce == nil || ce.Code != core.CodeValidation {
		t.Errorf("unknown ref: want validation error, got %v", err)
	}
}

func TestMergeBase(t *testing.T) {
	dir := scenario(t)
	r := New(dir)
	oid, ok, err := r.MergeBase(context.Background(), "ours", "theirs")
	if err != nil || !ok || oid == "" {
		t.Fatalf("MergeBase ours/theirs: oid=%q ok=%v err=%v", oid, ok, err)
	}
	// Unrelated history → no merge base.
	gittest.Run(t, dir, "checkout", "-q", "--orphan", "island")
	gittest.Run(t, dir, "commit", "-q", "--allow-empty", "-m", "island")
	_, ok, err = r.MergeBase(context.Background(), "main", "island")
	if err != nil {
		t.Fatalf("MergeBase unrelated errored: %v", err)
	}
	if ok {
		t.Error("unrelated histories reported a merge base")
	}
}

func TestDiffNames(t *testing.T) {
	dir := scenario(t)
	r := New(dir)
	mb, _, _ := r.MergeBase(context.Background(), "main", "ours")
	names, err := r.DiffNames(context.Background(), mb, "ours")
	if err != nil {
		t.Fatalf("DiffNames: %v", err)
	}
	got := map[string]bool{}
	for _, n := range names {
		got[n] = true
	}
	for _, want := range []string{"addonly.txt", "d.txt", "f.txt"} {
		if !got[want] {
			t.Errorf("DiffNames missing %q; got %v", want, names)
		}
	}
	if got["untouched.txt"] {
		t.Errorf("DiffNames included an untouched file: %v", names)
	}
}

func TestShowBlob_MissingPath(t *testing.T) {
	dir := scenario(t)
	r := New(dir)
	if _, err := r.ShowBlob(context.Background(), "main", "does/not/exist"); err == nil {
		t.Error("ShowBlob of a missing path should error")
	}
}

func TestDefaultBase(t *testing.T) {
	dir := scenario(t)
	r := New(dir)
	// origin/HEAD is unset in a bare local repo → a clear validation error.
	if _, err := r.DefaultBase(context.Background()); err == nil {
		t.Error("DefaultBase with no origin/HEAD should error")
	}
	// Wire an origin/HEAD and it resolves.
	oid := gittest.Run(t, dir, "rev-parse", "main")
	gittest.Run(t, dir, "remote", "add", "origin", dir)
	gittest.Run(t, dir, "update-ref", "refs/remotes/origin/main", oid)
	gittest.Run(t, dir, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
	base, err := r.DefaultBase(context.Background())
	if err != nil {
		t.Fatalf("DefaultBase: %v", err)
	}
	if base != "origin/main" {
		t.Errorf("DefaultBase = %q, want origin/main", base)
	}
}

func TestBlobSize(t *testing.T) {
	dir := scenario(t)
	r := New(dir)
	out, _, _ := r.MergeTree(context.Background(), "ours", "theirs")
	mt, err := core.ParseMergeTreeZ(out)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	size, err := r.BlobSize(context.Background(), mt.Tree, "f.txt")
	if err != nil {
		t.Fatalf("BlobSize: %v", err)
	}
	blob, _ := r.ShowBlob(context.Background(), mt.Tree, "f.txt")
	if size != int64(len(blob)) {
		t.Errorf("BlobSize = %d, ShowBlob len = %d", size, len(blob))
	}
	if _, err := r.BlobSize(context.Background(), "main", "does/not/exist"); err == nil {
		t.Error("BlobSize of a missing path should error")
	}
}

// Unrelated histories must merge (as add/add) rather than git refusing with exit
// 128 — so the probe reports them gracefully instead of a spurious internal error.
func TestMergeTree_UnrelatedHistories(t *testing.T) {
	dir := gittest.Init(t)
	gittest.Write(t, dir, "a.txt", "from main\n")
	gittest.Run(t, dir, "add", ".")
	gittest.Run(t, dir, "commit", "-qm", "main")
	gittest.Run(t, dir, "checkout", "-q", "--orphan", "island")
	gittest.Run(t, dir, "rm", "-rfq", ".")
	gittest.Write(t, dir, "a.txt", "from island\n")
	gittest.Run(t, dir, "add", ".")
	gittest.Run(t, dir, "commit", "-qm", "island")

	r := New(dir)
	_, conflicted, err := r.MergeTree(context.Background(), "main", "island")
	if err != nil {
		t.Fatalf("MergeTree on unrelated histories errored (want a graceful conflict): %v", err)
	}
	if !conflicted {
		t.Error("unrelated histories with divergent a.txt should conflict, not merge clean")
	}
	if _, ok, _ := r.MergeBase(context.Background(), "main", "island"); ok {
		t.Error("unrelated histories should have no merge base")
	}
}

// upstreamWithPR builds a source repo carrying refs/pull/1/head (as GitHub
// exposes a PR head) and returns its path plus the PR-head OID.
func upstreamWithPR(t *testing.T) (dir, prOID string) {
	t.Helper()
	dir = gittest.Init(t)
	gittest.Write(t, dir, "a.txt", "base\n")
	gittest.Run(t, dir, "add", ".")
	gittest.Run(t, dir, "commit", "-qm", "base")
	gittest.Run(t, dir, "checkout", "-qb", "pr")
	gittest.Write(t, dir, "a.txt", "pr change\n")
	gittest.Run(t, dir, "commit", "-qam", "pr")
	prOID = gittest.Run(t, dir, "rev-parse", "HEAD")
	gittest.Run(t, dir, "update-ref", "refs/pull/1/head", prOID)
	gittest.Run(t, dir, "checkout", "-q", "main")
	return dir, prOID
}

// Fetch pulls a ref from a source into FETCH_HEAD and returns its OID, so PR
// resolution can pin a fetched PR head / base without adding tracking refs.
func TestFetch(t *testing.T) {
	up, prOID := upstreamWithPR(t)
	cons := gittest.Init(t)
	gittest.Run(t, cons, "remote", "add", "origin", up)
	r := New(cons)

	oid, err := r.Fetch(context.Background(), "origin", "refs/pull/1/head")
	if err != nil {
		t.Fatalf("Fetch pull ref: %v", err)
	}
	if oid != prOID {
		t.Errorf("Fetch pull ref = %q, want %q", oid, prOID)
	}
	// A branch name resolves too (the base-branch fetch path).
	mainOID := gittest.Run(t, up, "rev-parse", "main")
	if got, err := r.Fetch(context.Background(), "origin", "main"); err != nil || got != mainOID {
		t.Errorf("Fetch main = (%q,%v), want (%q,nil)", got, err, mainOID)
	}
}

// A missing remote ref is a soft not-found (exit 1), not an internal error, so
// `mergeprobe 999` for a nonexistent PR reports cleanly.
func TestFetch_MissingRef(t *testing.T) {
	up, _ := upstreamWithPR(t)
	cons := gittest.Init(t)
	gittest.Run(t, cons, "remote", "add", "origin", up)
	r := New(cons)

	_, err := r.Fetch(context.Background(), "origin", "refs/pull/999/head")
	if ce := core.AsError(err); ce == nil || ce.Code != core.CodeNotFound {
		t.Errorf("missing ref: want CodeNotFound, got %v", err)
	}
}

// Flag-shaped source/ref are rejected before reaching git (argument injection),
// mirroring ResolveCommit's guard.
func TestFetch_DashRejected(t *testing.T) {
	gittest.SkipIfNoGit(t)
	r := New(t.TempDir())
	if _, err := r.Fetch(context.Background(), "-x", "main"); core.AsError(err) == nil {
		t.Error("dash source not rejected")
	}
	if _, err := r.Fetch(context.Background(), "origin", "-x"); core.AsError(err) == nil {
		t.Error("dash ref not rejected")
	}
}

// Remotes maps each remote name to its fetch URL, so owner/repo#N can be matched
// to a configured remote.
func TestRemotes(t *testing.T) {
	dir := gittest.Init(t)
	gittest.Run(t, dir, "remote", "add", "origin", "git@github.com:akira-toriyama/mergeprobe.git")
	gittest.Run(t, dir, "remote", "add", "upstream", "https://github.com/cli/cli.git")
	r := New(dir)

	m, err := r.Remotes(context.Background())
	if err != nil {
		t.Fatalf("Remotes: %v", err)
	}
	if m["origin"] != "git@github.com:akira-toriyama/mergeprobe.git" {
		t.Errorf("origin = %q", m["origin"])
	}
	if m["upstream"] != "https://github.com/cli/cli.git" {
		t.Errorf("upstream = %q", m["upstream"])
	}
}

// No remotes yields an empty map, not an error.
func TestRemotes_None(t *testing.T) {
	dir := gittest.Init(t)
	r := New(dir)
	m, err := r.Remotes(context.Background())
	if err != nil {
		t.Fatalf("Remotes: %v", err)
	}
	if len(m) != 0 {
		t.Errorf("want no remotes, got %v", m)
	}
}

// rebaseScenario builds base + a two-commit topic where the first commit adds a
// file cleanly and the second modifies a line the advanced base also changed, so
// a rebase conflicts on exactly the second commit — the shape RunRebase probes.
func rebaseScenario(t *testing.T) (dir string) {
	t.Helper()
	dir = gittest.Init(t)
	gittest.Write(t, dir, "a.txt", "a1\na2\na3\n")
	gittest.Run(t, dir, "add", ".")
	gittest.Run(t, dir, "commit", "-qm", "base")
	gittest.Run(t, dir, "checkout", "-qb", "topic")
	gittest.Write(t, dir, "b.txt", "new\n")
	gittest.Run(t, dir, "add", "b.txt")
	gittest.Run(t, dir, "commit", "-qm", "c1 add b")
	gittest.Write(t, dir, "a.txt", "a1\nTOPIC\na3\n")
	gittest.Run(t, dir, "commit", "-qam", "c2 modify a")
	gittest.Run(t, dir, "checkout", "-q", "main")
	gittest.Write(t, dir, "a.txt", "a1\nMAIN\na3\n")
	gittest.Run(t, dir, "commit", "-qam", "main modifies a")
	return dir
}

// CommitsToReplay lists base..topic oldest-first, each with its first parent
// (the rebase step's merge base) and subject.
func TestCommitsToReplay(t *testing.T) {
	dir := rebaseScenario(t)
	r := New(dir)
	commits, err := r.CommitsToReplay(context.Background(), "main", "topic")
	if err != nil {
		t.Fatalf("CommitsToReplay: %v", err)
	}
	if len(commits) != 2 {
		t.Fatalf("want 2 commits to replay, got %d: %+v", len(commits), commits)
	}
	if commits[0].Subject != "c1 add b" || commits[1].Subject != "c2 modify a" {
		t.Errorf("wrong order/subjects: %+v", commits)
	}
	// Each commit's Parent must be the previous commit (linear history).
	if commits[1].Parent != commits[0].OID {
		t.Errorf("c2's parent %q should be c1 %q", commits[1].Parent, commits[0].OID)
	}
	for _, c := range commits {
		if len(c.OID) != 40 || len(c.Parent) != 40 {
			t.Errorf("OID/Parent not full SHAs: %+v", c)
		}
	}
}

// No commits to replay (topic already on base) is an empty list, not an error.
func TestCommitsToReplay_Empty(t *testing.T) {
	dir := rebaseScenario(t)
	r := New(dir)
	commits, err := r.CommitsToReplay(context.Background(), "topic", "topic")
	if err != nil {
		t.Fatalf("CommitsToReplay: %v", err)
	}
	if len(commits) != 0 {
		t.Errorf("want no commits, got %+v", commits)
	}
}

// MergeTree3 applies theirs's delta (from mergeBase) onto ours — the rebase
// step. c1 applies cleanly onto main; c2 then conflicts on a.txt.
func TestMergeTree3(t *testing.T) {
	dir := rebaseScenario(t)
	r := New(dir)
	ctx := context.Background()
	commits, err := r.CommitsToReplay(ctx, "main", "topic")
	if err != nil {
		t.Fatalf("CommitsToReplay: %v", err)
	}
	base := gittest.Run(t, dir, "rev-parse", "main")

	// Step 1: c1 (add b.txt) onto main — clean.
	out, conflicted, err := r.MergeTree3(ctx, commits[0].Parent, base, commits[0].OID)
	if err != nil || conflicted {
		t.Fatalf("c1 should apply cleanly: conflicted=%v err=%v", conflicted, err)
	}
	mt, err := core.ParseMergeTreeZ(out)
	if err != nil {
		t.Fatalf("parse step1: %v", err)
	}
	running := mt.Tree

	// Step 2: c2 (modify a.txt) onto the running tree — conflicts.
	out, conflicted, err = r.MergeTree3(ctx, commits[1].Parent, running, commits[1].OID)
	if err != nil {
		t.Fatalf("MergeTree3 step2: %v", err)
	}
	if !conflicted {
		t.Fatal("c2 modifies a line main also changed; the rebase step must conflict")
	}
	mt, err = core.ParseMergeTreeZ(out)
	if err != nil {
		t.Fatalf("parse step2: %v", err)
	}
	byPath := map[string]core.ConflictFile{}
	for _, f := range mt.Files {
		byPath[f.Path] = f
	}
	if core.Classify(byPath["a.txt"]) != core.ClassBothModified {
		t.Errorf("a.txt should be both-modified, got %+v", mt.Files)
	}
}

// ConflictMarkerSize reads the effective conflict-marker-size for a path from a
// tree's .gitattributes (matching what merge-tree used), defaulting to 7 when
// the attribute is unset.
func TestConflictMarkerSize(t *testing.T) {
	dir := gittest.Init(t)
	gittest.Write(t, dir, ".gitattributes", "small.txt conflict-marker-size=4\n")
	gittest.Write(t, dir, "small.txt", "x\n")
	gittest.Write(t, dir, "normal.txt", "y\n")
	gittest.Run(t, dir, "add", ".")
	gittest.Run(t, dir, "commit", "-qm", "base")
	tree := gittest.Run(t, dir, "rev-parse", "HEAD^{tree}")
	r := New(dir)

	if got, err := r.ConflictMarkerSize(context.Background(), tree, "small.txt"); err != nil || got != 4 {
		t.Errorf("small.txt size = (%d,%v), want 4", got, err)
	}
	// Unset attribute defaults to 7.
	if got, err := r.ConflictMarkerSize(context.Background(), tree, "normal.txt"); err != nil || got != 7 {
		t.Errorf("normal.txt size = (%d,%v), want default 7", got, err)
	}
}

func TestNotARepo(t *testing.T) {
	gittest.SkipIfNoGit(t)
	r := New(t.TempDir()) // empty dir, no repo
	_, err := r.ResolveCommit(context.Background(), "HEAD")
	if ce := core.AsError(err); ce == nil || ce.Code != core.CodeValidation {
		t.Errorf("not-a-repo: want validation error, got %v", err)
	}
}
