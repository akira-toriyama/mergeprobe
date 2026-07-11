package core

import "testing"

func file(stages ...int) ConflictFile {
	cf := ConflictFile{Path: "p", Stages: map[int]Blob{}}
	for _, s := range stages {
		cf.Stages[s] = Blob{Mode: "100644", OID: "x"}
	}
	return cf
}

func TestClassify(t *testing.T) {
	cases := []struct {
		name string
		cf   ConflictFile
		want Class
	}{
		{"content both-modified", file(1, 2, 3), ClassBothModified},
		{"add-add (no base)", file(2, 3), ClassAddAdd},
		{"modify-delete, deleted in topic", file(1, 2), ClassModifyDelete},
		{"modify-delete, deleted in base", file(1, 3), ClassModifyDelete},
		{"delete-delete", file(1), ClassDeleteDelete},
		{"lone add on one side", file(2), ClassOther},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Classify(c.cf); got != c.want {
				t.Errorf("Classify(%v) = %q, want %q", stageKeys(c.cf), got, c.want)
			}
		})
	}
}

func stageKeys(cf ConflictFile) []int {
	var ks []int
	for k := range cf.Stages {
		ks = append(ks, k)
	}
	return ks
}

func TestIsBinary(t *testing.T) {
	if IsBinary([]byte("plain text\nwith lines\n")) {
		t.Error("plain text classified as binary")
	}
	if !IsBinary([]byte("has a \x00 nul")) {
		t.Error("content with NUL not classified as binary")
	}
	if IsBinary(nil) {
		t.Error("empty content classified as binary")
	}
	// A NUL beyond the sniff window (8000 bytes) is not scanned — matches git.
	big := append(make([]byte, 9000), 0)
	for i := range big[:9000] {
		big[i] = 'a'
	}
	if IsBinary(big) {
		t.Error("NUL past the 8000-byte sniff window should not count as binary")
	}
}
