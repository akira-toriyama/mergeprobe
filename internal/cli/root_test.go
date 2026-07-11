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

// An agent's reflex is `mergeprobe 123`; the all-digit topic gets a clear
// pointer at the pending PR-number feature, exit 2, not a raw "unknown ref".
func TestEndToEnd_PRNumberHint(t *testing.T) {
	dir := gittest.ConflictRepo(t)
	_, stderr, code := runCLI(t, dir, "123")
	if code != int(core.CodeValidation) {
		t.Fatalf("exit = %d, want 2", code)
	}
	if !strings.Contains(stderr, "PR number") {
		t.Errorf("stderr should mention PR-number resolution: %q", stderr)
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
