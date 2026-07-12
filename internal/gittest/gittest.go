// Package gittest builds throwaway git repositories for integration tests. It
// drives the real git binary (the house preference over mocking git), with a
// pinned committer identity and signing disabled so runs are deterministic and
// hermetic. Import it only from _test files.
package gittest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// SkipIfNoGit skips the test when git is unavailable, so a machine without git
// does not report a failure for something it cannot run.
func SkipIfNoGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}

// Init creates an empty repository in a fresh temp dir with a fixed identity and
// returns its path. The default branch is "main".
func Init(t *testing.T) string {
	t.Helper()
	SkipIfNoGit(t)
	dir := t.TempDir()
	Run(t, dir, "init", "-q", "-b", "main")
	Run(t, dir, "config", "user.email", "test@mergeprobe.local")
	Run(t, dir, "config", "user.name", "mergeprobe test")
	Run(t, dir, "config", "commit.gpgsign", "false")
	return dir
}

// Run executes git in dir and fails the test on error, returning trimmed stdout.
func Run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(), "LC_ALL=C")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// Write writes a file (creating parent dirs) inside the repo dir.
func Write(t *testing.T, dir, name, content string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", name, err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// Orphan creates an orphan branch (an unrelated history) named name containing
// exactly files, commits it with subject, and returns to main. The index and
// worktree inherited from the current branch are cleared first, so the root
// commit holds only what files names; an empty map yields an empty root.
func Orphan(t *testing.T, dir, name, subject string, files map[string]string) {
	t.Helper()
	Run(t, dir, "checkout", "-q", "--orphan", name)
	Run(t, dir, "rm", "-rfq", "--ignore-unmatch", "--", ".")
	for f, content := range files {
		Write(t, dir, f, content)
	}
	Run(t, dir, "add", "-A")
	Run(t, dir, "commit", "-q", "--allow-empty", "-m", subject)
	Run(t, dir, "checkout", "-q", "main")
}

// ConflictRepo builds a repo with a base commit and two divergent branches,
// "ours" and "theirs", colliding three ways: f.txt (content), addonly.txt
// (add/add), and d.txt (modify/delete — ours deletes, theirs modifies). main is
// left checked out and stays an ancestor of "ours" (so main..ours is a clean
// merge). It returns the repo path.
func ConflictRepo(t *testing.T) string {
	t.Helper()
	dir := Init(t)
	Write(t, dir, "f.txt", "line1\nline2\nline3\n")
	Write(t, dir, "d.txt", "del me\n")
	Write(t, dir, "untouched.txt", "keep\n")
	Run(t, dir, "add", ".")
	Run(t, dir, "commit", "-qm", "base")

	Run(t, dir, "checkout", "-qb", "ours")
	Write(t, dir, "f.txt", "OURS-1\nline2\nline3\n")
	Run(t, dir, "rm", "-q", "d.txt")
	Write(t, dir, "addonly.txt", "from ours\n")
	Run(t, dir, "add", "addonly.txt")
	Run(t, dir, "commit", "-qam", "ours")

	Run(t, dir, "checkout", "-q", "main")
	Run(t, dir, "checkout", "-qb", "theirs")
	Write(t, dir, "f.txt", "THEIRS-1\nline2\nline3\n")
	Write(t, dir, "d.txt", "del me but modified\n")
	Write(t, dir, "addonly.txt", "from theirs DIFFERENT\n")
	Run(t, dir, "add", ".")
	Run(t, dir, "commit", "-qam", "theirs")

	Run(t, dir, "checkout", "-q", "main")
	return dir
}
