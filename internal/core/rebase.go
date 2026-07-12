package core

import "sort"

// Commit is a topic commit to replay in a rebase simulation: its OID, its first
// parent (the merge base for that replay step), and its subject for the report.
// It lives in core so both the git adapter (which produces it) and the probe use
// case (which consumes it) can name it without either depending on the other.
type Commit struct {
	OID     string
	Parent  string
	Subject string
}

// RebaseReport answers "does <topic> rebase cleanly onto <base>, and if not,
// which commit first conflicts and where?" — the payload for --rebase. Like
// Report it is a stable stdout contract: build it, call Normalize, then render.
type RebaseReport struct {
	// Rebaseable is true when every topic commit replays onto base with no conflict.
	Rebaseable bool `json:"rebaseable"`
	// Base is the ref the topic's commits are replayed onto.
	Base string `json:"base"`
	// Topic is the ref whose commits are replayed.
	Topic string `json:"topic"`
	// Commits is the number of commits in base..topic — the replay length.
	Commits int `json:"commits"`
	// Applied is how many commits replayed cleanly before a conflict stopped the
	// simulation; it equals Commits when Rebaseable.
	Applied int `json:"applied"`
	// Conflict is the first commit that failed to replay, or nil for a clean
	// rebase. Rebase stops at the first conflict (a real rebase does too, and the
	// running tree past a conflict is not meaningfully replayable), so at most one
	// is reported.
	Conflict *RebaseConflict `json:"conflict,omitempty"`
}

// RebaseConflict is the first conflicting commit in a rebase simulation and the
// conflicts its replay produced — reusing the static probe's Conflict shape, so
// path/class/hunks/sample read identically.
type RebaseConflict struct {
	// Commit is the short OID of the first commit that failed to replay.
	Commit string `json:"commit"`
	// Subject is that commit's subject line.
	Subject string `json:"subject"`
	// Conflicts lists the paths its replay conflicted on. Always [] after
	// Normalize, sorted by path.
	Conflicts []Conflict `json:"conflicts"`
}

// Normalize makes the rebase report safe for a machine consumer: the conflict's
// path list is non-nil (renders [] not null) and sorted for stable output.
func (r *RebaseReport) Normalize() {
	if r.Conflict == nil {
		return
	}
	if r.Conflict.Conflicts == nil {
		r.Conflict.Conflicts = []Conflict{}
	}
	sort.Slice(r.Conflict.Conflicts, func(i, j int) bool {
		return r.Conflict.Conflicts[i].Path < r.Conflict.Conflicts[j].Path
	})
}
