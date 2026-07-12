// Package git is the read-only git adapter that satisfies probe.Git by shelling
// out to the git binary. It captures each child's stdout/stderr into its own
// buffers (never inheriting the parent's), classifies failures as *core.Error at
// the point they happen, and forces LC_ALL=C so diagnostics are stable. It holds
// no domain logic — parsing and verdicts live in core/probe.
//
// merge-tree --write-tree writes tree/blob objects into .git/objects but never
// touches the index, HEAD, or the worktree, so the "worktree untouched" promise
// holds; the loose objects are harmless and garbage-collected.
package git

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"

	"github.com/akira-toriyama/mergeprobe/internal/core"
)

// Repo runs git commands in dir (empty = the process working directory, letting
// git discover the repo upward).
type Repo struct {
	dir string
	bin string
}

// New returns a Repo rooted at dir (use "" for the current working directory).
func New(dir string) *Repo { return &Repo{dir: dir, bin: "git"} }

// run executes git with args, returning stdout, stderr, the exit code, and a
// start error (nonzero exit is not itself an error here — callers decide what a
// given code means). LC_ALL=C keeps messages/collation stable.
func (r *Repo) run(ctx context.Context, args ...string) (stdout, stderr []byte, code int, startErr error) {
	// #nosec G204 -- args are a fixed git subcommand plus refs/paths that are
	// either git-derived or validated by the caller (no leading dash; see
	// probe.validateRef and the --end-of-options guards below). No shell is
	// involved: exec passes an explicit argv, so there is no shell injection.
	cmd := exec.CommandContext(ctx, r.bin, args...)
	cmd.Dir = r.dir
	cmd.Env = append(cmd.Environ(), "LC_ALL=C")
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	code = 0
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		} else {
			// git failed to start (not installed, permission) — a real internal
			// failure distinct from any git exit code.
			return out.Bytes(), errb.Bytes(), -1, core.Internalf("git-exec",
				"could not run git: %v", err)
		}
	}
	return out.Bytes(), errb.Bytes(), code, nil
}

// gitError classifies a git failure by its stderr into the exit-code contract.
func gitError(id string, stderr []byte, code int) error {
	msg := distill(stderr)
	if strings.Contains(msg, "not a git repository") {
		return core.Validationf("not-a-repo", "not inside a git repository")
	}
	return core.Internalf(id, "git failed (exit %d): %s", code, msg)
}

// distill reduces git's stderr to its most diagnostic single line: the first
// fatal:/error:/warning: line, else the last non-empty line, else "".
func distill(stderr []byte) string {
	lines := strings.Split(strings.TrimRight(string(stderr), "\n"), "\n")
	last := ""
	for _, ln := range lines {
		t := strings.TrimSpace(ln)
		if t == "" {
			continue
		}
		last = t
		if strings.HasPrefix(t, "fatal:") || strings.HasPrefix(t, "error:") {
			return t
		}
	}
	return last
}

// ResolveCommit resolves ref to a commit OID, returning a validation error for
// an unknown ref.
func (r *Repo) ResolveCommit(ctx context.Context, ref string) (string, error) {
	// --end-of-options makes a ref that looks like a flag (e.g. "-x") parse as a
	// literal rev rather than an option, so it fails to resolve instead of being
	// misinterpreted.
	out, errb, code, err := r.run(ctx, "rev-parse", "--verify", "--quiet", "--end-of-options", ref+"^{commit}")
	if err != nil {
		return "", err
	}
	if code == 1 {
		return "", core.Validationf("unknown-ref", "cannot resolve %q to a commit", ref)
	}
	if code != 0 {
		return "", gitError("rev-parse", errb, code)
	}
	return strings.TrimSpace(string(out)), nil
}

// DefaultBase resolves the ref a topic lands on by default: git's origin/HEAD.
func (r *Repo) DefaultBase(ctx context.Context) (string, error) {
	out, _, code, err := r.run(ctx, "symbolic-ref", "--short", "refs/remotes/origin/HEAD")
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", core.Validationf("no-default-base",
			"cannot determine the default base (origin/HEAD is unset); pass --onto <ref>")
	}
	return strings.TrimSpace(string(out)), nil
}

// MergeTree runs the in-memory merge. Exit 0 = clean, exit 1 = conflicted; any
// other code is a hard failure. --allow-unrelated-histories lets refs with no
// common ancestor merge (as add/add against the empty base) instead of git
// refusing with exit 128, so the probe reports them gracefully.
func (r *Repo) MergeTree(ctx context.Context, base, topic string) ([]byte, bool, error) {
	out, errb, code, err := r.run(ctx, "merge-tree", "--write-tree", "--allow-unrelated-histories", "-z", base, topic)
	if err != nil {
		return nil, false, err
	}
	switch code {
	case 0:
		return out, false, nil
	case 1:
		return out, true, nil
	default:
		return nil, false, gitError("merge-tree", errb, code)
	}
}

// Fetch retrieves ref (a pull ref like refs/pull/N/head, or a branch name) from
// source (a remote name or URL) into FETCH_HEAD and returns the fetched commit
// OID. Pinning the OID lets a later merge-tree run against a stable object
// rather than a ref another fetch could move. --no-tags keeps the footprint to
// FETCH_HEAD plus the fetched objects — no remote-tracking ref is written — and,
// like the merge-tree write, this touches .git/objects but never the index,
// HEAD, or the worktree.
func (r *Repo) Fetch(ctx context.Context, source, ref string) (string, error) {
	if err := rejectDash("fetch source", source); err != nil {
		return "", err
	}
	if err := rejectDash("fetch ref", ref); err != nil {
		return "", err
	}
	_, errb, code, err := r.run(ctx, "fetch", "--quiet", "--no-tags", source, ref)
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", fetchError(errb, code)
	}
	out, errb, code, err := r.run(ctx, "rev-parse", "--verify", "--quiet", "FETCH_HEAD")
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", gitError("rev-parse", errb, code)
	}
	return strings.TrimSpace(string(out)), nil
}

// Remotes maps each configured remote name to its fetch URL, so a owner/repo#N
// reference can be matched to a remote that already points at that repository.
func (r *Repo) Remotes(ctx context.Context) (map[string]string, error) {
	out, errb, code, err := r.run(ctx, "remote", "-v")
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, gitError("remote", errb, code)
	}
	return parseRemotes(out), nil
}

// parseRemotes decodes `git remote -v` fetch lines ("<name>\t<url> (fetch)")
// into a name→URL map.
func parseRemotes(out []byte) map[string]string {
	m := map[string]string{}
	for _, ln := range strings.Split(string(out), "\n") {
		ln = strings.TrimSpace(ln)
		if !strings.HasSuffix(ln, "(fetch)") {
			continue
		}
		tab := strings.IndexByte(ln, '\t')
		if tab < 0 {
			continue
		}
		name := ln[:tab]
		url := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(ln[tab+1:]), "(fetch)"))
		if name != "" && url != "" {
			m[name] = url
		}
	}
	return m
}

// rejectDash refuses a leading-dash value that git could parse as an option
// (argument injection), the same guard probe.validateRef applies to refs.
func rejectDash(field, v string) error {
	if v == "" {
		return core.Validationf("empty-arg", "%s must not be empty", field)
	}
	if v[0] == '-' {
		return core.Validationf("dash-arg", "%s %q must not start with '-'", field, v)
	}
	return nil
}

// fetchError classifies a `git fetch` failure: a missing remote ref is a soft
// not-found (so `mergeprobe 999` reports cleanly), anything else defers to the
// shared classifier.
func fetchError(stderr []byte, code int) error {
	msg := distill(stderr)
	if strings.Contains(msg, "couldn't find remote ref") || strings.Contains(msg, "not our ref") {
		return core.NotFoundf("fetch-no-ref", "remote ref not found: %s", msg)
	}
	return gitError("fetch", stderr, code)
}

// CommitsToReplay lists the commits a rebase of topic onto base would replay —
// base..topic, oldest-first in topological order — each with its first parent
// (the merge base for that replay step), subject, and merge-commit flag. An
// empty range (topic already on base) yields no commits. Parent is never
// empty: a true root commit (unrelated-history topic) gets the repository's
// empty tree — its delta is everything it introduces — while a parentless
// commit in a shallow clone is a graft boundary whose real parents are hidden
// (and whose log truncates the replay list itself), so the range is rejected
// with a validation error rather than simulated untruthfully.
func (r *Repo) CommitsToReplay(ctx context.Context, base, topic string) ([]core.Commit, error) {
	// %H <first-and-other-parents> \x1f <subject>, one line per commit. %s is a
	// single line, so the \n record separator is unambiguous; \x1f separates the
	// hash/parent list from the subject without risking a space collision.
	out, errb, code, err := r.run(ctx, "log", "--reverse", "--topo-order",
		"--format=%H %P%x1f%s", "--end-of-options", base+".."+topic)
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, gitError("log", errb, code)
	}
	commits := parseCommitLog(out)
	for i := range commits {
		if commits[i].Parent != "" {
			continue
		}
		shallow, err := r.isShallow(ctx)
		if err != nil {
			return nil, err
		}
		if shallow {
			return nil, core.Validationf("shallow-history",
				"cannot simulate a rebase: %s..%s crosses the shallow-clone boundary at %.12s (its parents are hidden); fetch the full history (git fetch --unshallow) and retry",
				base, topic, commits[i].OID)
		}
		empty, err := r.EmptyTree(ctx)
		if err != nil {
			return nil, err
		}
		commits[i].Parent = empty
	}
	return commits, nil
}

// isShallow reports whether the repository is a shallow clone — where a
// parentless commit in a range is a graft boundary rather than a true root.
// Queried only when such a commit appears, so the common case pays nothing.
func (r *Repo) isShallow(ctx context.Context) (bool, error) {
	out, errb, code, err := r.run(ctx, "rev-parse", "--is-shallow-repository")
	if err != nil {
		return false, err
	}
	if code != 0 {
		return false, gitError("rev-parse", errb, code)
	}
	return strings.TrimSpace(string(out)) == "true", nil
}

// EmptyTree returns the empty tree's OID in the repository's object format.
// hash-object computes it (sha1: the well-known 4b825dc6…, sha256: its own
// hash), so a sha256 repository gets a resolvable OID where a hardcoded sha1
// value would make merge-tree or diff die with an unparseable object. Stdin
// is nil (empty).
func (r *Repo) EmptyTree(ctx context.Context) (string, error) {
	out, errb, code, err := r.run(ctx, "hash-object", "-t", "tree", "--stdin")
	if err != nil {
		return "", err
	}
	if code != 0 {
		return "", gitError("hash-object", errb, code)
	}
	return strings.TrimSpace(string(out)), nil
}

// parseCommitLog decodes CommitsToReplay's "%H %P\x1f%s" lines. The first parent
// is the second space-separated token before the \x1f; a parentless commit
// leaves Parent empty for CommitsToReplay to resolve (root vs shallow
// boundary), and a second parent marks a merge commit.
func parseCommitLog(out []byte) []core.Commit {
	var commits []core.Commit
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if line == "" {
			continue
		}
		meta, subject, _ := strings.Cut(line, "\x1f")
		fields := strings.Fields(meta)
		if len(fields) == 0 {
			continue
		}
		c := core.Commit{OID: fields[0], Subject: subject, Merge: len(fields) > 2}
		if len(fields) > 1 {
			c.Parent = fields[1] // first parent = the rebase step's merge base
		}
		commits = append(commits, c)
	}
	return commits
}

// MergeTree3 runs a 3-way merge with an explicit merge base — cherry-pick /
// rebase-step semantics: apply theirs's delta-from-mergeBase onto ours. ours may
// be a running tree OID (not a commit), so a rebase can be replayed a commit at a
// time. Exit 0 = clean, 1 = conflicted, any other code a hard failure. Like
// MergeTree it writes only objects, never the worktree.
func (r *Repo) MergeTree3(ctx context.Context, mergeBase, ours, theirs string) ([]byte, bool, error) {
	out, errb, code, err := r.run(ctx, "merge-tree", "--write-tree", "--allow-unrelated-histories",
		"-z", "--merge-base="+mergeBase, ours, theirs)
	if err != nil {
		return nil, false, err
	}
	switch code {
	case 0:
		return out, false, nil
	case 1:
		return out, true, nil
	default:
		return nil, false, gitError("merge-tree", errb, code)
	}
}

// MergeBase returns the common ancestor, ok=false when the refs are unrelated.
func (r *Repo) MergeBase(ctx context.Context, a, b string) (string, bool, error) {
	out, errb, code, err := r.run(ctx, "merge-base", "--end-of-options", a, b)
	if err != nil {
		return "", false, err
	}
	switch code {
	case 0:
		return strings.TrimSpace(string(out)), true, nil
	case 1:
		return "", false, nil
	default:
		return "", false, gitError("merge-base", errb, code)
	}
}

// DiffNames lists paths that differ between from and to.
func (r *Repo) DiffNames(ctx context.Context, from, to string) ([]string, error) {
	out, errb, code, err := r.run(ctx, "diff", "--name-only", "-z", "--end-of-options", from, to)
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, gitError("diff", errb, code)
	}
	return splitNUL(out), nil
}

// ShowBlob returns the content of <treeish>:<path>.
func (r *Repo) ShowBlob(ctx context.Context, treeish, path string) ([]byte, error) {
	out, errb, code, err := r.run(ctx, "cat-file", "-p", treeish+":"+path)
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, gitError("cat-file", errb, code)
	}
	return out, nil
}

// BlobSize returns the byte size of <treeish>:<path> without reading its
// content. A path absent from the tree (e.g. a modify/delete surviving on the
// other side) yields a cat-file error the caller degrades to "no sample".
func (r *Repo) BlobSize(ctx context.Context, treeish, path string) (int64, error) {
	out, errb, code, err := r.run(ctx, "cat-file", "-s", treeish+":"+path)
	if err != nil {
		return 0, err
	}
	if code != 0 {
		return 0, gitError("cat-file", errb, code)
	}
	n, perr := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	if perr != nil {
		return 0, core.Internalf("cat-file-size", "unparseable object size %q: %v", out, perr)
	}
	return n, nil
}

// ConflictMarkerSize returns the effective conflict-marker-size for path in
// tree — the number of marker characters merge-tree wrote for it. It reads the
// attribute from the given tree (--source), so it matches exactly what
// merge-tree saw, and returns core.DefaultMarkerSize when the attribute is unset
// or unspecified.
func (r *Repo) ConflictMarkerSize(ctx context.Context, tree, path string) (int, error) {
	out, errb, code, err := r.run(ctx, "check-attr", "--source", tree, "conflict-marker-size", "--", path)
	if err != nil {
		return 0, err
	}
	if code != 0 {
		return 0, gitError("check-attr", errb, code)
	}
	// Output: "<path>: conflict-marker-size: <value>". value is "unspecified"
	// (attribute absent), "unset"/"set", or a number; only a number overrides.
	value := ""
	if i := strings.LastIndex(string(out), ": "); i >= 0 {
		value = strings.TrimSpace(string(out)[i+2:])
	}
	n, perr := strconv.Atoi(value)
	if perr != nil || n < 1 {
		return core.DefaultMarkerSize, nil
	}
	return n, nil
}

// splitNUL splits NUL-delimited output into fields, dropping the trailing empty
// element git leaves after the final terminator.
func splitNUL(b []byte) []string {
	if len(b) == 0 {
		return nil
	}
	parts := strings.Split(string(b), "\x00")
	if n := len(parts); n > 0 && parts[n-1] == "" {
		parts = parts[:n-1]
	}
	if len(parts) == 0 {
		return nil
	}
	return parts
}
