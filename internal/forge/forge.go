// Package forge is the optional GitHub-metadata adapter: it shells out to the
// gh CLI for the one fact git alone cannot supply — a pull request's base
// branch. It satisfies probe.Forge structurally (the interface is asserted in
// cli, so this package need not import probe) and takes primitives, keeping it
// as dependency-light as the git adapter. Its defining trait is graceful
// absence: when gh is not installed it reports "unavailable" without error, so
// PR resolution degrades to a git-only default rather than failing.
package forge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// GH runs the gh CLI (bin, "gh" by default).
type GH struct {
	bin string
}

// New returns a GH adapter that invokes the gh binary on PATH.
func New() *GH { return &GH{bin: "gh"} }

// PRBaseRef asks gh for PR num's base branch. owner/repo scope it to a specific
// repository (empty = the ambient repo gh infers from the cwd's remotes). The
// return contract encodes graceful degradation: ok=true with the base on
// success; ok=false, err=nil when gh could not start (not installed) — an
// expected absence, not an error; ok=false with a reason when gh ran but failed
// (unauthenticated, no such PR, network) or returned nothing usable.
func (g *GH) PRBaseRef(ctx context.Context, owner, repo string, num int) (string, bool, error) {
	args := []string{"pr", "view", strconv.Itoa(num), "--json", "baseRefName", "--jq", ".baseRefName"}
	if owner != "" {
		args = append(args, "--repo", owner+"/"+repo)
	}
	// #nosec G204 -- args are a fixed gh subcommand plus an integer PR number
	// and a owner/repo validated by probe.ParsePRRef (single slash, no leading
	// dash). No shell is involved: exec passes an explicit argv.
	cmd := exec.CommandContext(ctx, g.bin, args...)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			// gh ran but exited non-zero: auth, missing PR, network. Non-fatal —
			// the caller falls back and surfaces this reason in a note.
			return "", false, fmt.Errorf("gh pr view failed: %s", reason(errb.String(), ee))
		}
		// gh could not start (not installed): a clean unavailable, no reason.
		return "", false, nil
	}
	base := strings.TrimSpace(out.String())
	if base == "" {
		return "", false, errors.New("gh returned an empty base branch")
	}
	return base, true, nil
}

// reason distills gh's failure to one line for a diagnostic note: the last
// non-empty stderr line, else the exit error itself.
func reason(stderr string, ee *exec.ExitError) string {
	for _, ln := range strings.Split(strings.TrimRight(stderr, "\n"), "\n") {
		if t := strings.TrimSpace(ln); t != "" {
			return t
		}
	}
	return ee.String()
}
