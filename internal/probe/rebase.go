package probe

import (
	"context"

	"github.com/akira-toriyama/mergeprobe/internal/core"
)

// RunRebase simulates rebasing topic onto base and reports whether it lands
// cleanly — and if not, which commit first conflicts and where. It replays each
// commit in base..topic onto a running tree via a 3-way merge whose base is that
// commit's parent (the same delta a rebase applies), threading the result tree
// into the next step. A real rebase stops at the first conflict and so does
// this — the running tree past a conflict is not meaningfully replayable — so at
// most one conflicting commit is reported. Nothing touches the worktree.
//
// This is the design's differentiator: rebase conflicts differ from merge
// conflicts, agents usually rebase, and simulating one by hand means running
// merge-tree per commit — genuinely hard to compose in a single turn.
func RunRebase(ctx context.Context, g Git, opts Options) (core.RebaseReport, error) {
	topic, base, err := resolveTopicBase(ctx, g, opts)
	if err != nil {
		return core.RebaseReport{}, err
	}

	commits, err := g.CommitsToReplay(ctx, base, topic)
	if err != nil {
		return core.RebaseReport{}, err
	}

	report := core.RebaseReport{
		Base:       orLabel(opts.BaseLabel, base),
		Topic:      orLabel(opts.TopicLabel, topic),
		Commits:    len(commits),
		Rebaseable: true,
	}

	// running is the tree each successive commit is replayed onto: the base
	// commit to start, then each clean step's result tree.
	running := base
	for i, c := range commits {
		out, conflicted, err := g.MergeTree3(ctx, c.Parent, running, c.OID)
		if err != nil {
			return core.RebaseReport{}, err
		}
		parsed, err := core.ParseMergeTreeZ(out)
		if err != nil {
			return core.RebaseReport{}, err
		}
		if conflicted {
			report.Rebaseable = false
			report.Applied = i // commits that landed cleanly before this one
			rc := &core.RebaseConflict{Commit: shorten(c.OID), Subject: c.Subject}
			for _, f := range parsed.Files {
				rc.Conflicts = append(rc.Conflicts, buildConflict(ctx, g, parsed.Tree, f, summarySampleLines, false))
			}
			report.Conflict = rc
			report.Normalize()
			return report, nil
		}
		running = parsed.Tree
	}
	report.Applied = len(commits)
	report.Normalize()
	return report, nil
}
