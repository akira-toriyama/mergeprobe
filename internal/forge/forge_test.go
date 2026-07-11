package forge

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeGH writes an executable stand-in for the gh binary running body, and
// returns its path. Driving a real child process (rather than mocking exec)
// mirrors the house preference and exercises the adapter's actual argv/exit
// handling.
func fakeGH(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "gh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatalf("write fake gh: %v", err)
	}
	return p
}

func TestPRBaseRef_Success(t *testing.T) {
	g := &GH{bin: fakeGH(t, "echo main\n")}
	base, ok, err := g.PRBaseRef(context.Background(), "", "", 123)
	if err != nil || !ok {
		t.Fatalf("PRBaseRef = (%q,%v,%v), want (main,true,nil)", base, ok, err)
	}
	if base != "main" {
		t.Errorf("base = %q, want main", base)
	}
}

// An owner/repo PR passes --repo owner/repo to gh; a bare PR omits it.
func TestPRBaseRef_PassesRepoFlag(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args")
	g := &GH{bin: fakeGH(t, "echo \"$@\" > "+argsFile+"\necho trunk\n")}

	if _, _, err := g.PRBaseRef(context.Background(), "cli", "cli", 872); err != nil {
		t.Fatalf("PRBaseRef: %v", err)
	}
	got, _ := os.ReadFile(argsFile)
	if !strings.Contains(string(got), "--repo cli/cli") {
		t.Errorf("gh args = %q, want --repo cli/cli", got)
	}
	if !strings.Contains(string(got), "872") {
		t.Errorf("gh args = %q, want the PR number 872", got)
	}

	// A bare PR must not send --repo.
	argsFile2 := filepath.Join(t.TempDir(), "args2")
	g2 := &GH{bin: fakeGH(t, "echo \"$@\" > "+argsFile2+"\necho main\n")}
	if _, _, err := g2.PRBaseRef(context.Background(), "", "", 5); err != nil {
		t.Fatalf("PRBaseRef: %v", err)
	}
	got2, _ := os.ReadFile(argsFile2)
	if strings.Contains(string(got2), "--repo") {
		t.Errorf("bare PR should not pass --repo, got %q", got2)
	}
}

// gh present but failing (auth, no such PR) is non-fatal: ok=false with a reason
// so the caller can fall back and explain why.
func TestPRBaseRef_GHError(t *testing.T) {
	g := &GH{bin: fakeGH(t, "echo 'gh: not authenticated' >&2\nexit 1\n")}
	_, ok, err := g.PRBaseRef(context.Background(), "", "", 1)
	if ok {
		t.Error("a failing gh should report ok=false")
	}
	if err == nil || !strings.Contains(err.Error(), "not authenticated") {
		t.Errorf("want a reason mentioning the gh failure, got %v", err)
	}
}

// gh not installed is the graceful-degradation case: ok=false, err=nil (no
// reason — its absence is expected, not an error to shout about).
func TestPRBaseRef_GHMissing(t *testing.T) {
	g := &GH{bin: filepath.Join(t.TempDir(), "does-not-exist")}
	_, ok, err := g.PRBaseRef(context.Background(), "", "", 1)
	if ok || err != nil {
		t.Errorf("missing gh = (ok=%v,err=%v), want (false,nil)", ok, err)
	}
}

// gh returning empty output (e.g. a filtered field that didn't match) is not a
// usable base.
func TestPRBaseRef_EmptyOutput(t *testing.T) {
	g := &GH{bin: fakeGH(t, "true\n")}
	_, ok, err := g.PRBaseRef(context.Background(), "", "", 1)
	if ok {
		t.Error("empty gh output should report ok=false")
	}
	if err == nil {
		t.Error("empty gh output should carry a reason")
	}
}
