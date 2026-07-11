// Package cli is the cobra adapter — mergeprobe's command-line presentation
// layer. It parses flags, will call the pure core for every operation, and
// renders the result. It holds no domain logic. stdout carries pipeable
// payload only; diagnostics and error envelopes go to stderr.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/akira-toriyama/mergeprobe/internal/core"
	"github.com/akira-toriyama/mergeprobe/internal/git"
	"github.com/akira-toriyama/mergeprobe/internal/probe"
	"github.com/akira-toriyama/mergeprobe/internal/version"
)

// The real git adapter must satisfy the probe port; fail the build if it drifts.
var _ probe.Git = (*git.Repo)(nil)

// newRepo builds the git adapter the root command probes with. Production roots
// it at the process working directory (git discovers the repo upward); tests
// override it to root a real adapter at a temporary repository, so the full
// stack is exercised against real git without a chdir.
var newRepo = func() probe.Git { return git.New("") }

// out/errOut are the single funnel for process output: stdout = payload,
// stderr = diagnostics. No other file writes to os.Stdout/os.Stderr directly.
var (
	out    io.Writer = os.Stdout
	errOut io.Writer = os.Stderr
)

// Execute builds the root command, runs it, and maps the result to the
// exit-code contract: 0 ok / 1 not-found|empty / 2 bad-usage|validation /
// 3+ internal|IO. On a non-zero exit it prints {"error":{...}} to stderr. It
// is the only place that decides the process exit code; main is just
// os.Exit(cli.Execute()).
//
// Signals: the root context cancels on the first SIGINT/SIGTERM so in-flight
// git subprocesses can unwind gracefully (exec.CommandContext); the deferred
// stop restores the default disposition, so a second Ctrl-C hard-kills a
// wedged process.
func Execute() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		stop() // restore default disposition: a second Ctrl-C terminates hard
	}()
	return run(ctx, os.Args[1:])
}

// run builds the root command with the given args, executes it, and maps the
// result to the exit-code contract. It is the single seam end-to-end tests drive
// (with args + the git adapter overridden) so the cobra→validation mapping and
// error rendering are exercised for real.
func run(ctx context.Context, args []string) int {
	root := newRootCmd()
	root.SetArgs(args)
	err := root.ExecuteContext(ctx)
	if err == nil {
		return int(core.CodeOK)
	}
	// Core code always returns *core.Error; a bare error here can only be a
	// cobra flag/arg parse problem, which is a usage error by contract.
	ce := core.AsError(err)
	if ce == nil {
		ce = &core.Error{Code: core.CodeValidation, Msg: err.Error()}
	}
	renderError(ce)
	return int(ce.Code)
}

func newRootCmd() *cobra.Command {
	var onto, path string
	root := &cobra.Command{
		Use:   "mergeprobe [<branch>]",
		Short: "Probe merge conflicts without touching the worktree (git merge-tree → bounded JSON)",
		Long: "mergeprobe answers \"what does this branch conflict with, where, and how\n" +
			"badly?\" in one call — without checking anything out. It wraps git 2.38+'s\n" +
			"merge-tree --write-tree (in-memory merge, worktree untouched) and renders the\n" +
			"arcane plumbing output as bounded JSON an AI coding agent consumes in one turn.\n\n" +
			"Grammar:\n" +
			"  mergeprobe                                # HEAD onto origin/HEAD (does my branch land?)\n" +
			"  mergeprobe feature-x                      # feature-x onto origin/HEAD\n" +
			"  mergeprobe feature-x --onto origin/main   # any ref pair\n" +
			"  mergeprobe feature-x --path app/Kconfig   # drill into one conflicted file\n\n" +
			"A successful probe exits 0 whether or not it merges cleanly; read .mergeable in\n" +
			"the payload. PR-number resolution and --rebase are planned (docs/design.md).",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version.Resolve().String(),
		Args:          cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			topic := ""
			if len(args) == 1 {
				topic = args[0]
			}
			report, err := probe.Run(cmd.Context(), newRepo(), probe.Options{Topic: topic, Base: onto, Path: path})
			if err != nil {
				return prettifyRefError(err, topic)
			}
			// Classify a stdout write failure as internal/IO (a bare error here
			// would otherwise fall through to the cobra->validation mapping in
			// run()); this keeps RunE's contract of always returning *core.Error.
			if err := writeJSON(cmd.OutOrStdout(), report); err != nil {
				return core.Internalf("output-write", "writing result: %v", err)
			}
			return nil
		},
	}
	root.Flags().StringVar(&onto, "onto", "", "ref the topic lands on (default: origin/HEAD)")
	root.Flags().StringVar(&path, "path", "", "drill into one conflicted file and show its full sample")
	root.SetOut(out)
	root.SetErr(errOut)
	return root
}

// prettifyRefError turns the raw unknown-ref error for an all-digit topic into a
// pointer at the not-yet-implemented PR-number resolution, since an agent's
// first instinct is `mergeprobe 123`.
func prettifyRefError(err error, topic string) error {
	ce := core.AsError(err)
	if ce != nil && ce.ID == "unknown-ref" && topic != "" && isAllDigits(topic) {
		return core.Validationf("pr-number-unsupported",
			"%q looks like a PR number, but PR-number resolution is not implemented yet; "+
				"pass a branch/ref (e.g. mergeprobe feature-x --onto origin/main) — see docs/design.md", topic)
	}
	return err
}

func isAllDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// renderError prints the structured error envelope to stderr — never stdout,
// so piping the payload into jq stays clean.
func renderError(e *core.Error) {
	env := map[string]any{"code": int(e.Code), "message": e.Msg}
	if e.ID != "" {
		env["id"] = e.ID
	}
	if e.Details != nil {
		env["details"] = e.Details
	}
	if err := writeJSON(errOut, map[string]any{"error": env}); err != nil {
		fmt.Fprintln(errOut, e.Msg)
	}
}

// writeJSON is the single JSON funnel for every payload and envelope: HTML
// escaping is off so <, >, & survive verbatim (messages echo diff/path/code
// content) and the emitted bytes match the on-disk encoding. Encode appends a
// trailing newline.
func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}
