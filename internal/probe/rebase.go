package probe

import (
	"context"
	"fmt"

	"github.com/akira-toriyama/mergeprobe/internal/core"
)

// RunRebase simulates rebasing topic onto base and reports whether it lands
// cleanly — and if not, which commit first conflicts and where. It replays each
// commit in base..topic onto a running tree via a 3-way merge whose base is that
// commit's parent (the same delta a rebase applies), threading the result tree
// into the next step. A real rebase stops at the first conflict and so does
// this — the running tree past a conflict is not meaningfully replayable — so at
// most one conflicting commit is reported. A non-empty opts.Path narrows that
// commit's report to the one file with the fuller drill-down sample, erroring
// not-found when the commit does not conflict on it (or the rebase is clean).
// Nothing touches the worktree.
//
// This is the design's differentiator: rebase conflicts differ from merge
// conflicts, agents usually rebase, and simulating one by hand means running
// merge-tree per commit — genuinely hard to compose in a single turn.
//
// The returned notes are human diagnostics for stderr (the ResolvePR
// convention): today, a warning when the range contains merge commits, which
// replay as first-parent deltas — an approximation a real rebase (which drops
// merges) does not share.
func RunRebase(ctx context.Context, g Git, opts Options) (core.RebaseReport, []string, error) {
	topic, base, err := resolveTopicBase(ctx, g, opts)
	if err != nil {
		return core.RebaseReport{}, nil, err
	}

	commits, err := g.CommitsToReplay(ctx, base, topic)
	if err != nil {
		return core.RebaseReport{}, nil, err
	}
	report := core.RebaseReport{
		Base:       orLabel(opts.BaseLabel, base),
		Topic:      orLabel(opts.TopicLabel, topic),
		Commits:    len(commits),
		Rebaseable: true,
	}
	notes := mergeCommitNotes(report.Base, report.Topic, commits)

	// running is the tree each successive commit is replayed onto: the base
	// commit to start, then each clean step's result tree.
	running := base
	for i, c := range commits {
		out, conflicted, err := g.MergeTree3(ctx, c.Parent, running, c.OID)
		if err != nil {
			return core.RebaseReport{}, nil, err
		}
		parsed, err := core.ParseMergeTreeZ(out)
		if err != nil {
			return core.RebaseReport{}, nil, err
		}
		if conflicted {
			report.Rebaseable = false
			report.Applied = i // commits that landed cleanly before this one
			rc := &core.RebaseConflict{Commit: shorten(c.OID), Subject: c.Subject}
			if opts.Path != "" {
				// Drill-down: isolate the one requested file of this commit's
				// conflicts and emit its fuller sample, like the static probe.
				found := false
				for _, f := range parsed.Files {
					if f.Path == opts.Path {
						rc.Conflicts = []core.Conflict{buildConflict(ctx, g, parsed.Tree, f, drillSampleLines, true)}
						found = true
						break
					}
				}
				if !found {
					return core.RebaseReport{}, notes, core.NotFoundf("path-not-conflicted",
						"%q is not among the files commit %s conflicts on (run without --path to list them)",
						opts.Path, rc.Commit)
				}
			} else {
				for _, f := range parsed.Files {
					rc.Conflicts = append(rc.Conflicts, buildConflict(ctx, g, parsed.Tree, f, summarySampleLines, false))
				}
			}
			report.Conflict = rc
			report.Normalize()
			return report, notes, nil
		}
		running = parsed.Tree
	}
	if opts.Path != "" {
		return core.RebaseReport{}, notes, core.NotFoundf("path-not-conflicted",
			"the rebase replays cleanly, so there is no conflicted file to drill into")
	}
	report.Applied = len(commits)
	report.Normalize()
	return report, notes, nil
}

// mergeCommitNotes warns when the replay range contains merge commits: each
// replays as its first-parent delta, while a real rebase drops merges, so the
// verdict can differ (design.md "Implementation notes (--rebase simulation)").
// A linear topic — the common case — yields nothing.
func mergeCommitNotes(base, topic string, commits []core.Commit) []string {
	merges := 0
	for _, c := range commits {
		if c.Merge {
			merges++
		}
	}
	if merges == 0 {
		return nil
	}
	plural := ""
	if merges > 1 {
		plural = "s"
	}
	return []string{fmt.Sprintf("%s..%s contains %d merge commit%s, replayed as first-parent deltas; a real rebase drops merges, so the verdict can differ",
		base, topic, merges, plural)}
}
