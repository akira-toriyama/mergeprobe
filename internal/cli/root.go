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
	"github.com/akira-toriyama/mergeprobe/internal/forge"
	"github.com/akira-toriyama/mergeprobe/internal/git"
	"github.com/akira-toriyama/mergeprobe/internal/probe"
	"github.com/akira-toriyama/mergeprobe/internal/version"
)

// The real adapters must satisfy the probe ports; fail the build if they drift.
var (
	_ probe.Git   = (*git.Repo)(nil)
	_ probe.Forge = (*forge.GH)(nil)
)

// newRepo builds the git adapter the root command probes with. Production roots
// it at the process working directory (git discovers the repo upward); tests
// override it to root a real adapter at a temporary repository, so the full
// stack is exercised against real git without a chdir.
var newRepo = func() probe.Git { return git.New("") }

// newForge builds the optional GitHub-metadata adapter used to resolve a PR's
// base branch. Tests override it to stub gh; production shells out to it when
// available and degrades gracefully when not.
var newForge = func() probe.Forge { return forge.New() }

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
	var rebase bool
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
			"  mergeprobe 123                            # origin PR #123 (fetches pull/123/head)\n" +
			"  mergeprobe cli/cli#872                    # PR #872 in another repo\n" +
			"  mergeprobe feature-x --path app/Kconfig   # drill into one conflicted file\n" +
			"  mergeprobe feature-x --onto main --rebase # simulate a rebase, not a merge\n\n" +
			"For a PR, the base branch comes from gh when available, else origin/HEAD with a\n" +
			"note (--onto always overrides). --rebase replays topic's commits onto base and\n" +
			"reports the first one that conflicts. A successful probe exits 0 whether or not\n" +
			"it merges cleanly; read .mergeable (or .rebaseable) in the payload.",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version.Resolve().String(),
		Args:          cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			topic := ""
			if len(args) == 1 {
				topic = args[0]
			}
			if rebase && path != "" {
				return core.Validationf("rebase-path",
					"--path is not supported with --rebase (drill-down is for the static probe); drop one")
			}
			g := newRepo()
			opts, err := resolveOptions(cmd, g, topic, onto, path)
			if err != nil {
				return err
			}
			var result any
			if rebase {
				var notes []string
				result, notes, err = probe.RunRebase(cmd.Context(), g, opts)
				for _, n := range notes {
					fmt.Fprintln(cmd.ErrOrStderr(), "note: "+n)
				}
			} else {
				result, err = probe.Run(cmd.Context(), g, opts)
			}
			if err != nil {
				return err
			}
			// Classify a stdout write failure as internal/IO (a bare error here
			// would otherwise fall through to the cobra->validation mapping in
			// run()); this keeps RunE's contract of always returning *core.Error.
			if err := writeJSON(cmd.OutOrStdout(), result); err != nil {
				return core.Internalf("output-write", "writing result: %v", err)
			}
			return nil
		},
	}
	root.Flags().StringVar(&onto, "onto", "", "ref the topic lands on (default: origin/HEAD)")
	root.Flags().StringVar(&path, "path", "", "drill into one conflicted file and show its full sample")
	root.Flags().BoolVar(&rebase, "rebase", false, "simulate a rebase (replay topic's commits onto base) and report the first conflicting commit")
	root.SetOut(out)
	root.SetErr(errOut)
	return root
}

// resolveOptions turns the CLI's topic argument into a probe request. A topic
// that reads as a PR reference (123 / owner/repo#123) is resolved through the
// git+forge ports — fetching the PR head, deciding the base — and any assumed-
// base notes are written to stderr. Any other topic is a plain ref pair.
func resolveOptions(cmd *cobra.Command, g probe.Git, topic, onto, path string) (probe.Options, error) {
	if pr, ok := probe.ParsePRRef(topic); ok {
		opts, notes, err := probe.ResolvePR(cmd.Context(), g, newForge(), pr, onto)
		if err != nil {
			return probe.Options{}, err
		}
		for _, n := range notes {
			fmt.Fprintln(cmd.ErrOrStderr(), "note: "+n)
		}
		opts.Path = path
		return opts, nil
	}
	return probe.Options{Topic: topic, Base: onto, Path: path}, nil
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
