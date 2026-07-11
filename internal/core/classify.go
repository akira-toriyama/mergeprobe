package core

import "bytes"

// Class is the structural resolution class of a conflict, derived purely from
// which index stages git emitted for the path. Stage presence is locale- and
// content-independent, so this classification never depends on parsing git's
// English messages. Binariness is orthogonal (see IsBinary / Conflict.Binary):
// a binary file conflicts with stages {1,2,3} identical to a text both-modified,
// so it cannot be a Class value — it is reported as a separate flag.
type Class string

const (
	// ClassBothModified: stages {1,2,3} — both sides changed the same file from a
	// common base. The classic content conflict.
	ClassBothModified Class = "both-modified"
	// ClassAddAdd: stages {2,3}, no base — both sides added the same path with
	// different content.
	ClassAddAdd Class = "add-add"
	// ClassModifyDelete: stages {1,2} or {1,3} — one side modified, the other
	// deleted. No content merge is possible; a human picks keep-or-delete.
	ClassModifyDelete Class = "modify-delete"
	// ClassDeleteDelete: stage {1} only — both sides deleted, but a rename/other
	// edit made git flag it. Rare.
	ClassDeleteDelete Class = "delete-delete"
	// ClassOther: any shape the cases above do not cover (e.g. a lone add stage,
	// exotic rename combinations). The stages are still reported.
	ClassOther Class = "other"
)

// Classify maps a conflicted path's present stages to its structural class.
func Classify(cf ConflictFile) Class {
	has := func(s int) bool { _, ok := cf.Stages[s]; return ok }
	switch {
	case has(1) && has(2) && has(3):
		return ClassBothModified
	case !has(1) && has(2) && has(3):
		return ClassAddAdd
	case has(1) && has(2) && !has(3):
		return ClassModifyDelete
	case has(1) && !has(2) && has(3):
		return ClassModifyDelete
	case has(1) && !has(2) && !has(3):
		return ClassDeleteDelete
	default:
		return ClassOther
	}
}

// binarySniffLen matches git's buffer_is_binary window: only the first 8000
// bytes are examined for a NUL.
const binarySniffLen = 8000

// IsBinary reports whether content should be treated as binary, using git's own
// heuristic: a NUL byte within the first 8000 bytes. Binary conflicts carry no
// usable text markers, so this drives Conflict.Binary and an empty sample.
func IsBinary(content []byte) bool {
	if len(content) > binarySniffLen {
		content = content[:binarySniffLen]
	}
	return bytes.IndexByte(content, 0) >= 0
}
