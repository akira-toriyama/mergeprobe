# mergeprobe — design

> Distilled from tracker task `t-2kq2` (private board, 2026-07-03 research,
> refined 2026-07-06). This is the pre-implementation design; verify premises
> (git version, upstream issue state) before building on them.

## What

"What does this PR/branch conflict with, where, and how badly?" in one turn.
Wraps git 2.38+'s `merge-tree --write-tree` (in-memory merge — the worktree is
never touched) and turns its arcane plumbing output into bounded JSON.
Verified working against git 2.50.

## Pain

- The GitHub API returns only `mergeable: false` after async polling; **no
  endpoint lists the conflicting files** (cli/cli#872 / #1358, open for years).
- Today's agent flow: fetch PR head → checkout/merge → read conflict markers →
  `merge --abort` → hope the worktree is intact. 4-6 turns, and a leftover
  mid-merge state poisons the whole session's git usage — a real observed risk.
- AWS CodeCommit ships `get-merge-conflicts`; the product shape is proven.

## Design notes (from the verification agent's refinement)

1. **Generalize the subject**: `mergeprobe [<branch>] [--onto <ref>]` for any
   ref pair; PR-number resolution is sugar. "Does my branch land cleanly on
   origin/main?" is the higher-frequency question — it lifts utility from
   weekly to daily.
2. **`--rebase` simulation is the strongest differentiator**: agents usually
   rebase, rebase conflicts differ from merge conflicts, and simulating one
   means running merge-tree per commit — genuinely hard to hand-compose in one
   turn. A bare merge-tree wrapper has a thin moat; this is the thick part.
3. Drop the remote temp-clone mode from v1 (blobless clones are neither fast
   nor cheap). Require a clone; fetch `pull/N/head` as needed.
4. Report **resolution classes** derived for free from index stage info
   (both-modified / delete-modify / add-add / binary) and the
   **`both_touched_clean` list** —
   files merge-tree merged cleanly but both sides touched (the semantic-
   conflict blind spot no existing tool reports).
5. `sample` is hard-capped; `--path <file>` drills down — the "bounded summary
   in 1 turn → targeted detail in 1 turn" flow.

## Refs

- https://git-scm.com/docs/git-merge-tree
- https://github.com/cli/cli/issues/872 / #1358 (conflict file list, long-open ask)
- https://docs.aws.amazon.com/cli/latest/reference/codecommit/get-merge-conflicts.html
