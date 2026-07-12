package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/akira-toriyama/mergeprobe/internal/core"
	"github.com/akira-toriyama/mergeprobe/internal/git"
	"github.com/akira-toriyama/mergeprobe/internal/gittest"
	"github.com/akira-toriyama/mergeprobe/internal/probe"
)

// The scaffold guarantees --help succeeds and renders the grammar.
func TestRootHelp(t *testing.T) {
	root := newRootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"--help"})
	if err := root.Execute(); err != nil {
		t.Fatalf("--help failed: %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, "mergeprobe") {
		t.Errorf("help output does not mention the binary name: %q", got)
	}
	if !strings.Contains(got, "--onto") {
		t.Errorf("help output does not render the grammar: %q", got)
	}
}

// The JSON funnel must not HTML-escape <, >, & — conflict markers and paths echo
// those bytes, and stderr envelopes must byte-match the on-disk form.
func TestRenderErrorNoHTMLEscape(t *testing.T) {
	old := errOut
	var buf bytes.Buffer
	errOut = &buf
	defer func() { errOut = old }()

	renderError(&core.Error{Code: core.CodeValidation, Msg: "line <html> & path a>b"})

	got := buf.String()
	for _, escaped := range []string{"\\u003c", "\\u003e", "\\u0026"} {
		if strings.Contains(got, escaped) {
			t.Errorf("error envelope contains an HTML-escaped sequence (%s): %s", escaped, got)
		}
	}
	if !strings.Contains(got, "line <html> & path a>b") {
		t.Errorf("message not emitted verbatim: %s", got)
	}
}

// runCLI drives the real exit-code path (run) against a real git repo, with
// stdout/stderr captured. Returns the payload, the error envelope, and the code.
func runCLI(t *testing.T, dir string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	oldRepo, oldOut, oldErr := newRepo, out, errOut
	var bout, berr bytes.Buffer
	newRepo = func() probe.Git { return git.New(dir) }
	out, errOut = &bout, &berr
	defer func() { newRepo, out, errOut = oldRepo, oldOut, oldErr }()
	code = run(context.Background(), args)
	return bout.String(), berr.String(), code
}

func TestEndToEnd_Conflict(t *testing.T) {
	dir := gittest.ConflictRepo(t)
	stdout, stderr, code := runCLI(t, dir, "theirs", "--onto", "ours")
	if code != int(core.CodeOK) {
		t.Fatalf("exit = %d (want 0); stderr=%s", code, stderr)
	}
	var r core.Report
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout)
	}
	if r.Mergeable {
		t.Errorf("conflict reported mergeable")
	}
	if r.Base != "ours" || r.Topic != "theirs" {
		t.Errorf("base/topic = %q/%q", r.Base, r.Topic)
	}
	byPath := map[string]core.Conflict{}
	for _, c := range r.Conflicts {
		byPath[c.Path] = c
	}
	if c, ok := byPath["f.txt"]; !ok || c.Class != core.ClassBothModified || c.Hunks < 1 || !strings.Contains(c.Sample, "<<<<<<<") {
		t.Errorf("f.txt conflict wrong: %+v", c)
	}
	if c := byPath["addonly.txt"]; c.Class != core.ClassAddAdd {
		t.Errorf("addonly.txt class = %q, want add-add", c.Class)
	}
	if c := byPath["d.txt"]; c.Class != core.ClassModifyDelete {
		t.Errorf("d.txt class = %q, want modify-delete", c.Class)
	}
	if r.MergeBase == "" {
		t.Errorf("merge_base not set")
	}
}

func TestEndToEnd_Clean(t *testing.T) {
	dir := gittest.ConflictRepo(t)
	stdout, stderr, code := runCLI(t, dir, "ours", "--onto", "main")
	if code != int(core.CodeOK) {
		t.Fatalf("exit = %d (want 0); stderr=%s", code, stderr)
	}
	var r core.Report
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout)
	}
	if !r.Mergeable {
		t.Errorf("clean merge reported not mergeable: %+v", r)
	}
	if len(r.Conflicts) != 0 {
		t.Errorf("clean merge has conflicts: %+v", r.Conflicts)
	}
}

func TestEndToEnd_DrillDown(t *testing.T) {
	dir := gittest.ConflictRepo(t)
	stdout, _, code := runCLI(t, dir, "theirs", "--onto", "ours", "--path", "f.txt")
	if code != int(core.CodeOK) {
		t.Fatalf("exit = %d (want 0)", code)
	}
	var r core.Report
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout)
	}
	if len(r.Conflicts) != 1 || r.Conflicts[0].Path != "f.txt" {
		t.Fatalf("drill-down should isolate f.txt: %+v", r.Conflicts)
	}
	if !strings.Contains(r.Conflicts[0].Sample, "<<<<<<<") {
		t.Errorf("drill-down sample lacks markers: %q", r.Conflicts[0].Sample)
	}
}

func TestEndToEnd_DrillDownUnknownPath(t *testing.T) {
	dir := gittest.ConflictRepo(t)
	stdout, stderr, code := runCLI(t, dir, "theirs", "--onto", "ours", "--path", "nope.txt")
	if code != int(core.CodeNotFound) {
		t.Fatalf("exit = %d, want 1 (not found)", code)
	}
	if stdout != "" {
		t.Errorf("stdout should be empty on error, got %q", stdout)
	}
	if !strings.Contains(stderr, "not among the conflicted files") {
		t.Errorf("stderr lacks a helpful message: %q", stderr)
	}
}

// stubForge is a fixed Forge for the CLI e2e tests, so PR resolution never
// shells out to a real gh.
type stubForge struct {
	base string
	ok   bool
	err  error
}

func (s stubForge) PRBaseRef(context.Context, string, string, int) (string, bool, error) {
	return s.base, s.ok, s.err
}

// withForge installs a stub Forge for one test and restores the default after.
func withForge(t *testing.T, f probe.Forge) {
	t.Helper()
	old := newForge
	newForge = func() probe.Forge { return f }
	t.Cleanup(func() { newForge = old })
}

// upstreamPRConflict builds a source repo whose refs/pull/1/head (branch
// "feature") conflicts with its advanced "main" on shared.txt — the shape a
// real "does PR #1 still land?" probe faces.
func upstreamPRConflict(t *testing.T) string {
	t.Helper()
	dir := gittest.Init(t)
	gittest.Write(t, dir, "shared.txt", "v1\n")
	gittest.Run(t, dir, "add", ".")
	gittest.Run(t, dir, "commit", "-qm", "base")
	gittest.Run(t, dir, "checkout", "-qb", "feature")
	gittest.Write(t, dir, "shared.txt", "feature\n")
	gittest.Run(t, dir, "commit", "-qam", "feature change")
	gittest.Run(t, dir, "update-ref", "refs/pull/1/head", gittest.Run(t, dir, "rev-parse", "HEAD"))
	gittest.Run(t, dir, "checkout", "-q", "main")
	gittest.Write(t, dir, "shared.txt", "mainchange\n")
	gittest.Run(t, dir, "commit", "-qam", "main change")
	return dir
}

// mergeprobe 1 resolves the PR head and (via gh) its base branch, then probes —
// reporting the conflict without any note, and labelling the refs #1 / main.
func TestEndToEnd_PRResolve_ForgeBase(t *testing.T) {
	up := upstreamPRConflict(t)
	cons := gittest.Init(t)
	gittest.Run(t, cons, "remote", "add", "origin", up)
	withForge(t, stubForge{base: "main", ok: true})

	stdout, stderr, code := runCLI(t, cons, "1")
	if code != int(core.CodeOK) {
		t.Fatalf("exit = %d (want 0); stderr=%s", code, stderr)
	}
	var r core.Report
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatalf("stdout not JSON: %v\n%s", err, stdout)
	}
	if r.Topic != "#1" || r.Base != "main" {
		t.Errorf("labels: topic=%q base=%q, want #1 / main", r.Topic, r.Base)
	}
	if r.Mergeable {
		t.Errorf("PR #1 conflicts with main; should not be mergeable")
	}
	if len(r.Conflicts) != 1 || r.Conflicts[0].Path != "shared.txt" {
		t.Errorf("want a shared.txt conflict, got %+v", r.Conflicts)
	}
	if strings.Contains(stderr, "note:") {
		t.Errorf("gh answered the base; there should be no assumed-base note: %q", stderr)
	}
}

// With gh unavailable, mergeprobe falls back to origin/HEAD and prints a note so
// the assumption is visible; the probe still runs.
func TestEndToEnd_PRResolve_FallbackNote(t *testing.T) {
	up := upstreamPRConflict(t)
	cons := gittest.Init(t)
	gittest.Run(t, cons, "remote", "add", "origin", up)
	gittest.Run(t, cons, "fetch", "-q", "origin")
	gittest.Run(t, cons, "remote", "set-head", "origin", "main")
	withForge(t, stubForge{ok: false}) // gh unavailable

	stdout, stderr, code := runCLI(t, cons, "1")
	if code != int(core.CodeOK) {
		t.Fatalf("exit = %d (want 0); stderr=%s", code, stderr)
	}
	var r core.Report
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatalf("stdout not JSON: %v\n%s", err, stdout)
	}
	if r.Base != "origin/main" {
		t.Errorf("fallback base = %q, want origin/main", r.Base)
	}
	if !strings.Contains(stderr, "note:") || !strings.Contains(stderr, "assumed") {
		t.Errorf("fallback should print an assumed-base note: %q", stderr)
	}
	if stdout == "" || !json.Valid([]byte(stdout)) {
		t.Errorf("stdout must stay clean JSON despite the stderr note")
	}
}

// A nonexistent PR is a soft not-found (exit 1) naming the PR, not a crash.
func TestEndToEnd_PRResolve_NotFound(t *testing.T) {
	up := upstreamPRConflict(t)
	cons := gittest.Init(t)
	gittest.Run(t, cons, "remote", "add", "origin", up)
	withForge(t, stubForge{base: "main", ok: true})

	stdout, stderr, code := runCLI(t, cons, "999")
	if code != int(core.CodeNotFound) {
		t.Fatalf("exit = %d, want 1 (not found); stderr=%s", code, stderr)
	}
	if stdout != "" {
		t.Errorf("stdout should be empty on error: %q", stdout)
	}
	if !strings.Contains(stderr, "#999") {
		t.Errorf("not-found should name the PR: %q", stderr)
	}
}

// An ordinary unknown branch (non-digit, not a PR reference) surfaces the raw
// unknown-ref validation error (exit 2) with no PR-number confusion.
func TestEndToEnd_UnknownBranch(t *testing.T) {
	dir := gittest.ConflictRepo(t)
	stdout, stderr, code := runCLI(t, dir, "no-such-branch")
	if code != int(core.CodeValidation) {
		t.Fatalf("exit = %d, want 2 for an unknown branch; stderr=%s", code, stderr)
	}
	if stdout != "" {
		t.Errorf("stdout should be empty on error: %q", stdout)
	}
	if !strings.Contains(stderr, "no-such-branch") {
		t.Errorf("stderr should name the unresolved ref: %q", stderr)
	}
	if strings.Contains(stderr, "PR number") {
		t.Errorf("an unknown branch must not be mistaken for a PR number: %q", stderr)
	}
}

// Too many positionals is a usage error (exit 2) via the cobra→validation map.
func TestEndToEnd_TooManyArgs(t *testing.T) {
	dir := gittest.ConflictRepo(t)
	_, _, code := runCLI(t, dir, "a", "b")
	if code != int(core.CodeValidation) {
		t.Errorf("exit = %d, want 2 for too many args", code)
	}
}

// A file carrying a non-default conflict-marker-size gitattribute makes
// merge-tree emit shorter markers (e.g. <<<< instead of <<<<<<<); mergeprobe
// must still find the hunks and sample, not silently report hunks:0. The size
// is read from the merged tree via check-attr, matching what merge-tree used.
func TestEndToEnd_SmallConflictMarkerSize(t *testing.T) {
	dir := gittest.Init(t)
	gittest.Write(t, dir, ".gitattributes", "f.txt conflict-marker-size=4\n")
	gittest.Write(t, dir, "f.txt", "a\nb\nc\n")
	gittest.Run(t, dir, "add", ".")
	gittest.Run(t, dir, "commit", "-qm", "base")
	gittest.Run(t, dir, "checkout", "-qb", "ours")
	gittest.Write(t, dir, "f.txt", "OURS\nb\nc\n")
	gittest.Run(t, dir, "commit", "-qam", "ours")
	gittest.Run(t, dir, "checkout", "-q", "main")
	gittest.Run(t, dir, "checkout", "-qb", "theirs")
	gittest.Write(t, dir, "f.txt", "THEIRS\nb\nc\n")
	gittest.Run(t, dir, "commit", "-qam", "theirs")

	stdout, stderr, code := runCLI(t, dir, "theirs", "--onto", "ours")
	if code != int(core.CodeOK) {
		t.Fatalf("exit = %d (want 0); stderr=%s", code, stderr)
	}
	var r core.Report
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatalf("stdout not JSON: %v\n%s", err, stdout)
	}
	if len(r.Conflicts) != 1 {
		t.Fatalf("want one conflict, got %+v", r.Conflicts)
	}
	c := r.Conflicts[0]
	if c.Hunks != 1 {
		t.Errorf("hunks = %d, want 1 (marker-size-4 conflict must not read as 0)", c.Hunks)
	}
	if !strings.Contains(c.Sample, "<<<<") || !strings.Contains(c.Sample, ">>>>") {
		t.Errorf("sample lacks the size-4 markers: %q", c.Sample)
	}
}

// rebaseRepo builds base + a two-commit topic (c1 adds b.txt cleanly, c2
// modifies a.txt). Returns the repo path; callers add a divergent main.
func rebaseRepo(t *testing.T) string {
	t.Helper()
	dir := gittest.Init(t)
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
	return dir
}

// --rebase replays topic's commits onto an advanced main; the second commit
// touches a line main also changed, so the rebase conflicts on exactly that
// commit — the differentiator a bare merge probe cannot show.
func TestEndToEnd_RebaseConflict(t *testing.T) {
	dir := rebaseRepo(t)
	gittest.Write(t, dir, "a.txt", "a1\nMAIN\na3\n")
	gittest.Run(t, dir, "commit", "-qam", "main modifies a")

	stdout, stderr, code := runCLI(t, dir, "topic", "--onto", "main", "--rebase")
	if code != int(core.CodeOK) {
		t.Fatalf("exit = %d (want 0); stderr=%s", code, stderr)
	}
	var r core.RebaseReport
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatalf("stdout not a RebaseReport: %v\n%s", err, stdout)
	}
	if r.Rebaseable {
		t.Error("rebase onto conflicting main should not be rebaseable")
	}
	if r.Commits != 2 || r.Applied != 1 {
		t.Errorf("commits/applied = %d/%d, want 2/1", r.Commits, r.Applied)
	}
	if r.Conflict == nil || r.Conflict.Subject != "c2 modify a" {
		t.Fatalf("first conflict should be c2: %+v", r.Conflict)
	}
	if len(r.Conflict.Conflicts) != 1 || r.Conflict.Conflicts[0].Path != "a.txt" {
		t.Errorf("conflict path should be a.txt: %+v", r.Conflict.Conflicts)
	}
	if !strings.Contains(r.Conflict.Conflicts[0].Sample, "<<<<<<<") {
		t.Errorf("rebase conflict sample lacks markers: %q", r.Conflict.Conflicts[0].Sample)
	}
}

// The same topic rebases cleanly onto a main that advanced without touching the
// topic's files.
func TestEndToEnd_RebaseClean(t *testing.T) {
	dir := rebaseRepo(t)
	gittest.Write(t, dir, "unrelated.txt", "main-only\n")
	gittest.Run(t, dir, "add", "unrelated.txt")
	gittest.Run(t, dir, "commit", "-qm", "main adds unrelated")

	stdout, stderr, code := runCLI(t, dir, "topic", "--onto", "main", "--rebase")
	if code != int(core.CodeOK) {
		t.Fatalf("exit = %d (want 0); stderr=%s", code, stderr)
	}
	var r core.RebaseReport
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatalf("stdout not a RebaseReport: %v\n%s", err, stdout)
	}
	if !r.Rebaseable || r.Commits != 2 || r.Applied != 2 || r.Conflict != nil {
		t.Errorf("clean rebase = %+v, want rebaseable/2/2/nil", r)
	}
}

// A topic containing a merge commit replays by first-parent approximation, and
// the CLI says so with a stderr note. This test also pins the divergence the
// note warns about: the merge commit carries an "evil" edit (a change belonging
// to neither parent) that collides with the advanced main, so the simulation
// stops on the merge commit — while a real git rebase drops merges (and the
// evil edit with them) and completes cleanly.
func TestEndToEnd_RebaseMergeCommitNoteAndDivergence(t *testing.T) {
	dir := gittest.Init(t)
	gittest.Write(t, dir, "a.txt", "a\n")
	gittest.Write(t, dir, "c.txt", "c1\nc2\nc3\n")
	gittest.Run(t, dir, "add", ".")
	gittest.Run(t, dir, "commit", "-qm", "base")
	gittest.Run(t, dir, "checkout", "-qb", "side")
	gittest.Write(t, dir, "b.txt", "b\n")
	gittest.Run(t, dir, "add", "b.txt")
	gittest.Run(t, dir, "commit", "-qm", "s1 add b")
	gittest.Run(t, dir, "checkout", "-q", "main")
	gittest.Run(t, dir, "checkout", "-qb", "topic")
	gittest.Write(t, dir, "a.txt", "topic-a\n")
	gittest.Run(t, dir, "commit", "-qam", "t1 modify a")
	gittest.Run(t, dir, "merge", "--no-commit", "--no-ff", "side")
	gittest.Write(t, dir, "c.txt", "c1\nEVIL\nc3\n") // rides only in the merge commit
	gittest.Run(t, dir, "add", "c.txt")
	gittest.Run(t, dir, "commit", "-qm", "merge side (evil)")
	gittest.Run(t, dir, "checkout", "-q", "main")
	gittest.Write(t, dir, "c.txt", "c1\nMAIN\nc3\n")
	gittest.Run(t, dir, "commit", "-qam", "main modifies c")

	stdout, stderr, code := runCLI(t, dir, "topic", "--onto", "main", "--rebase")
	if code != int(core.CodeOK) {
		t.Fatalf("exit = %d (want 0); stderr=%s", code, stderr)
	}
	if !strings.Contains(stderr, "note:") || !strings.Contains(stderr, "merge commit") {
		t.Errorf("stderr should carry the merge-commit note: %q", stderr)
	}
	var r core.RebaseReport
	if err := json.Unmarshal([]byte(stdout), &r); err != nil {
		t.Fatalf("stdout not a RebaseReport: %v\n%s", err, stdout)
	}
	if r.Rebaseable {
		t.Error("the evil merge's delta collides with main; the simulation should conflict")
	}
	if r.Conflict == nil || r.Conflict.Subject != "merge side (evil)" {
		t.Fatalf("first conflict should be the merge commit: %+v", r.Conflict)
	}
	if len(r.Conflict.Conflicts) != 1 || r.Conflict.Conflicts[0].Path != "c.txt" {
		t.Errorf("conflict path should be c.txt: %+v", r.Conflict.Conflicts)
	}

	// The divergence the note warns about: a real rebase drops the merge commit
	// (and its evil edit), so the same rebase completes cleanly.
	gittest.Run(t, dir, "rebase", "main", "topic")
}

// --rebase and --path are mutually exclusive: reject with a usage error rather
// than silently ignoring one.
func TestEndToEnd_RebaseWithPathRejected(t *testing.T) {
	dir := rebaseRepo(t)
	_, stderr, code := runCLI(t, dir, "topic", "--onto", "main", "--rebase", "--path", "a.txt")
	if code != int(core.CodeValidation) {
		t.Fatalf("exit = %d, want 2; stderr=%s", code, stderr)
	}
	if !strings.Contains(stderr, "--path") || !strings.Contains(stderr, "--rebase") {
		t.Errorf("error should name both flags: %q", stderr)
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("no space left on device") }

// A stdout write failure is an IO error (exit 3), not a usage error (exit 2):
// the cobra→validation fallback in run() must apply only to bare cobra parse
// errors, never to a RunE result.
func TestEndToEnd_StdoutWriteErrorIsInternal(t *testing.T) {
	dir := gittest.ConflictRepo(t)
	oldRepo, oldOut, oldErr := newRepo, out, errOut
	var berr bytes.Buffer
	newRepo = func() probe.Git { return git.New(dir) }
	out, errOut = failWriter{}, &berr
	defer func() { newRepo, out, errOut = oldRepo, oldOut, oldErr }()

	code := run(context.Background(), []string{"ours", "--onto", "main"}) // a clean probe that reaches writeJSON
	if code != int(core.CodeInternal) {
		t.Errorf("exit = %d, want 3 (internal/IO) for a stdout write failure", code)
	}
}
