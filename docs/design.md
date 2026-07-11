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

## Implementation notes (v0.1 — static merge probe)

Shipped: the ref-pair core. `mergeprobe [<topic>] [--onto <base>]` (topic
defaults to `HEAD`, base to `origin/HEAD`), the `-z` merge-tree parse, resolution
classes, `binary`, bounded `sample`, `--path` drill-down, `clean_merges`, and
`both_touched_clean`. Deferred to their own tasks: **PR-number resolution**
(`mergeprobe 123` → fetch `pull/N/head`) and **`--rebase`** per-commit simulation
(the design's thick differentiator). The ref-pair core is the load-bearing
foundation both build on.

Premises verified against git 2.53, with two corrections to this design:

1. **`-z` is the parse target, not the default format.** `merge-tree --write-tree
   -z` emits `<tree>\0`, then `<mode> <oid> <stage>\t<path>\0` per conflicted
   stage, a `\0` separator, then `<count>\0<path>…\0<type>\0<message>\0` info
   records. A clean merge emits only `<tree>\0`. -z never quotes paths and gives
   a structured type field, so parsing needs no locale-sensitive matching — and
   `core` classifies purely from **index stages**, never from git's (localizable)
   English messages.

2. **Binary is NOT stage-derivable** (design note 4 assumed it was). A binary
   conflict has stages `{1,2,3}` identical to a text both-modified; git's "Cannot
   merge binary files" is a *stderr* warning absent from the -z stream. So
   `binary` is a separate content-derived boolean (NUL in the first 8000 bytes,
   git's own heuristic), not a `class` value. `class` stays structural.

3. **`merge-tree` rejects `--end-of-options`** (rev-parse/merge-base/diff accept
   it). Flag-shaped refs are instead rejected up front (`probe.validateRef`) and
   caught again by `rev-parse --end-of-options` in `ResolveCommit`, which runs
   before `merge-tree` — so a `-`-prefixed ref never reaches it.

Layering: `core` (pure parse/classify/sample, table+fuzz tested) ← `probe`
(orchestration over a `Git` port, fake-tested) ← `git` (os/exec adapter,
real-git integration tested) ← `cli` (cobra). A successful probe exits 0 whether
or not it merges cleanly; the verdict is `.mergeable` in the payload.
