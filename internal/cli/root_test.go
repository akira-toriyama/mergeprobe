package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/akira-toriyama/mergeprobe/internal/core"
)

// The scaffold guarantees exactly this much: --help succeeds and renders the
// planned grammar. Real commands arrive with the implementation.
func TestRootHelp(t *testing.T) {
	root := newRootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"--help"})
	if err := root.Execute(); err != nil {
		t.Fatalf("--help failed: %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, "mergeprobe") {
		t.Errorf("help output does not mention the binary name: %q", got)
	}
	if !strings.Contains(got, "--onto") {
		t.Errorf("help output does not render the planned grammar sketch: %q", got)
	}
}

// The JSON funnel must not HTML-escape <, >, & — error messages (and the future
// payload) echo diff/path/code content, and stderr envelopes must byte-match the
// on-disk form so `jq` over them stays honest.
func TestRenderErrorNoHTMLEscape(t *testing.T) {
	old := errOut
	var buf bytes.Buffer
	errOut = &buf
	defer func() { errOut = old }()

	renderError(&core.Error{Code: core.CodeValidation, Msg: "line <html> & path a>b"})

	got := buf.String()
	// EscapeHTML(false) means the escaped \uXXXX forms must be absent and the raw
	// characters present verbatim.
	for _, escaped := range []string{"\\u003c", "\\u003e", "\\u0026"} {
		if strings.Contains(got, escaped) {
			t.Errorf("error envelope contains an HTML-escaped sequence (%s): %s", escaped, got)
		}
	}
	if !strings.Contains(got, "line <html> & path a>b") {
		t.Errorf("message not emitted verbatim: %s", got)
	}
}

// A pre-v0 invocation must fail loudly (internal, not usage) — never a silent
// help-and-exit-0 an agent could mistake for success.
func TestPreV0InvocationFailsLoudly(t *testing.T) {
	root := newRootCmd()
	var buf bytes.Buffer
	root.SetOut(&buf)
	root.SetErr(&buf)
	root.SetArgs([]string{"123"})
	err := root.Execute()
	if err == nil {
		t.Fatal("pre-v0 invocation succeeded; want a not-implemented error")
	}
	if got, want := core.ExitCode(err), int(core.CodeInternal); got != want {
		t.Errorf("exit code = %d, want %d (internal)", got, want)
	}
}
