// mergeprobe answers "what does this PR/branch conflict with, where, and how
// badly?" in one call, without touching the worktree: it wraps git merge-tree
// --write-tree (in-memory merge) and renders the plumbing output as bounded
// JSON. main is the only untestable process boundary — everything below
// returns and is exercised by tests through cli.Execute.
package main

import (
	"os"

	"github.com/akira-toriyama/mergeprobe/internal/cli"
)

func main() { os.Exit(cli.Execute()) }
