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
	"github.com/akira-toriyama/mergeprobe/internal/version"
)

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

	root := newRootCmd()
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
	root := &cobra.Command{
		Use:   "mergeprobe [<pr-number> | <branch>]",
		Short: "Probe merge/rebase conflicts without touching the worktree (git merge-tree → bounded JSON)",
		Long: "mergeprobe answers \"what does this PR/branch conflict with, where, and how\n" +
			"badly?\" in one call — without checking anything out. It wraps git 2.38+'s\n" +
			"merge-tree --write-tree (in-memory merge, worktree untouched) and renders the\n" +
			"arcane plumbing output as bounded JSON an AI coding agent consumes in one turn.\n\n" +
			"Planned grammar (design: docs/design.md — not implemented yet):\n" +
			"  mergeprobe 123                            # PR by number, inside a clone\n" +
			"  mergeprobe feature-x --onto origin/main   # any ref pair\n" +
			"  mergeprobe 123 --rebase                   # per-commit rebase simulation\n" +
			"  mergeprobe 123 --path app/Kconfig         # drill into one file's conflict",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version.Resolve().String(),
		// Pre-v0: a real invocation fails loudly instead of silently printing
		// help — an agent must not mistake a no-op for success. Bare
		// `mergeprobe` still shows help.
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}
			return core.Internalf("not-implemented",
				"mergeprobe is a pre-v0 scaffold — nothing is implemented yet (see docs/design.md)")
		},
	}
	root.SetOut(out)
	root.SetErr(errOut)
	return root
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
	b, err := json.Marshal(map[string]any{"error": env})
	if err != nil {
		fmt.Fprintln(errOut, e.Msg)
		return
	}
	fmt.Fprintln(errOut, string(b))
}
