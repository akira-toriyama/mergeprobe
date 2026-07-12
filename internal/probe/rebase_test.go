package probe

import (
	"context"
	"strings"
	"testing"

	"github.com/akira-toriyama/mergeprobe/internal/core"
)

func twoCommits() []core.Commit {
	return []core.Commit{
		{OID: strings.Repeat("a", 40), Parent: "p1", Subject: "c1 add b"},
		{OID: strings.Repeat("b", 40), Parent: "p2", Subject: "c2 modify a"},
	}
}

// Every topic commit replays cleanly → rebaseable, no conflict, applied == all.
func TestRunRebase_Clean(t *testing.T) {
	steps := 0
	g := fakeGit{
		commits: func(base, topic string) ([]core.Commit, error) { return twoCommits(), nil },
		mergeTree3: func(mergeBase, ours, theirs string) ([]byte, bool, error) {
			steps++
			return z("T-" + theirs), false, nil
		},
	}
	r, notes, err := RunRebase(context.Background(), g, Options{Topic: "topic", Base: "main"})
	if err != nil {
		t.Fatalf("RunRebase: %v", err)
	}
	if len(notes) != 0 {
		t.Errorf("linear topic should carry no notes, got %v", notes)
	}
	if !r.Rebaseable {
		t.Error("clean rebase reported not rebaseable")
	}
	if r.Commits != 2 || r.Applied != 2 {
		t.Errorf("commits/applied = %d/%d, want 2/2", r.Commits, r.Applied)
	}
	if r.Conflict != nil {
		t.Errorf("clean rebase carries a conflict: %+v", r.Conflict)
	}
	if steps != 2 {
		t.Errorf("replayed %d steps, want 2", steps)
	}
	if r.Base != "main" || r.Topic != "topic" {
		t.Errorf("base/topic = %q/%q", r.Base, r.Topic)
	}
}

// The running tree carries forward: step 1 replays onto base, step 2 onto step
// 1's result tree, and each step's merge base is that commit's parent.
func TestRunRebase_RunningTreeAndMergeBaseThread(t *testing.T) {
	var ours, bases []string
	g := fakeGit{
		commits: func(base, topic string) ([]core.Commit, error) { return twoCommits(), nil },
		mergeTree3: func(mergeBase, o, theirs string) ([]byte, bool, error) {
			ours = append(ours, o)
			bases = append(bases, mergeBase)
			return z("T-" + theirs), false, nil
		},
	}
	if _, _, err := RunRebase(context.Background(), g, Options{Topic: "topic", Base: "main"}); err != nil {
		t.Fatalf("RunRebase: %v", err)
	}
	wantOurs := []string{"main", "T-" + strings.Repeat("a", 40)}
	if len(ours) != 2 || ours[0] != wantOurs[0] || ours[1] != wantOurs[1] {
		t.Errorf("running-tree threading = %v, want %v", ours, wantOurs)
	}
	if len(bases) != 2 || bases[0] != "p1" || bases[1] != "p2" {
		t.Errorf("merge bases = %v, want [p1 p2]", bases)
	}
}

// A commit that fails to replay stops the simulation at that commit, reports it
// (short OID + subject) and its conflicts, and records how many applied first.
func TestRunRebase_ConflictStopsAtCommit(t *testing.T) {
	g := fakeGit{
		commits: func(base, topic string) ([]core.Commit, error) { return twoCommits(), nil },
		mergeTree3: func(mergeBase, ours, theirs string) ([]byte, bool, error) {
			if theirs == strings.Repeat("b", 40) {
				return conflictBytes(), true, nil // second commit conflicts
			}
			return z("cleantree"), false, nil
		},
		showBlob: func(treeish, path string) ([]byte, error) { return markeredBlob(), nil },
	}
	r, _, err := RunRebase(context.Background(), g, Options{Topic: "topic", Base: "main"})
	if err != nil {
		t.Fatalf("RunRebase: %v", err)
	}
	if r.Rebaseable {
		t.Error("a conflicting rebase must not be rebaseable")
	}
	if r.Applied != 1 {
		t.Errorf("applied = %d, want 1 (c1 applied, c2 conflicted)", r.Applied)
	}
	if r.Conflict == nil {
		t.Fatal("no conflict reported")
	}
	if r.Conflict.Subject != "c2 modify a" {
		t.Errorf("conflict subject = %q", r.Conflict.Subject)
	}
	if r.Conflict.Commit != strings.Repeat("b", 12) {
		t.Errorf("conflict commit = %q, want a 12-char short OID", r.Conflict.Commit)
	}
	if len(r.Conflict.Conflicts) != 1 || r.Conflict.Conflicts[0].Path != "f.txt" {
		t.Errorf("conflicts = %+v", r.Conflict.Conflicts)
	}
	if !strings.Contains(r.Conflict.Conflicts[0].Sample, "<<<<<<<") {
		t.Errorf("conflict sample lacks markers: %q", r.Conflict.Conflicts[0].Sample)
	}
}

// Nothing to replay (topic already on base) is a clean, zero-commit rebase.
func TestRunRebase_NoCommits(t *testing.T) {
	g := fakeGit{commits: func(base, topic string) ([]core.Commit, error) { return nil, nil }}
	r, _, err := RunRebase(context.Background(), g, Options{Topic: "topic", Base: "main"})
	if err != nil {
		t.Fatalf("RunRebase: %v", err)
	}
	if !r.Rebaseable || r.Commits != 0 || r.Applied != 0 || r.Conflict != nil {
		t.Errorf("empty range = %+v, want rebaseable/0/0/nil", r)
	}
}

// Bad input is rejected before any replay (the shared ref resolution).
func TestRunRebase_UnknownRef(t *testing.T) {
	g := fakeGit{
		resolve: func(ref string) (string, error) {
			if ref == "bogus" {
				return "", core.Validationf("unknown-ref", "no")
			}
			return ref + "-oid", nil
		},
		commits: func(base, topic string) ([]core.Commit, error) {
			t.Fatal("CommitsToReplay should not run for a bad ref")
			return nil, nil
		},
	}
	_, _, err := RunRebase(context.Background(), g, Options{Topic: "bogus", Base: "main"})
	if ce := core.AsError(err); ce == nil || ce.Code != core.CodeValidation {
		t.Errorf("want validation error, got %v", err)
	}
}

// PR-mode labels flow into the rebase report just as they do the static one.
func TestRunRebase_UsesLabels(t *testing.T) {
	g := fakeGit{
		commits:    func(base, topic string) ([]core.Commit, error) { return nil, nil },
		mergeTree3: func(mergeBase, ours, theirs string) ([]byte, bool, error) { return z("t"), false, nil },
	}
	r, _, err := RunRebase(context.Background(), g, Options{Topic: "boid", Base: "boid2", TopicLabel: "#7", BaseLabel: "main"})
	if err != nil {
		t.Fatalf("RunRebase: %v", err)
	}
	if r.Topic != "#7" || r.Base != "main" {
		t.Errorf("labels not used: topic=%q base=%q", r.Topic, r.Base)
	}
}

// twoFileConflictBytes is a conflicted merge-tree -z payload naming two files,
// so drill-down tests can prove the narrowing to one.
func twoFileConflictBytes() []byte {
	return z(
		"tree2",
		"100644 aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa 1\tf.txt",
		"100644 bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb 2\tf.txt",
		"100644 cccccccccccccccccccccccccccccccccccccccc 3\tf.txt",
		"100644 dddddddddddddddddddddddddddddddddddddddd 1\tg.txt",
		"100644 1111111111111111111111111111111111111111 2\tg.txt",
		"100644 2222222222222222222222222222222222222222 3\tg.txt",
		"", "1", "f.txt", "CONFLICT (contents)", "CONFLICT (content): Merge conflict in f.txt\n",
		"1", "g.txt", "CONFLICT (contents)", "CONFLICT (content): Merge conflict in g.txt\n",
	)
}

// --path narrows a conflicting replay to that one file, with the fuller
// drill-down sample (every hunk, not just the first).
func TestRunRebase_DrillDown(t *testing.T) {
	g := fakeGit{
		commits: func(base, topic string) ([]core.Commit, error) { return twoCommits(), nil },
		mergeTree3: func(mergeBase, ours, theirs string) ([]byte, bool, error) {
			return twoFileConflictBytes(), true, nil
		},
		showBlob: func(treeish, path string) ([]byte, error) { return markeredBlob(), nil },
	}
	r, _, err := RunRebase(context.Background(), g, Options{Topic: "topic", Base: "main", Path: "g.txt"})
	if err != nil {
		t.Fatalf("RunRebase: %v", err)
	}
	if r.Conflict == nil || len(r.Conflict.Conflicts) != 1 || r.Conflict.Conflicts[0].Path != "g.txt" {
		t.Fatalf("drill-down should isolate g.txt: %+v", r.Conflict)
	}
	if s := r.Conflict.Conflicts[0].Sample; !strings.Contains(s, "ours2") {
		t.Errorf("drill-down sample should span every hunk, got %q", s)
	}
}

// --path on a file the first conflicting commit does not conflict on is a
// not-found, mirroring the static probe's contract.
func TestRunRebase_DrillDownPathNotConflicted(t *testing.T) {
	g := fakeGit{
		commits: func(base, topic string) ([]core.Commit, error) { return twoCommits(), nil },
		mergeTree3: func(mergeBase, ours, theirs string) ([]byte, bool, error) {
			return conflictBytes(), true, nil
		},
	}
	_, _, err := RunRebase(context.Background(), g, Options{Topic: "topic", Base: "main", Path: "nope.txt"})
	if ce := core.AsError(err); ce == nil || ce.Code != core.CodeNotFound {
		t.Errorf("want not-found error, got %v", err)
	}
}

// --path on a rebase that replays cleanly has nothing to drill into: not-found,
// like asking the static probe about a file that did not conflict.
func TestRunRebase_DrillDownCleanRebase(t *testing.T) {
	g := fakeGit{
		commits:    func(base, topic string) ([]core.Commit, error) { return twoCommits(), nil },
		mergeTree3: func(mergeBase, ours, theirs string) ([]byte, bool, error) { return z("t"), false, nil },
	}
	_, _, err := RunRebase(context.Background(), g, Options{Topic: "topic", Base: "main", Path: "f.txt"})
	if ce := core.AsError(err); ce == nil || ce.Code != core.CodeNotFound {
		t.Errorf("want not-found error for a clean rebase, got %v", err)
	}
}

// A topic containing merge commits is replayed by first-parent approximation
// (a real rebase drops merges), so RunRebase says so in a note the CLI prints
// to stderr — instead of silently diverging from git rebase.
func TestRunRebase_MergeCommitNote(t *testing.T) {
	commits := []core.Commit{
		{OID: strings.Repeat("a", 40), Parent: "p1", Subject: "c1"},
		{OID: strings.Repeat("e", 40), Parent: "p2", Subject: "merge side", Merge: true},
	}
	g := fakeGit{
		commits:    func(base, topic string) ([]core.Commit, error) { return commits, nil },
		mergeTree3: func(mergeBase, ours, theirs string) ([]byte, bool, error) { return z("t"), false, nil },
	}
	r, notes, err := RunRebase(context.Background(), g, Options{Topic: "topic", Base: "main"})
	if err != nil {
		t.Fatalf("RunRebase: %v", err)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "merge commit") {
		t.Fatalf("want one merge-commit note, got %v", notes)
	}
	if !strings.Contains(notes[0], "main..topic") {
		t.Errorf("note should name the range: %q", notes[0])
	}
	// The note is advisory: the verdict itself is untouched.
	if !r.Rebaseable || r.Applied != 2 {
		t.Errorf("noted rebase = %+v, want rebaseable/applied 2", r)
	}
}

// A merge commit the simulation never reached must not be noted: when the
// first conflict precedes every merge in the range, the replayed prefix is
// linear and the verdict matches a real rebase exactly — a note would be a
// false alarm.
func TestRunRebase_NoNoteWhenConflictPrecedesMerge(t *testing.T) {
	commits := []core.Commit{
		{OID: strings.Repeat("a", 40), Parent: "p1", Subject: "c1"},
		{OID: strings.Repeat("e", 40), Parent: "p2", Subject: "merge side", Merge: true},
	}
	g := fakeGit{
		commits: func(base, topic string) ([]core.Commit, error) { return commits, nil },
		mergeTree3: func(mergeBase, ours, theirs string) ([]byte, bool, error) {
			if theirs == strings.Repeat("a", 40) {
				return conflictBytes(), true, nil // first commit conflicts; merge never replays
			}
			return z("t"), false, nil
		},
		showBlob: func(treeish, path string) ([]byte, error) { return markeredBlob(), nil },
	}
	r, notes, err := RunRebase(context.Background(), g, Options{Topic: "topic", Base: "main"})
	if err != nil {
		t.Fatalf("RunRebase: %v", err)
	}
	if len(notes) != 0 {
		t.Errorf("merge commit was never replayed; want no notes, got %v", notes)
	}
	if r.Rebaseable || r.Conflict == nil || r.Conflict.Subject != "c1" {
		t.Errorf("conflict should stop at c1: %+v", r)
	}
}
