---
name: gh-stack-extension
description: Work with GitHub stacked pull requests through the gh-stack-extension CLI (`gh stack-extension`). Use whenever a change is split into dependent branches or pull requests, the user mentions a stack, stacked PRs, layers, restacking, or merging a chain of PRs, or a branch is cut from another feature branch rather than from the trunk.
---

# Stacked pull requests with gh-stack-extension

`gh stack-extension` turns local branches cut one from another into pull
requests linked as a GitHub stack, keeps them in sync, and merges them. It
never prompts, prints JSON with `--json`, and is safe to rerun. Use it instead
of hand-made `gh pr create --base ...` chains or `gh api` calls.

## Setup

```sh
gh extension install manz/gh-stack-extension   # once; needs `gh auth login`
```

## Rules

- Pass `--json` and read the result; branch on the exit code, not on text.
- Run `--dry-run` first for anything that changes state (`submit`, `create`,
  `add`, `adopt`, `unstack`, `restack`, `push`, `merge`) when the outcome is not
  obvious, and show the plan before running it for real.
- Every new pull request needs a description: the first commit of the branch
  must have a body, or pass `--message BRANCH=FILE` (title on line one, a blank
  line, then the body). `submit` refuses an empty description (exit 2) before
  pushing anything.
- One concern per branch; cut each new branch from the top of the stack.
- Merging is outward-facing: only merge when the user asks. `merge PR` needs
  `--repo OWNER/REPO`; a bare `merge` merges the current branch's PR.
- Never edit `.git` stack state by hand and never run `gh api` to check a stack:
  `view` and `status` report everything.

## Build a stack

```sh
git checkout -b feature/one                 # from the trunk
git commit -m "Subject" -m "Why this layer exists."
git checkout -b feature/two                 # from feature/one
git commit -m "Subject" -m "Why this layer exists."
gh stack-extension submit --dry-run --json  # plan: pushes, PRs, link
gh stack-extension submit --json            # push changed branches, open PRs, link the stack
```

A bare `submit` takes every local branch between the trunk (the remote's
default branch) and `HEAD`, bottom first, each based on the one below. On a
branch already in a stack it takes that stack. Rerunning it reports
`unchanged` and pushes nothing.

To add a layer later: cut a branch from the stack's top, commit, `submit`.

## Keep it in sync

```sh
gh stack-extension status --json
```

Each layer has a `sync` state against GitHub's head:

| `sync` | Meaning | Do |
|---|---|---|
| `in_sync` | local = GitHub | nothing |
| `ahead` | local has new commits | `push` |
| `behind` | GitHub has commits you lack | pull that branch first |
| `diverged` | both moved (after a rebase) | `push` (uses `--force-with-lease`) |
| `missing` | no local branch | check it out |
| `unknown` | GitHub's commit not fetched | `git fetch`, rerun |

`needs_restack: true` means the layer does not contain its parent's tip.

After changing a lower layer (review fix, rebase on a moved trunk):

```sh
gh stack-extension restack --json   # replays each layer onto its parent, bottom up
gh stack-extension push --json      # pushes only the branches that changed
```

On a conflict `restack` exits 4 and names the branch. Resolve the files,
`git add` them, then `gh stack-extension restack --continue`. To give up,
`gh stack-extension restack --abort` puts every branch back where it was.

## Existing pull requests

- `adopt [PR|BRANCH]` links pull requests already chained by base branch.
- `create PR...` (bottom first) / `add STACK PR...` link explicit numbers.
- `view [STACK]`, `--pr N`, or no argument for the current branch's stack.
- `unstack STACK` dissolves a stack; the pull requests stay open.

## Merge

```sh
gh stack-extension merge --dry-run --json                       # what lands
gh stack-extension merge 42 --repo owner/repo --wait --json     # merge #42 and every layer below
```

`--wait` polls until `merged`, `enqueued` or `failed`. `--queue` / `--direct`
choose the merge queue or a direct merge; `--method merge|squash|rebase`.

## Exit codes

| Code | Meaning | Typical next step |
|---|---|---|
| 0 | done (also "nothing to change") | |
| 1 | error (git, network, file) | read stderr |
| 2 | usage or refused (no description, PR number without `--repo`, restack waiting on a conflict) | fix the command |
| 3 | not found (stack, PR, branch) | check names; `git fetch` |
| 4 | conflict (restack conflict, merge pending, branch behind, branch already merged) | follow the message |
| 5 | validation (PRs do not chain, a fork, a PR in another stack) | fix the branch layout |
| 6 | merge failed | read `message`; fix checks; retry |

A branch whose pull request already merged stops `submit` (exit 4): run
`git fetch` and rebase the remaining branches onto the updated trunk.
