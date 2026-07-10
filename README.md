# mergeprobe

Probe what a PR or branch conflicts with — **without touching the worktree**.
Wraps git 2.38+'s `merge-tree --write-tree` (in-memory merge) and renders the
plumbing output as bounded JSON an AI coding agent can consume in one turn.

> **Status: pre-v0 scaffold.** The house Go CLI spine (exit-code contract,
> stdout/stderr discipline, cobra shell) is in place; the merge-tree logic is
> not implemented yet. The design lives in [docs/design.md](docs/design.md).

## The pain this kills

- The GitHub API only says `mergeable: false` (after async polling) — there is
  **no endpoint that lists the conflicting files**
  ([cli/cli#872](https://github.com/cli/cli/issues/872), #1358, open for years).
- Today an agent fetches the PR head, checks it out, merges, reads conflict
  markers, aborts, and prays the worktree survives — 4-6 turns, and a leftover
  mid-merge state poisons every later git command in the session.
- AWS CodeCommit ships `get-merge-conflicts` (proof of the product shape);
  GitHub does not.

## Planned CLI

```console
$ mergeprobe 123          # inside a clone; owner/repo#123 also works
{"mergeable":false,
 "conflicts":[{"path":"app/Kconfig","hunks":2,"ours_commits":["a1b2 power tune"],
   "theirs_commits":["c3d4 refactor kconfig"],"sample":"<<<<<<< …trimmed… >>>>>>>"}],
 "clean_merges":117}

$ mergeprobe feature-x --onto origin/main   # any ref pair
$ mergeprobe 123 --rebase                   # per-commit rebase simulation
$ mergeprobe 123 --path app/Kconfig         # drill into one file
```

## Exit codes

| Code | Meaning |
|---|---|
| `0` | OK |
| `1` | not found / empty result |
| `2` | bad usage / validation — fix the args, do not retry |
| `3+` | internal / IO error |

stdout carries pipeable payload only; diagnostics and the JSON error envelope
(`{"error":{"code","message",…}}`) go to stderr.

## Install

From source, for now:

```sh
go install github.com/akira-toriyama/mergeprobe/cmd/mergeprobe@latest
```

Requires git 2.38+ (`merge-tree --write-tree`).

## License

[MIT](LICENSE)
