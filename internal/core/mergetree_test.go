package core

import (
	"reflect"
	"testing"
)

// z joins fields the way `git merge-tree --write-tree -z` does: every field is
// terminated by a NUL, including the last. Building fixtures this way documents
// the exact wire format the parser must accept (verified against git 2.53).
func z(fields ...string) []byte {
	var b []byte
	for _, f := range fields {
		b = append(b, f...)
		b = append(b, 0)
	}
	return b
}

// A clean merge emits only "<tree>\0" — no separator, no entries, no messages.
func TestParseMergeTreeZ_Clean(t *testing.T) {
	mt, err := ParseMergeTreeZ(z("b7e11ab0a723a741f2a31ab2502d1b5c9cf70261"))
	if err != nil {
		t.Fatalf("clean parse errored: %v", err)
	}
	if mt.Tree != "b7e11ab0a723a741f2a31ab2502d1b5c9cf70261" {
		t.Errorf("Tree = %q, want the sole OID field", mt.Tree)
	}
	if len(mt.Files) != 0 {
		t.Errorf("clean merge has %d conflict files, want 0: %+v", len(mt.Files), mt.Files)
	}
	if len(mt.Messages) != 0 {
		t.Errorf("clean merge has %d messages, want 0: %+v", len(mt.Messages), mt.Messages)
	}
}

// The load-bearing case: a real 3-way conflict with all three resolution shapes
// present (content / add-add / modify-delete), parsed from the exact -z bytes
// git emits.
func TestParseMergeTreeZ_Conflict(t *testing.T) {
	data := z(
		"57002ca4d96048b835e3390a3de7bff18967842b",
		// conflicted-file-info: "<mode> <oid> <stage>\t<path>", git-sorted by path then stage
		"100644 6ba63417d03301a3e6e6d92e63bfce6c97fc6691 2\taddonly.txt",
		"100644 6cab31033617f70b8458bc33266d8de19a7e57b6 3\taddonly.txt",
		"100644 c1189167281fdb49483172c24bf207a52c2032ef 1\td.txt",
		"100644 879d5e394648da8e908a1e1dfcb063fe22ed369d 3\td.txt",
		"100644 83db48f84ec878fbfb30b46d16630e944e34f205 1\tf.txt",
		"100644 c25966fe378b98573acd1d526698f8f4e777cbff 2\tf.txt",
		"100644 f4720a29912a0d4537c0646bd0f103354fda3489 3\tf.txt",
		"", // section separator (the empty field between stages and messages)
		// informational messages: <count> <path>...<count> <type> <message>
		"1", "addonly.txt", "Auto-merging", "Auto-merging addonly.txt\n",
		"1", "addonly.txt", "CONFLICT (contents)", "CONFLICT (add/add): Merge conflict in addonly.txt\n",
		"1", "d.txt", "CONFLICT (modify/delete)", "CONFLICT (modify/delete): d.txt deleted in ours and modified in theirs.\n",
		"1", "f.txt", "Auto-merging", "Auto-merging f.txt\n",
		"1", "f.txt", "CONFLICT (contents)", "CONFLICT (content): Merge conflict in f.txt\n",
	)

	mt, err := ParseMergeTreeZ(data)
	if err != nil {
		t.Fatalf("conflict parse errored: %v", err)
	}
	if mt.Tree != "57002ca4d96048b835e3390a3de7bff18967842b" {
		t.Errorf("Tree = %q", mt.Tree)
	}

	wantFiles := []ConflictFile{
		{Path: "addonly.txt", Stages: map[int]Blob{
			2: {Mode: "100644", OID: "6ba63417d03301a3e6e6d92e63bfce6c97fc6691"},
			3: {Mode: "100644", OID: "6cab31033617f70b8458bc33266d8de19a7e57b6"},
		}},
		{Path: "d.txt", Stages: map[int]Blob{
			1: {Mode: "100644", OID: "c1189167281fdb49483172c24bf207a52c2032ef"},
			3: {Mode: "100644", OID: "879d5e394648da8e908a1e1dfcb063fe22ed369d"},
		}},
		{Path: "f.txt", Stages: map[int]Blob{
			1: {Mode: "100644", OID: "83db48f84ec878fbfb30b46d16630e944e34f205"},
			2: {Mode: "100644", OID: "c25966fe378b98573acd1d526698f8f4e777cbff"},
			3: {Mode: "100644", OID: "f4720a29912a0d4537c0646bd0f103354fda3489"},
		}},
	}
	if !reflect.DeepEqual(mt.Files, wantFiles) {
		t.Errorf("Files mismatch:\n got %+v\nwant %+v", mt.Files, wantFiles)
	}

	if len(mt.Messages) != 5 {
		t.Fatalf("got %d messages, want 5: %+v", len(mt.Messages), mt.Messages)
	}
	got := mt.Messages[1]
	want := InfoMessage{Paths: []string{"addonly.txt"}, Type: "CONFLICT (contents)", Message: "CONFLICT (add/add): Merge conflict in addonly.txt\n"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Messages[1] = %+v, want %+v", got, want)
	}
}

// A path with an embedded space/quote survives because -z disables path quoting;
// the parser must split on TAB, not whitespace.
func TestParseMergeTreeZ_PathWithSpace(t *testing.T) {
	data := z(
		"aaaa",
		"100644 1111111111111111111111111111111111111111 1\tmy dir/a b.txt",
		"100644 2222222222222222222222222222222222222222 2\tmy dir/a b.txt",
		"100644 3333333333333333333333333333333333333333 3\tmy dir/a b.txt",
		"",
		"1", "my dir/a b.txt", "CONFLICT (contents)", "CONFLICT (content): Merge conflict in my dir/a b.txt\n",
	)
	mt, err := ParseMergeTreeZ(data)
	if err != nil {
		t.Fatalf("parse errored: %v", err)
	}
	if len(mt.Files) != 1 || mt.Files[0].Path != "my dir/a b.txt" {
		t.Fatalf("path with space not preserved: %+v", mt.Files)
	}
	if mt.Files[0].Stages[2].OID != "2222222222222222222222222222222222222222" {
		t.Errorf("stage-2 OID for spaced path wrong: %+v", mt.Files[0])
	}
}

// A multi-path informational message (e.g. rename/rename lists 2+ paths) must
// consume exactly <count> path fields before the type/message.
func TestParseMergeTreeZ_MultiPathMessage(t *testing.T) {
	data := z(
		"aaaa",
		"100644 1111111111111111111111111111111111111111 1\told.txt",
		"100644 2222222222222222222222222222222222222222 2\tnew-a.txt",
		"100644 3333333333333333333333333333333333333333 3\tnew-b.txt",
		"",
		"3", "old.txt", "new-a.txt", "new-b.txt", "CONFLICT (rename/rename)", "CONFLICT (rename/rename): old.txt renamed to new-a.txt and new-b.txt.\n",
	)
	mt, err := ParseMergeTreeZ(data)
	if err != nil {
		t.Fatalf("parse errored: %v", err)
	}
	if len(mt.Messages) != 1 {
		t.Fatalf("got %d messages, want 1: %+v", len(mt.Messages), mt.Messages)
	}
	if want := []string{"old.txt", "new-a.txt", "new-b.txt"}; !reflect.DeepEqual(mt.Messages[0].Paths, want) {
		t.Errorf("Paths = %v, want %v", mt.Messages[0].Paths, want)
	}
	if mt.Messages[0].Type != "CONFLICT (rename/rename)" {
		t.Errorf("Type = %q", mt.Messages[0].Type)
	}
}

func TestParseMergeTreeZ_Malformed(t *testing.T) {
	cases := []struct {
		name string
		data []byte
	}{
		{"empty", nil},
		{"only-nuls", []byte{0, 0}},
		{"bad-entry-no-tab", z("aaaa", "100644 1111 1 notab.txt", "")},
		{"bad-entry-short", z("aaaa", "100644 1\tf.txt", "")},
		{"bad-stage", z("aaaa", "100644 1111111111111111111111111111111111111111 9\tf.txt", "")},
		{"empty-path", z("aaaa", "100644 1111111111111111111111111111111111111111 1\t", "")},
		{"truncated-message-count", z("aaaa", "100644 1111111111111111111111111111111111111111 1\tf.txt", "", "2", "only-one-path")},
		// A message path-count over maxInfoPaths (or wide enough to overflow int)
		// must be rejected, not used as a slice bound — the maxInfoPaths guard.
		{"giant-message-count", z("aaaa", "100644 1111111111111111111111111111111111111111 1\tf.txt", "", "9999999", "p")},
		{"overflow-message-count", z("aaaa", "100644 1111111111111111111111111111111111111111 1\tf.txt", "", "99999999999999999999", "p")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := ParseMergeTreeZ(c.data); err == nil {
				t.Errorf("ParseMergeTreeZ(%q) = nil error, want malformed error", c.data)
			}
		})
	}
}
