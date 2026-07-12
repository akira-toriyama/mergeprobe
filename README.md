# mergeprobe

Probe what a branch conflicts with — **without touching the worktree**. Wraps
git 2.38+'s `merge-tree --write-tree` (in-memory merge) and renders the plumbing
output as bounded JSON an AI coding agent can consume in one turn.

> **Status: the static merge probe, PR-number resolution, and `--rebase`
> simulation all work.** Any ref pair, resolution classes, bounded samples,
> `--path` drill-down, the `both_touched_clean` blind-spot list, `mergeprobe
> 123` / `owner/repo#123` PR resolution, and `--rebase` per-commit conflict
> simulation all ship — see [docs/design.md](docs/design.md).

## The pain this kills

- The GitHub API only says `mergeable: false` (after async polling) — there is
  **no endpoint that lists the conflicting files**
  ([cli/cli#872](https://github.com/cli/cli/issues/872), #1358, open for years).
- Today an agent fetches the PR head, checks it out, merges, reads conflict
  markers, aborts, and prays the worktree survives — 4-6 turns, and a leftover
  mid-merge state poisons every later git command in the session.
- AWS CodeCommit ships `get-merge-conflicts` (proof of the product shape);
  GitHub does not.

`merge-tree --write-tree` merges entirely in memory: it writes tree/blob objects
into `.git/objects` but never touches the index, `HEAD`, or the worktree.

## CLI

```console
$ mergeprobe                                # HEAD onto origin/HEAD — "does my branch land?"
$ mergeprobe feature-x                      # feature-x onto origin/HEAD
$ mergeprobe feature-x --onto origin/main   # any ref pair
$ mergeprobe 123                            # origin PR #123 — "does it still land?"
$ mergeprobe cli/cli#872                    # a PR in another repo
$ mergeprobe feature-x --path app/Kconfig   # drill into one conflicted file
$ mergeprobe feature-x --onto main --rebase # simulate a rebase, not a merge
```

The topic defaults to `HEAD`; `--onto` defaults to `origin/HEAD` (the remote's
default branch). A conflict verdict:

```console
$ mergeprobe theirs --onto ours
{
  "mergeable": false,
  "base": "ours",
  "topic": "theirs",
  "merge_base": "eb9e4463ca0a",
  "conflicts": [
    {"path": "add.txt", "class": "add-add", "hunks": 1,
     "sample": "<<<<<<< ours\nours add\n=======\ntheirs add DIFF\n>>>>>>> theirs\n"},
    {"path": "d.txt", "class": "modify-delete", "hunks": 0},
    {"path": "f.txt", "class": "both-modified", "hunks": 1, "sample": "<<<<<<< …"}
  ],
  "clean_merges": 0,
  "both_touched_clean": []
}
```

### Fields

| Field | Meaning |
|---|---|
| `mergeable` | true when the in-memory merge produced no conflicts |
| `base` / `topic` | the two refs merged (`base` = what `topic` lands on) |
| `merge_base` | short OID of the common ancestor; omitted for unrelated histories |
| `conflicts[].class` | `both-modified` / `add-add` / `modify-delete` / `delete-delete` / `other`, from index stages (locale-independent) |
| `conflicts[].binary` | true when the content is binary (no text merge; empty sample) |
| `conflicts[].hunks` | number of conflict regions in the merged file |
| `conflicts[].sample` | bounded excerpt of the first region with markers; `truncated` flags a cap |
| `clean_merges` | count of files the merge integrated without conflict |
| `both_touched_clean` | files **both sides changed** that still merged cleanly — the semantic-conflict blind spot no other tool reports |

Use `--path <file>` to get that one file's full conflict regions in a second
turn.

## Probing a pull request

`mergeprobe 123` resolves an origin PR, and `mergeprobe owner/repo#123` a PR in
another repository. Either way mergeprobe fetches `refs/pull/<n>/head` (pinning
it to a commit, so no branch or tracking ref moves) and probes it against the
PR's base — the same in-memory merge, just with the refs resolved for you.

The base branch is the one fact git alone cannot supply, so resolution degrades
gracefully:

1. an explicit `--onto <ref>` always wins;
2. otherwise [`gh`](https://cli.github.com) supplies the PR's real base branch
   when it is installed and authenticated;
3. otherwise an origin PR falls back to `origin/HEAD`, and a `note:` on **stderr**
   says so — stdout stays pure JSON, so the note never reaches `jq`.

`gh` is optional: with it, the base is exact; without it, `mergeprobe 123` still
works against `origin/HEAD` (pass `--onto` to be sure). For `owner/repo#123`,
mergeprobe fetches from whichever remote already points at that repo, or from
`https://github.com/owner/repo.git` when none does. The `base`/`topic` fields
echo the PR (`#123`, `owner/repo#123`) and base branch you named, not the raw
OIDs they resolved to.

## Simulating a rebase

Agents usually **rebase**, and a rebase conflict is not a merge conflict: a merge
looks at the two endpoints, a rebase replays each commit in turn, so a branch can
merge cleanly yet fail to rebase (or the reverse). `--rebase` replays `base..topic`
commit by commit and reports the **first commit that conflicts**:

```console
$ mergeprobe feature-x --onto main --rebase
{
  "rebaseable": false,
  "base": "main",
  "topic": "feature-x",
  "commits": 5,
  "applied": 2,
  "conflict": {
    "commit": "1a498dc64cdf",
    "subject": "retune the power budget",
    "conflicts": [
      {"path": "app/Kconfig", "class": "both-modified", "hunks": 1, "sample": "<<<<<<< …"}
    ]
  }
}
```

| Field | Meaning |
|---|---|
| `rebaseable` | true when every topic commit replays onto base with no conflict |
| `commits` | number of commits `base..topic` — the replay length |
| `applied` | how many replayed cleanly before a conflict stopped it (`== commits` when rebaseable) |
| `conflict` | the first commit that failed to replay (`commit` short OID, `subject`, and its `conflicts[]` in the same shape as above); omitted for a clean rebase |

Each commit is replayed with an in-memory 3-way merge against its own parent, so
the worktree is never touched — the same guarantee as the static probe. Simulation
stops at the first conflict, exactly as a real rebase does. `--rebase` composes
with PR resolution (`mergeprobe 123 --rebase`) and with `--path`, which drills
into one conflicted file of the first conflicting commit (full sample, every
hunk) and exits 1 when that commit does not conflict on it — or when the rebase
replays cleanly and there is nothing to drill into.

A topic that itself contains **merge commits** is approximated: each replays as
its first-parent delta, while a real rebase drops merges, so the verdict can
differ (e.g. an "evil" resolution living only in the merge commit conflicts here
but vanishes in a real rebase). mergeprobe prints a `note:` to stderr when the
simulation actually replayed one — a merge past the first conflict never
influenced the verdict, so it is not flagged — and the common linear feature
branch never hits this.

## Exit codes

| Code | Meaning |
|---|---|
| `0` | probe ran — **read `.mergeable`** (or `.rebaseable` with `--rebase`) for the verdict (a conflict is not an error) |
| `1` | not found / empty result (e.g. `--path` on a non-conflicted file) |
| `2` | bad usage / validation — fix the args, do not retry |
| `3+` | internal / IO error |

stdout carries pipeable JSON only; diagnostics and the error envelope
(`{"error":{"code","message",…}}`) go to stderr, so `mergeprobe … | jq` stays
clean.

## Install

From source, for now:

```sh
go install github.com/akira-toriyama/mergeprobe/cmd/mergeprobe@latest
```

Requires git 2.38+ (`merge-tree --write-tree`) and a clone to run inside.
[`gh`](https://cli.github.com) is optional — it sharpens PR base-branch
resolution but mergeprobe runs without it.

## License

[MIT](LICENSE)
