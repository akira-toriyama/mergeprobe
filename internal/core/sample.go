package core

import (
	"fmt"
	"strings"
)

// The merged tree git merge-tree writes embeds ordinary conflict markers for
// text files (verified against git 2.53), so a bounded, human-legible sample is
// just a substring of `git show <tree>:<path>`. This file extracts and bounds
// those markers. It is pure: the git blob is fetched by the adapter and handed
// in here.

// DefaultMarkerSize is git's default conflict-marker-size: the number of marker
// characters in <<<<<<< / ======= / >>>>>>>. A file can lower it via the
// conflict-marker-size gitattribute, and merge-tree honors that, so the size is
// not a constant — ConflictHunks takes the effective size for the file.
const DefaultMarkerSize = 7

// ConflictHunks returns each conflict region in blob (its markers included) and
// their count. markerSize is the file's effective conflict-marker-size (7 by
// default; smaller when the conflict-marker-size gitattribute lowered it, which
// merge-tree obeys) — a value < 1 is treated as the default. A blob with no
// markers — a clean-but-both-touched file, or a binary file — yields zero hunks.
// An unterminated opening marker is captured through end-of-input rather than
// dropped, so malformed content is still surfaced.
func ConflictHunks(blob []byte, markerSize int) ([]string, int) {
	if markerSize < 1 {
		markerSize = DefaultMarkerSize
	}
	start := strings.Repeat("<", markerSize)
	end := strings.Repeat(">", markerSize)
	lines := splitLinesKeepNL(blob)
	var hunks []string
	var cur strings.Builder
	inHunk := false
	for _, ln := range lines {
		switch {
		case !inHunk && strings.HasPrefix(ln, start):
			inHunk = true
			cur.Reset()
			cur.WriteString(ln)
		case inHunk:
			cur.WriteString(ln)
			if strings.HasPrefix(ln, end) {
				hunks = append(hunks, cur.String())
				inHunk = false
			}
		}
	}
	if inHunk { // unterminated opening marker
		hunks = append(hunks, cur.String())
	}
	return hunks, len(hunks)
}

// BoundedSample renders the first hunk for a summary verdict, capped to maxLines
// lines. A hunk within the cap passes through verbatim (truncated=false). A
// larger one keeps the head and the closing marker with a trimmed-count notice
// between them, so both conflict markers always survive. The count of remaining
// hunks is conveyed separately by Conflict.Hunks.
func BoundedSample(hunks []string, maxLines int) (sample string, truncated bool) {
	if len(hunks) == 0 {
		return "", false
	}
	// Summary mode: there IS more to fetch, so point the reader at --path.
	return boundLines(hunks[0], maxLines, "use --path for full detail")
}

// BoundedSampleAll renders every hunk (for --path drill-down) concatenated in
// order, capped to maxLines. Empty hunks yield an empty sample.
func BoundedSampleAll(hunks []string, maxLines int) (sample string, truncated bool) {
	if len(hunks) == 0 {
		return "", false
	}
	// Drill-down mode: the caller is already in --path, so re-running it yields
	// the same cap. Point at the file, not back at --path.
	return boundLines(strings.Join(hunks, ""), maxLines, "inspect the file directly")
}

// boundLines caps a single block to maxLines, preserving the first and last
// lines (the conflict markers) around a "… N lines trimmed …" notice. hint is
// the mode-appropriate pointer for retrieving what was trimmed.
func boundLines(block string, maxLines int, hint string) (string, bool) {
	lines := splitLinesKeepNL([]byte(block))
	if maxLines < 1 || len(lines) <= maxLines {
		return block, false
	}
	head := maxLines - 2
	if head < 1 {
		head = 1
	}
	if head >= len(lines) {
		return block, false
	}
	trimmed := len(lines) - head - 1
	var b strings.Builder
	for _, ln := range lines[:head] {
		b.WriteString(ln)
	}
	fmt.Fprintf(&b, "……… %d lines trimmed by mergeprobe (%s) ………\n", trimmed, hint)
	b.WriteString(lines[len(lines)-1]) // the closing marker line
	return b.String(), true
}

// splitLinesKeepNL splits into lines while keeping each line's trailing newline,
// so rejoining is byte-exact and no content is silently lost.
func splitLinesKeepNL(b []byte) []string {
	s := string(b)
	var out []string
	for len(s) > 0 {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			out = append(out, s)
			break
		}
		out = append(out, s[:i+1])
		s = s[i+1:]
	}
	return out
}
