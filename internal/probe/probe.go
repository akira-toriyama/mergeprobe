// Package probe is mergeprobe's domain use case: given a base and a topic ref,
// run an in-memory merge and assemble the bounded-JSON verdict. It holds the
// orchestration logic and depends on the Git port (declared here, implemented by
// the git adapter) — so every branch is exercised with a fake, no real repo
// required. It does no process I/O of its own and never touches the worktree.
package probe

import (
	"context"
	"sort"

	"github.com/akira-toriyama/mergeprobe/internal/core"
)

// Git is the set of read-only git operations the probe needs. The real adapter
// shells out; tests supply a fake. Every method takes a context so a slow or
// wedged git subprocess unwinds on cancellation. Implementations classify their
// own failures as *core.Error.
type Git interface {
	// ResolveCommit resolves a ref to a commit OID, erroring (validation) on an
	// unknown ref — used to reject bad input with a clean message up front.
	ResolveCommit(ctx context.Context, ref string) (oid string, err error)
	// DefaultBase returns the ref a topic lands on when --onto is omitted
	// (git's origin/HEAD), erroring if it cannot be determined.
	DefaultBase(ctx context.Context) (ref string, err error)
	// MergeTree runs `git merge-tree --write-tree -z <base> <topic>` and returns
	// its raw stdout plus whether the merge conflicted (git exit 1). A hard
	// failure (bad ref, not a repo) is returned as err.
	MergeTree(ctx context.Context, base, topic string) (out []byte, conflicted bool, err error)
	// MergeBase returns the common-ancestor OID, ok=false when the two refs share
	// no history.
	MergeBase(ctx context.Context, a, b string) (oid string, ok bool, err error)
	// DiffNames lists the paths that differ between two treeish.
	DiffNames(ctx context.Context, from, to string) ([]string, error)
	// ShowBlob returns the content of <treeish>:<path> (the merged tree carries
	// conflict markers for text files).
	ShowBlob(ctx context.Context, treeish, path string) ([]byte, error)
}

// Options is a resolved probe request. Empty Topic means HEAD; empty Base means
// "resolve the default". A non-empty Path switches to single-file drill-down.
type Options struct {
	Topic string
	Base  string
	Path  string
}

const (
	// summarySampleLines caps the per-conflict sample in the default verdict.
	summarySampleLines = 20
	// drillSampleLines caps the fuller sample returned for --path; still bounded
	// so the tool never floods a turn, but generous enough to resolve by hand.
	drillSampleLines = 400
	// mergeBaseShortLen is how much of the merge-base OID the report shows.
	mergeBaseShortLen = 12
)

// Run executes the probe and returns the assembled report. It resolves the
// topic/base refs, runs the in-memory merge, parses it, and enriches the result
// with resolution classes, bounded samples, and the both-touched-clean set.
func Run(ctx context.Context, g Git, opts Options) (core.Report, error) {
	// Resolve and validate the topic first: it is the thing the caller named, so
	// its error (e.g. an agent's `mergeprobe 123`) should surface before an
	// unrelated "cannot determine default base" from resolving --onto.
	topic := opts.Topic
	if topic == "" {
		topic = "HEAD"
	}
	if err := validateRef("topic", topic); err != nil {
		return core.Report{}, err
	}
	if _, err := g.ResolveCommit(ctx, topic); err != nil {
		return core.Report{}, err
	}

	base := opts.Base
	if base == "" {
		b, err := g.DefaultBase(ctx)
		if err != nil {
			return core.Report{}, err
		}
		base = b
	}
	if err := validateRef("--onto", base); err != nil {
		return core.Report{}, err
	}
	if _, err := g.ResolveCommit(ctx, base); err != nil {
		return core.Report{}, err
	}

	out, conflicted, err := g.MergeTree(ctx, base, topic)
	if err != nil {
		return core.Report{}, err
	}
	parsed, err := core.ParseMergeTreeZ(out)
	if err != nil {
		return core.Report{}, err
	}

	report := core.Report{
		Base:      base,
		Topic:     topic,
		Mergeable: !conflicted && parsed.Clean(),
	}

	// Determine the diff base: the merge base, or the empty tree when the two
	// refs are unrelated (so both_touched / clean_merges stay meaningful).
	diffBase := core.EmptyTreeOID
	if mb, ok, err := g.MergeBase(ctx, base, topic); err != nil {
		return core.Report{}, err
	} else if ok {
		diffBase = mb
		report.MergeBase = shorten(mb)
	}

	baseChanged, err := g.DiffNames(ctx, diffBase, base)
	if err != nil {
		return core.Report{}, err
	}
	topicChanged, err := g.DiffNames(ctx, diffBase, topic)
	if err != nil {
		return core.Report{}, err
	}

	conflictedSet := make(map[string]bool, len(parsed.Files))
	for _, f := range parsed.Files {
		conflictedSet[f.Path] = true
	}

	report.BothTouchedClean = bothTouchedClean(baseChanged, topicChanged, conflictedSet)
	report.CleanMerges = len(union(baseChanged, topicChanged)) - len(conflictedSet)
	if report.CleanMerges < 0 {
		report.CleanMerges = 0
	}

	// Drill-down mode: isolate the one requested path and emit its fuller sample.
	if opts.Path != "" {
		if !conflictedSet[opts.Path] {
			return core.Report{}, core.NotFoundf("path-not-conflicted",
				"%q is not among the conflicted files (run without --path to list them)", opts.Path)
		}
		for _, f := range parsed.Files {
			if f.Path == opts.Path {
				report.Conflicts = []core.Conflict{buildConflict(ctx, g, parsed.Tree, f, drillSampleLines, true)}
				break
			}
		}
		report.Normalize()
		return report, nil
	}

	for _, f := range parsed.Files {
		report.Conflicts = append(report.Conflicts, buildConflict(ctx, g, parsed.Tree, f, summarySampleLines, false))
	}
	report.Normalize()
	return report, nil
}

// buildConflict fetches a conflicted file's merged content and derives its
// class, binariness, hunk count and bounded sample. A blob it cannot read
// (e.g. a modify/delete leaves no content, or the tree lacks the path) degrades
// to no sample rather than failing the whole probe. When allHunks is true the
// sample spans every hunk (drill-down); otherwise just the first.
func buildConflict(ctx context.Context, g Git, tree string, f core.ConflictFile, maxLines int, allHunks bool) core.Conflict {
	c := core.Conflict{Path: f.Path, Class: core.Classify(f)}
	blob, err := g.ShowBlob(ctx, tree, f.Path)
	if err != nil || len(blob) == 0 {
		return c
	}
	if core.IsBinary(blob) {
		c.Binary = true
		return c
	}
	hunks, n := core.ConflictHunks(blob)
	c.Hunks = n
	if allHunks {
		c.Sample, c.Truncated = core.BoundedSampleAll(hunks, maxLines)
	} else {
		c.Sample, c.Truncated = core.BoundedSample(hunks, maxLines)
	}
	return c
}

// validateRef rejects an empty or leading-dash ref before it reaches git, so a
// value that looks like a flag (e.g. "-x", "--upload-pack=…") can never be
// misparsed as a git option (argument injection). The house CLI rule: reject
// flag-shaped identifiers rather than silently misapplying them.
func validateRef(field, ref string) error {
	if ref == "" {
		return core.Validationf("empty-ref", "%s must not be empty", field)
	}
	if ref[0] == '-' {
		return core.Validationf("dash-ref", "%s %q must not start with '-'", field, ref)
	}
	return nil
}

func shorten(oid string) string {
	if len(oid) > mergeBaseShortLen {
		return oid[:mergeBaseShortLen]
	}
	return oid
}

// bothTouchedClean returns the paths both sides changed that are not conflicted
// — the semantic-conflict blind spot. Sorted for stable output.
func bothTouchedClean(a, b []string, conflicted map[string]bool) []string {
	bset := make(map[string]bool, len(b))
	for _, p := range b {
		bset[p] = true
	}
	var out []string
	seen := map[string]bool{}
	for _, p := range a {
		if bset[p] && !conflicted[p] && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Strings(out)
	return out
}

// union is the deduplicated set of paths from both lists.
func union(a, b []string) map[string]bool {
	u := make(map[string]bool, len(a)+len(b))
	for _, p := range a {
		u[p] = true
	}
	for _, p := range b {
		u[p] = true
	}
	return u
}
