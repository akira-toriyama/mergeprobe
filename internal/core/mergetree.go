package core

import "strings"

// This file decodes the NUL-delimited output of
// `git merge-tree --write-tree -z <base> <topic>` into pure Go values. It is
// deterministic and does no I/O — the merge itself happens in the git adapter;
// here we only parse the bytes it produced. The -z wire format (git 2.38+,
// verified against 2.53):
//
//	<tree-oid> NUL
//	( <mode> SP <oid> SP <stage> TAB <path> ) NUL   -- one per conflicted stage
//	NUL                                              -- section separator
//	( <count> NUL (<path> NUL)*count <type> NUL <message> NUL )  -- info records
//
// A clean merge emits only "<tree-oid> NUL"; the separator and later sections
// appear only when the merge conflicted (git exits 1). -z is chosen over the
// default format precisely because it never quotes paths and gives a structured
// type field per message, so parsing needs no locale-sensitive string matching.

// Blob is one side of a conflicted path: its file mode and object id.
type Blob struct {
	Mode string
	OID  string
}

// ConflictFile groups the conflicted-index stages for a single path. Stage 1 is
// the merge base, stage 2 the base/"ours" side (merge-tree's first arg), stage 3
// the topic/"theirs" side (second arg). Only the stages git emitted are present,
// so the key set alone identifies the conflict shape (see Classify).
type ConflictFile struct {
	Path   string
	Stages map[int]Blob
}

// InfoMessage is one of merge-tree's informational records. Type is the stable
// machine tag (e.g. "CONFLICT (contents)", "Auto-merging"); Message is the
// human-readable, potentially localized line. mergeprobe branches on Stages, not
// on Message, so Message is carried for humans only.
type InfoMessage struct {
	Paths   []string
	Type    string
	Message string
}

// MergeTree is the decoded result. Tree is always set. Files is empty exactly
// when the merge produced no conflicts.
type MergeTree struct {
	Tree     string
	Files    []ConflictFile
	Messages []InfoMessage
}

// Clean reports whether the merge conflicted with nothing.
func (m MergeTree) Clean() bool { return len(m.Files) == 0 }

// ParseMergeTreeZ decodes the -z output of git merge-tree --write-tree. It
// returns a validation error on any structurally malformed input rather than
// guessing, so a git-format change surfaces loudly instead of silently
// producing a wrong verdict.
func ParseMergeTreeZ(data []byte) (MergeTree, error) {
	if len(data) == 0 {
		return MergeTree{}, Validationf("merge-tree-parse", "empty merge-tree output")
	}
	// Split on NUL. Git terminates every field including the last, so a trailing
	// empty element is expected; drop only that one.
	fields := strings.Split(string(data), "\x00")
	if n := len(fields); n > 0 && fields[n-1] == "" {
		fields = fields[:n-1]
	}
	if len(fields) == 0 || fields[0] == "" {
		return MergeTree{}, Validationf("merge-tree-parse", "missing merged-tree OID")
	}

	mt := MergeTree{Tree: fields[0]}
	i := 1

	// Conflicted-file-info: one field per stage, grouped by path in git's
	// emission order (sorted by path then stage), until the empty separator field
	// or end of input (a clean merge has neither entries nor separator).
	index := map[string]int{}
	for i < len(fields) && fields[i] != "" {
		mode, oid, stage, path, err := parseStageEntry(fields[i])
		if err != nil {
			return MergeTree{}, err
		}
		pos, ok := index[path]
		if !ok {
			pos = len(mt.Files)
			index[path] = pos
			mt.Files = append(mt.Files, ConflictFile{Path: path, Stages: map[int]Blob{}})
		}
		mt.Files[pos].Stages[stage] = Blob{Mode: mode, OID: oid}
		i++
	}

	// Skip the section separator, if present.
	if i < len(fields) && fields[i] == "" {
		i++
	}

	// Informational messages: <count> NUL then count path fields then type then
	// message.
	for i < len(fields) {
		msg, next, err := parseInfoRecord(fields, i)
		if err != nil {
			return MergeTree{}, err
		}
		mt.Messages = append(mt.Messages, msg)
		i = next
	}
	return mt, nil
}

// parseStageEntry decodes "<mode> <oid> <stage>\t<path>".
func parseStageEntry(field string) (mode, oid string, stage int, path string, err error) {
	tab := strings.IndexByte(field, '\t')
	if tab < 0 {
		return "", "", 0, "", Validationf("merge-tree-parse", "conflicted-file entry has no TAB: %q", field)
	}
	meta, p := field[:tab], field[tab+1:]
	parts := strings.Split(meta, " ")
	if len(parts) != 3 {
		return "", "", 0, "", Validationf("merge-tree-parse", "conflicted-file entry wants <mode> <oid> <stage>: %q", field)
	}
	switch parts[2] {
	case "1", "2", "3":
		stage = int(parts[2][0] - '0')
	default:
		return "", "", 0, "", Validationf("merge-tree-parse", "conflicted-file stage not 1/2/3: %q", field)
	}
	if p == "" {
		return "", "", 0, "", Validationf("merge-tree-parse", "conflicted-file entry has empty path: %q", field)
	}
	return parts[0], parts[1], stage, p, nil
}

// parseInfoRecord reads one informational record beginning at fields[i] and
// returns it plus the index just past it. A record is <count> path fields, a
// type field, and a message field; anything shorter is truncated input.
func parseInfoRecord(fields []string, i int) (InfoMessage, int, error) {
	count, ok := atoiNonNeg(fields[i])
	if !ok {
		return InfoMessage{}, 0, Validationf("merge-tree-parse", "info record path-count not a number: %q", fields[i])
	}
	i++
	// Need count path fields + a type field + a message field.
	if i+count+2 > len(fields) {
		return InfoMessage{}, 0, Validationf("merge-tree-parse", "info record truncated (need %d paths + type + message)", count)
	}
	paths := append([]string(nil), fields[i:i+count]...)
	i += count
	typ := fields[i]
	message := fields[i+1]
	return InfoMessage{Paths: paths, Type: typ, Message: message}, i + 2, nil
}

// maxInfoPaths caps an info record's declared path count. Real records list a
// handful of paths; a value beyond this is malformed input, and the cap keeps
// the accumulator from overflowing int into a negative slice bound.
const maxInfoPaths = 1 << 20

// atoiNonNeg parses a small non-negative decimal count; returns ok=false for
// anything that is not all ASCII digits or that exceeds maxInfoPaths (which also
// makes overflow to a negative value impossible).
func atoiNonNeg(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
		if n > maxInfoPaths {
			return 0, false
		}
	}
	return n, true
}
