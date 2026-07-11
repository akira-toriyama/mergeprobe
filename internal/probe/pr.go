package probe

import (
	"context"
	"fmt"
	"strings"

	"github.com/akira-toriyama/mergeprobe/internal/core"
)

// PR identifies a GitHub pull request to probe. Owner/Repo are empty for the
// bare-number form (mergeprobe 123 / #123), meaning "PR N in the origin repo";
// they are set for the owner/repo#N form, which targets a specific repository.
type PR struct {
	Owner string
	Repo  string
	Num   int
}

// ParsePRRef recognizes the two PR-reference spellings an agent reaches for:
// a bare (optionally #-prefixed) number for an origin PR, and owner/repo#N for
// a specific repository. It returns ok=false for anything that is not one of
// those, so the caller falls through to the ordinary ref-pair path. PR numbers
// start at 1, so a 0 is not a PR reference.
func ParsePRRef(s string) (PR, bool) {
	if strings.Count(s, "#") > 1 {
		return PR{}, false
	}
	left, numStr, hasHash := strings.Cut(s, "#")
	if !hasHash {
		// Bare number: PR N in origin. Anything non-numeric is an ordinary ref.
		numStr = left
		left = ""
	}
	num, ok := parsePRNum(numStr)
	if !ok {
		return PR{}, false
	}
	if left == "" {
		return PR{Num: num}, true
	}
	owner, repo, ok := splitOwnerRepo(left)
	if !ok {
		return PR{}, false
	}
	return PR{Owner: owner, Repo: repo, Num: num}, true
}

// Label renders the PR the way a human named it: "#123" for an origin PR,
// "owner/repo#123" for a specific repository. It is what the report and
// diagnostics show in place of the resolved OID.
func (pr PR) Label() string {
	if pr.Owner == "" {
		return fmt.Sprintf("#%d", pr.Num)
	}
	return fmt.Sprintf("%s/%s#%d", pr.Owner, pr.Repo, pr.Num)
}

// ResolvePR turns a PR reference into a concrete probe request: it fetches the
// PR head (pinning it to an OID), resolves the base branch, and labels both for
// the report. Base resolution runs most-explicit-first — an --onto override
// wins, else the Forge (gh) answers, else an origin PR falls back to git's
// origin/HEAD with a note. It returns human notes for stderr and never touches
// the worktree: the fetch writes only objects and FETCH_HEAD.
func ResolvePR(ctx context.Context, g Git, f Forge, pr PR, onto string) (Options, []string, error) {
	source, isOrigin, err := prSource(ctx, g, pr)
	if err != nil {
		return Options{}, nil, err
	}
	topicOID, err := g.Fetch(ctx, source, fmt.Sprintf("refs/pull/%d/head", pr.Num))
	if err != nil {
		if ce := core.AsError(err); ce != nil && ce.Code == core.CodeNotFound {
			return Options{}, nil, core.NotFoundf("pr-not-found",
				"%s: no PR head found — check the number and that the repo is accessible (%s)", pr.Label(), ce.Msg)
		}
		return Options{}, nil, err
	}
	opts := Options{Topic: topicOID, TopicLabel: pr.Label()}

	// An explicit --onto is the caller's stated intent and beats any inference.
	if onto != "" {
		opts.Base, opts.BaseLabel = onto, onto
		return opts, nil, nil
	}

	// The forge (gh) is the accurate source of a PR's base branch.
	baseRef, ok, ferr := f.PRBaseRef(ctx, pr.Owner, pr.Repo, pr.Num)
	if ok {
		baseOID, err := g.Fetch(ctx, source, baseRef)
		if err != nil {
			return Options{}, nil, err
		}
		opts.Base, opts.BaseLabel = baseOID, baseRef
		return opts, nil, nil
	}

	// Fallback: only an origin PR has a sensible git-only default (origin/HEAD);
	// for any other repo without gh we cannot know the base and must ask for
	// --onto rather than probe against a wrong one.
	reason := "gh unavailable"
	if ferr != nil {
		reason = "gh error: " + ferr.Error()
	}
	if !isOrigin {
		return Options{}, nil, core.Validationf("pr-base-unknown",
			"cannot determine %s's base branch (%s); pass --onto <ref>", pr.Label(), reason)
	}
	base, err := g.DefaultBase(ctx)
	if err != nil {
		return Options{}, nil, err
	}
	opts.Base, opts.BaseLabel = base, base
	note := fmt.Sprintf("%s: base assumed to be %s (%s); pass --onto <ref> to be explicit", pr.Label(), base, reason)
	return opts, []string{note}, nil
}

// prSource decides where to fetch a PR from: an origin PR uses "origin"; an
// owner/repo PR uses a configured remote already pointing at that repository, or
// the GitHub HTTPS URL when none does. isOrigin marks whether origin/HEAD is a
// valid base fallback.
func prSource(ctx context.Context, g Git, pr PR) (source string, isOrigin bool, err error) {
	if pr.Owner == "" {
		return "origin", true, nil
	}
	remotes, err := g.Remotes(ctx)
	if err != nil {
		return "", false, err
	}
	for name, url := range remotes {
		if o, r, ok := parseGitHubRepo(url); ok && sameRepo(o, r, pr.Owner, pr.Repo) {
			return name, name == "origin", nil
		}
	}
	return fmt.Sprintf("https://github.com/%s/%s.git", pr.Owner, pr.Repo), false, nil
}

// parsePRNum parses a positive decimal PR number. Empty, non-digit, or zero is
// rejected; the bound keeps a pathological input from overflowing int.
func parsePRNum(s string) (int, bool) {
	if s == "" {
		return 0, false
	}
	n := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
		if n > 1<<30 {
			return 0, false
		}
	}
	if n < 1 {
		return 0, false
	}
	return n, true
}

// splitOwnerRepo validates the "owner/repo" half: exactly one slash, both parts
// non-empty and not flag-shaped (leading '-'), so a value that could be
// mistaken for a git option can never reach the fetch.
func splitOwnerRepo(s string) (owner, repo string, ok bool) {
	owner, repo, found := strings.Cut(s, "/")
	if !found || owner == "" || repo == "" {
		return "", "", false
	}
	if strings.Contains(repo, "/") {
		return "", "", false
	}
	if owner[0] == '-' || repo[0] == '-' {
		return "", "", false
	}
	return owner, repo, true
}
