---
name: verify
description: Build and drive mergeprobe end-to-end against throwaway git repos to verify a change at its CLI surface.
---

# Verifying mergeprobe changes

Build (no special setup):

```sh
go build -o "$SCRATCH/mergeprobe" ./cmd/mergeprobe
```

Drive it against a throwaway repo — mergeprobe only needs `git init -b main`
plus a pinned identity (`user.email`/`user.name`, `commit.gpgsign false`).
Useful scenarios:

- **Static conflict**: two branches editing the same line; probe `theirs --onto ours`.
- **Rebase conflict**: linear topic where a later commit touches a line the
  advanced main also changed; probe `topic --onto main --rebase`.
- **Merge-commit topic (first-parent approximation)**: merge a side branch with
  `--no-commit`, sneak an extra "evil" edit into the merge commit, advance main
  over the same line — the simulation conflicts on the merge commit while a real
  `git rebase main topic` succeeds (merges are dropped).

Check the contract, not just the verdict: stdout must stay pipeable JSON
(`| jq` on every run), notes and `{"error":{...}}` envelopes go to stderr,
exit codes are 0=ran / 1=not-found / 2=usage / 3=internal.

Gotchas:

- zsh eats `echo ===` (`=cmd` expansion) — quote banner strings.
- A real `git rebase` in the scenario repo moves `topic` and checks it out;
  `git checkout main` before `git branch -f topic <oid>` to restore.
