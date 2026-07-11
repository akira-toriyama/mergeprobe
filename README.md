# mergeprobe

Probe what a branch conflicts with — **without touching the worktree**. Wraps
git 2.38+'s `merge-tree --write-tree` (in-memory merge) and renders the plumbing
output as bounded JSON an AI coding agent can consume in one turn.

> **Status: v0.1 — the static merge probe works.** Any ref pair, resolution
> classes, bounded samples, `--path` drill-down, and the `both_touched_clean`
> blind-spot list all ship. PR-number resolution (`mergeprobe 123`) and
> `--rebase` per-commit simulation are the next milestones — see
> [docs/design.md](docs/design.md).

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
$ mergeprobe feature-x --path app/Kconfig   # drill into one conflicted file
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

## Exit codes

| Code | Meaning |
|---|---|
| `0` | probe ran — **read `.mergeable`** for the verdict (a conflict is not an error) |
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

## License

[MIT](LICENSE)
