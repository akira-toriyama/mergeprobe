package core

import "sort"

// EmptyTreeOID is git's well-known empty-tree object. When two refs share no
// common ancestor, diffing each against this tree yields "everything each side
// contains", which lets both_touched / clean_merges still be computed instead of
// silently going blank.
const EmptyTreeOID = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// Report is mergeprobe's payload: the one-turn answer to "what does this
// topic/base pair conflict on, where, and how badly?" It is the stable stdout
// contract, so field names and slice-vs-null shapes must stay put. Build it,
// call Normalize, then render through the JSON funnel.
type Report struct {
	// Mergeable is true when the in-memory merge produced no conflicts.
	Mergeable bool `json:"mergeable"`
	// Base is the ref merged into ("ours"/stage 2 — what topic lands on).
	Base string `json:"base"`
	// Topic is the ref being evaluated ("theirs"/stage 3 — the incoming change).
	Topic string `json:"topic"`
	// MergeBase is the short OID of the common ancestor, or "" for unrelated
	// histories (no common ancestor).
	MergeBase string `json:"merge_base,omitempty"`
	// Conflicts lists every conflicted path. Always [] (never null) after
	// Normalize, sorted by path.
	Conflicts []Conflict `json:"conflicts"`
	// CleanMerges counts files integrated by the merge without conflict — the
	// "117 files merged fine, 1 conflicted" denominator.
	CleanMerges int `json:"clean_merges"`
	// BothTouchedClean lists files both sides changed that merge-tree merged
	// cleanly — the semantic-conflict blind spot no other tool reports. Always []
	// after Normalize, sorted.
	BothTouchedClean []string `json:"both_touched_clean"`
}

// Conflict is one conflicted path in a Report.
type Conflict struct {
	// Path is the repository-relative file path.
	Path string `json:"path"`
	// Class is the structural resolution class (see Class).
	Class Class `json:"class"`
	// Binary is true when the conflicted content is binary (no text merge
	// possible); Sample is then empty and Hunks is 0. Omitted when false.
	Binary bool `json:"binary,omitempty"`
	// Hunks is the number of conflict regions in the merged file (0 for binary
	// or non-content conflicts like modify/delete).
	Hunks int `json:"hunks"`
	// Sample is a bounded excerpt of the first conflict region with its markers,
	// for a one-glance read. Empty when there is nothing textual to show.
	Sample string `json:"sample,omitempty"`
	// Truncated is true when the full conflict content is not in Sample — either
	// the sample was capped mid-region (use --path for the rest) or the blob was
	// too large to read at all (inspect the file directly). Omitted when false.
	Truncated bool `json:"truncated,omitempty"`
}

// Normalize makes the report safe for a machine consumer: nil slices become
// empty (so JSON renders [] not null) and both lists are sorted by path for
// stable, diffable output. Call it once before rendering.
func (r *Report) Normalize() {
	if r.Conflicts == nil {
		r.Conflicts = []Conflict{}
	}
	if r.BothTouchedClean == nil {
		r.BothTouchedClean = []string{}
	}
	sort.Slice(r.Conflicts, func(i, j int) bool { return r.Conflicts[i].Path < r.Conflicts[j].Path })
	sort.Strings(r.BothTouchedClean)
}
