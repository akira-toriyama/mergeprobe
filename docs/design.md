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
`both_touched_clean`. **PR-number resolution** (`mergeprobe 123` → fetch
`pull/N/head`) and **`--rebase`** per-commit simulation (the design's thick
differentiator) now ship too (notes below). The ref-pair core was the
load-bearing foundation both built on.

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

Hardening from an adversarial multi-lens review (each finding reproduced against
git 2.53 before fixing):

4. **`clean_merges` / `both_touched_clean` subtract a conflict *footprint*, not a
   count.** git parks file/dir and some rename conflicts under a synthetic stage
   path (e.g. `d~ours`) absent from either diff, so `|union| - |conflictedSet|`
   cancels unrelated real paths. The footprint is the union of stage paths and
   the real paths named in each **CONFLICT** info message (`d~ours`'s message
   lists `d`), subtracted by set membership. Only CONFLICT-type messages count —
   an `Auto-merging <path>` message names a file that merged *cleanly*, which is
   exactly a `both_touched_clean` file and must not be excluded (a re-review
   caught the first cut wrongly folding those in).
5. **Unrelated histories are graceful, not an internal error.** `merge-tree`
   without `--allow-unrelated-histories` fatals (exit 128), which mapped to
   CodeInternal (exit 3). The flag makes disjoint refs merge as add/add against
   the empty base; `MergeBase` then returns no ancestor, `merge_base` is omitted,
   and both_touched/clean_merges diff against the empty tree.
6. **Oversized blobs are size-checked, never slurped.** A conflicted blob over 16
   MiB is flagged `truncated` with no inline sample instead of being read whole
   into memory (`cat-file -s` before `-p`) — the JSON stays bounded even when a
   multi-hundred-MB generated file / lockfile / dump conflicts. (git's own
   in-memory merge of such a file is a separate, inherent cost.)
7. **A stdout write failure is IO (exit 3), not usage (exit 2).** `RunE` wraps the
   write error as a `*core.Error{CodeInternal}` so the cobra→validation fallback
   in `Execute` applies only to genuine flag/arg parse errors.
8. **The conflict-marker size is not hard-coded 7.** A file can lower its
   `conflict-marker-size` gitattribute, and merge-tree obeys it (a size-4 file
   gets `<<<<`/`====`/`>>>>`), so a fixed 7-char matcher silently reported
   `hunks:0` with no sample for a real text conflict. `ConflictHunks` now takes
   the size; `buildConflict` reads the effective value from the *merged tree*
   (`check-attr --source=<tree>`, matching what merge-tree used) and retries —
   but only when the default-7 pass found nothing, so the common case pays no
   extra I/O. (Found by the second-round review; verdict/class were always
   correct since they derive from stages, so this was a degraded sample.)
   `check-attr --source` needs git 2.40+, above the 2.38 `merge-tree
   --write-tree` floor; on 2.38–2.39 the lookup errors, `buildConflict` swallows
   it, and a lowered-marker conflict falls back to the pre-fix `hunks:0`/no-sample
   — a graceful degradation of one rare case, not a failure.

## Implementation notes (PR-number resolution)

Shipped: `mergeprobe 123` and `mergeprobe owner/repo#123` resolve a GitHub PR to
the same ref-pair probe. `probe.ParsePRRef` recognizes a bare (optionally
`#`-prefixed) number as an origin PR and `owner/repo#N` as a foreign one;
`probe.ResolvePR` orchestrates the resolution over the ports and hands ordinary
`Options` to `Run`, so the merge/verdict path is entirely unchanged.

1. **The head is fetched and pinned to an OID, never left on a moving ref.**
   `git fetch --no-tags <source> refs/pull/N/head` writes only objects and
   FETCH_HEAD (no tracking ref), which `Fetch` immediately resolves to a commit
   OID. This keeps the "worktree untouched" promise — the fetch is the same kind
   of object-only write `merge-tree` already does — and makes the probe
   race-free against a concurrent fetch.

2. **The base branch is the one fact git cannot supply, so it degrades.** A PR's
   base lives in GitHub metadata, not in `refs/pull/N/head`. Resolution order:
   an explicit `--onto` wins; else the **`Forge` port** (a `gh pr view --json
   baseRefName` adapter) answers; else an *origin* PR falls back to
   `origin/HEAD` with a `note:` on stderr. A non-origin PR with no `gh` errors
   (validation) rather than probe against a guessed base. `gh` is optional by
   design: the `forge.GH` adapter reports *unavailable* (ok=false, no error)
   when `gh` is absent, and a *reason* when `gh` ran but failed — the difference
   between "expected" and "worth mentioning".

3. **`owner/repo#N` routes to a matching remote, else the HTTPS URL.**
   `prSource` scans `git remote -v` for a remote whose fetch URL parses
   (`parseGitHubRepo`, any scp/https/ssh spelling) to the same owner/repo
   (case-insensitively), and fetches from that remote name; with no match it
   fetches straight from `https://github.com/owner/repo.git`. No remote is
   added, so the caller's config is untouched.

4. **The report shows what the user named, not OIDs.** Because the resolved
   `base`/`topic` are commit OIDs, `Options` carries `TopicLabel`/`BaseLabel`
   (`#123` / `owner/repo#123` / the base branch) that `Run` prefers for the
   `base`/`topic` fields — the JSON reads the way the invocation did.

5. **Layering holds.** The new work is a pure parser (`ParsePRRef`,
   `parseGitHubRepo`, fake-free unit tests) + orchestration (`ResolvePR` over
   the `Git`+`Forge` ports, fake-tested) + two adapters (`git.Fetch`/`Remotes`
   real-git tested; `forge.GH` driven against a fake `gh` script). `cli` wires a
   PR-shaped topic through `ResolvePR`, else the plain ref pair — the sole
   branch point.

## Implementation notes (--rebase simulation)

Shipped: `mergeprobe <topic> --onto <base> --rebase` (design note 2, the thick
differentiator). It answers "does this branch *rebase* cleanly, and if not which
commit first conflicts?" — a different question from the static merge, and a
harder one, since it means running merge-tree once per commit.

1. **A rebase is a sequence of 3-way merges, one per commit.** `RunRebase`
   enumerates `base..topic` oldest-first (`git log --reverse --topo-order`, each
   commit carrying its first parent + subject) and replays each onto a *running
   tree* (the base commit to start). Each step is
   `merge-tree --write-tree --merge-base=<commit^> <runningTree> <commit>` — i.e.
   apply the commit's delta-from-its-parent onto the rebased state so far — and a
   clean step's result tree becomes the next step's running tree. Verified against
   a real `git rebase`: the simulation flags the same commit and file git stops
   on. merge-tree accepts a bare tree OID for the running side, which is what lets
   the state thread commit to commit.

2. **Merge-cleanly ≠ rebase-cleanly, and that gap is the value.** A merge compares
   the two endpoints; a rebase replays intermediate states, so a branch can merge
   clean yet a mid-history commit conflict (observed live: this repo's PR #2 head
   *merges* clean onto an advanced main but its first commit fails to *rebase*).
   The static `mergeable` and the rebase `rebaseable` are genuinely distinct
   verdicts, which is the moat over a bare merge-tree wrapper.

3. **Stop at the first conflict — it is correct, not just an MVP.** Once a step
   conflicts, the running tree carries markers and is not meaningfully replayable
   (a real rebase halts for manual resolution too), so there is nothing sound to
   "keep going" to. `RunRebase` reports that one commit (short OID + subject) and
   its conflicts in the *same* `Conflict` shape as the static probe, plus
   `applied` (how far it got). No worktree is touched: every step is an in-memory
   merge-tree, same as the static probe.

4. **Reuse over re-implementation.** Topic/base resolution is the shared
   `resolveTopicBase` (extracted from `Run`, so labels and validation match), and
   each conflicting file goes through the same `buildConflict` (class, binary,
   bounded sample, marker-size handling). `--rebase` composes with PR resolution
   (`mergeprobe 123 --rebase`) for free — it just runs on the resolved
   `Options` — and is rejected with `--path` (drill-down is a static-probe
   concern). v1 replays each commit by its first parent; a topic containing merge
   commits is approximated (not `rebase --rebase-merges`), which the common linear
   feature branch never hits.
