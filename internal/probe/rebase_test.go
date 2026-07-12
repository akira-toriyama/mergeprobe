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
