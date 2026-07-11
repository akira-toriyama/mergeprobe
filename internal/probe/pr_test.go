package probe

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/akira-toriyama/mergeprobe/internal/core"
)

func TestParsePRRef(t *testing.T) {
	tests := []struct {
		in    string
		want  PR
		wantK bool
	}{
		// origin forms: a bare number, optionally #-prefixed, is PR N in origin.
		{"123", PR{Num: 123}, true},
		{"#123", PR{Num: 123}, true},
		{"1", PR{Num: 1}, true},
		// owner/repo#N targets a specific GitHub repo.
		{"akira-toriyama/mergeprobe#123", PR{Owner: "akira-toriyama", Repo: "mergeprobe", Num: 123}, true},
		{"cli/cli#872", PR{Owner: "cli", Repo: "cli", Num: 872}, true},
		// not PR references.
		{"feature-x", PR{}, false},
		{"123abc", PR{}, false},
		{"main", PR{}, false},
		{"", PR{}, false},
		{"#", PR{}, false},
		{"0", PR{}, false},            // PR numbers start at 1
		{"#0", PR{}, false},           // ditto
		{"owner/repo", PR{}, false},   // no #N
		{"owner/repo#", PR{}, false},  // empty number
		{"owner/repo#0", PR{}, false}, // zero
		{"owner#12", PR{}, false},     // missing repo half
		{"a/b/c#12", PR{}, false},     // too many slashes
		{"-x#12", PR{}, false},        // dash-shaped owner
	}
	for _, tc := range tests {
		got, ok := ParsePRRef(tc.in)
		if ok != tc.wantK {
			t.Errorf("ParsePRRef(%q) ok = %v, want %v", tc.in, ok, tc.wantK)
			continue
		}
		if ok && got != tc.want {
			t.Errorf("ParsePRRef(%q) = %+v, want %+v", tc.in, got, tc.want)
		}
	}
}

// ctx is a tiny helper so the resolve tests read cleanly.
func ctx() context.Context { return context.Background() }

// bare PR + gh answers the base: fetch both head and base from origin, pin both
// to OIDs, label them for the report, no diagnostic notes.
func TestResolvePR_BareNumber_ForgeBase(t *testing.T) {
	g := fakeGit{
		fetch: func(source, ref string) (string, error) {
			switch {
			case source == "origin" && ref == "refs/pull/123/head":
				return "headoid", nil
			case source == "origin" && ref == "main":
				return "baseoid", nil
			}
			t.Fatalf("unexpected fetch(%q,%q)", source, ref)
			return "", nil
		},
	}
	f := fakeForge{baseRef: func(_, _ string, _ int) (string, bool, error) { return "main", true, nil }}

	opts, notes, err := ResolvePR(ctx(), g, f, PR{Num: 123}, "")
	if err != nil {
		t.Fatalf("ResolvePR: %v", err)
	}
	if opts.Topic != "headoid" || opts.Base != "baseoid" {
		t.Errorf("resolved refs = (%q,%q), want (headoid,baseoid)", opts.Topic, opts.Base)
	}
	if opts.TopicLabel != "#123" || opts.BaseLabel != "main" {
		t.Errorf("labels = (%q,%q), want (#123,main)", opts.TopicLabel, opts.BaseLabel)
	}
	if len(notes) != 0 {
		t.Errorf("unexpected notes: %v", notes)
	}
}

// bare PR, gh unavailable: fall back to origin/HEAD and emit exactly one note so
// the caller knows the base was assumed. The base is NOT fetched (it is a local
// remote-tracking ref).
func TestResolvePR_BareNumber_FallbackDefaultBase(t *testing.T) {
	g := fakeGit{
		fetch: func(source, ref string) (string, error) {
			if ref == "refs/pull/7/head" {
				return "headoid", nil
			}
			t.Fatalf("fallback must not fetch a base: fetch(%q,%q)", source, ref)
			return "", nil
		},
		defBase: func() (string, error) { return "origin/main", nil },
	}
	opts, notes, err := ResolvePR(ctx(), g, fakeForge{}, PR{Num: 7}, "")
	if err != nil {
		t.Fatalf("ResolvePR: %v", err)
	}
	if opts.Base != "origin/main" || opts.BaseLabel != "origin/main" {
		t.Errorf("base = (%q,%q), want origin/main", opts.Base, opts.BaseLabel)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "origin/main") {
		t.Errorf("want one note mentioning the assumed base, got %v", notes)
	}
}

// gh present but erroring (e.g. unauthenticated) is non-fatal: fall back and
// surface the reason in the note.
func TestResolvePR_ForgeError_FallsBackWithReason(t *testing.T) {
	g := fakeGit{
		fetch:   func(source, ref string) (string, error) { return "headoid", nil },
		defBase: func() (string, error) { return "origin/trunk", nil },
	}
	f := fakeForge{baseRef: func(_, _ string, _ int) (string, bool, error) {
		return "", false, errors.New("gh: not authenticated")
	}}
	_, notes, err := ResolvePR(ctx(), g, f, PR{Num: 4}, "")
	if err != nil {
		t.Fatalf("ResolvePR: %v", err)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "not authenticated") {
		t.Errorf("note should explain the gh failure, got %v", notes)
	}
}

// an explicit --onto wins: the forge is never consulted and only the head is
// fetched.
func TestResolvePR_OntoOverride(t *testing.T) {
	g := fakeGit{
		fetch: func(source, ref string) (string, error) {
			if ref != "refs/pull/5/head" {
				t.Fatalf("onto override fetched a base: %q", ref)
			}
			return "headoid", nil
		},
	}
	forgeCalled := false
	f := fakeForge{baseRef: func(_, _ string, _ int) (string, bool, error) { forgeCalled = true; return "x", true, nil }}

	opts, notes, err := ResolvePR(ctx(), g, f, PR{Num: 5}, "origin/release")
	if err != nil {
		t.Fatalf("ResolvePR: %v", err)
	}
	if forgeCalled {
		t.Error("forge consulted despite explicit --onto")
	}
	if opts.Base != "origin/release" || opts.BaseLabel != "origin/release" {
		t.Errorf("base = (%q,%q), want origin/release", opts.Base, opts.BaseLabel)
	}
	if len(notes) != 0 {
		t.Errorf("unexpected notes: %v", notes)
	}
}

// owner/repo#N matching a configured remote fetches from that remote's name.
func TestResolvePR_OwnerRepo_MatchesRemote(t *testing.T) {
	g := fakeGit{
		remotes: func() (map[string]string, error) {
			return map[string]string{"origin": "git@github.com:cli/cli.git"}, nil
		},
		fetch: func(source, ref string) (string, error) {
			if source != "origin" {
				t.Errorf("source = %q, want the matched remote origin", source)
			}
			switch ref {
			case "refs/pull/872/head":
				return "headoid", nil
			case "trunk":
				return "baseoid", nil
			}
			t.Fatalf("unexpected fetch ref %q", ref)
			return "", nil
		},
	}
	f := fakeForge{baseRef: func(_, _ string, _ int) (string, bool, error) { return "trunk", true, nil }}

	opts, _, err := ResolvePR(ctx(), g, f, PR{Owner: "cli", Repo: "cli", Num: 872}, "")
	if err != nil {
		t.Fatalf("ResolvePR: %v", err)
	}
	if opts.TopicLabel != "cli/cli#872" {
		t.Errorf("topic label = %q, want cli/cli#872", opts.TopicLabel)
	}
}

// owner/repo#N with no matching remote fetches directly from the GitHub URL.
func TestResolvePR_OwnerRepo_URLSource(t *testing.T) {
	g := fakeGit{
		remotes: func() (map[string]string, error) {
			return map[string]string{"origin": "git@github.com:akira-toriyama/mergeprobe.git"}, nil
		},
		fetch: func(source, ref string) (string, error) {
			if source != "https://github.com/cli/cli.git" {
				t.Errorf("source = %q, want the github URL", source)
			}
			if ref == "refs/pull/1/head" {
				return "headoid", nil
			}
			return "baseoid", nil
		},
	}
	f := fakeForge{baseRef: func(_, _ string, _ int) (string, bool, error) { return "main", true, nil }}

	opts, _, err := ResolvePR(ctx(), g, f, PR{Owner: "cli", Repo: "cli", Num: 1}, "")
	if err != nil {
		t.Fatalf("ResolvePR: %v", err)
	}
	if opts.Base != "baseoid" {
		t.Errorf("base = %q, want baseoid", opts.Base)
	}
}

// owner/repo#N to a non-origin repo with gh unavailable cannot guess a base:
// error clearly and point at --onto rather than silently using origin/HEAD.
func TestResolvePR_OwnerRepo_NoForge_NoDefault(t *testing.T) {
	g := fakeGit{
		remotes: func() (map[string]string, error) {
			return map[string]string{"origin": "git@github.com:me/x.git"}, nil
		},
		fetch: func(source, ref string) (string, error) { return "headoid", nil },
	}
	_, _, err := ResolvePR(ctx(), g, fakeForge{}, PR{Owner: "cli", Repo: "cli", Num: 1}, "")
	if ce := core.AsError(err); ce == nil || ce.Code != core.CodeValidation {
		t.Fatalf("want CodeValidation, got %v", err)
	}
	if !strings.Contains(err.Error(), "--onto") {
		t.Errorf("error should point at --onto, got %v", err)
	}
}

// a missing PR head is a not-found error labelled with the PR.
func TestResolvePR_HeadNotFound(t *testing.T) {
	g := fakeGit{
		fetch: func(source, ref string) (string, error) {
			return "", core.NotFoundf("fetch-no-ref", "remote ref not found: fatal: couldn't find remote ref")
		},
	}
	_, _, err := ResolvePR(ctx(), g, fakeForge{}, PR{Num: 999}, "")
	if ce := core.AsError(err); ce == nil || ce.Code != core.CodeNotFound {
		t.Fatalf("want CodeNotFound, got %v", err)
	}
	if !strings.Contains(err.Error(), "#999") {
		t.Errorf("not-found should name the PR, got %v", err)
	}
}
