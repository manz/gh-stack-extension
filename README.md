# gh-stack-extension

[![ci](https://github.com/manz/gh-stack-extension/actions/workflows/ci.yml/badge.svg)](https://github.com/manz/gh-stack-extension/actions/workflows/ci.yml)
[![release](https://github.com/manz/gh-stack-extension/actions/workflows/release.yml/badge.svg)](https://github.com/manz/gh-stack-extension/actions/workflows/release.yml)
[![Quality Gate Status](https://sonarcloud.io/api/project_badges/measure?project=manz_gh-stack-extension&metric=alert_status)](https://sonarcloud.io/summary/new_code?id=manz_gh-stack-extension)
[![Coverage](https://sonarcloud.io/api/project_badges/measure?project=manz_gh-stack-extension&metric=coverage)](https://sonarcloud.io/summary/new_code?id=manz_gh-stack-extension)
[![Maintainability Rating](https://sonarcloud.io/api/project_badges/measure?project=manz_gh-stack-extension&metric=sqale_rating)](https://sonarcloud.io/summary/new_code?id=manz_gh-stack-extension)
[![Reliability Rating](https://sonarcloud.io/api/project_badges/measure?project=manz_gh-stack-extension&metric=reliability_rating)](https://sonarcloud.io/summary/new_code?id=manz_gh-stack-extension)
[![Security Rating](https://sonarcloud.io/api/project_badges/measure?project=manz_gh-stack-extension&metric=security_rating)](https://sonarcloud.io/summary/new_code?id=manz_gh-stack-extension)
[![Go](https://img.shields.io/github/go-mod/go-version/manz/gh-stack-extension)](go.mod)

A `gh` extension for GitHub [stacked pull requests][stacks], built to be driven
by scripts and agents: it never prompts, every command prints JSON with
`--json`, reruns are safe (each pull request reports `created`, `retargeted` or
`unchanged`), anything that changes state takes `--dry-run`, and failures map
to exit codes. The stack is always derived from git and GitHub; there is no
local state to drift.

[stacks]: https://docs.github.com/en/pull-requests/get-started/about-stacked-prs

## Install

```sh
gh extension install manz/gh-stack-extension
```

### Agent skill

[`skills/gh-stack-extension`](skills/gh-stack-extension/SKILL.md) teaches a
coding agent the loop, the rules and the exit codes. For Claude Code:

```sh
mkdir -p ~/.claude/skills && cp -r skills/gh-stack-extension ~/.claude/skills/
```

## The loop

```sh
git checkout -b next-layer          # from the top of a stack, or from the trunk
git commit                          # subject = PR title, body = PR description
gh stack-extension submit           # push, open the PR on the branch below, link the stack
gh stack-extension status           # each layer: in_sync, ahead, behind, diverged, missing
gh stack-extension restack          # after a lower layer changed: replay each layer onto its parent
                                    # on a conflict: fix it, git add, restack --continue (or --abort)
gh stack-extension push             # push the branches that differ from GitHub
gh stack-extension merge --wait     # merge the current PR and every layer below it
```

A bare `submit` takes every local branch between the trunk (the remote's
default branch) and `HEAD`, bottom first; or, on a branch already in a stack,
that stack. A new pull request needs a description: the first commit's body,
or `--message BRANCH=FILE` (title on the first line, body after a blank line).

## Commands

Every command takes `--json` and `--repo OWNER/REPO` (default: the repository
of the current directory). Flags can go before or after arguments.

| Command | Does | Flags |
|---|---|---|
| `list` | the repository's stacks | |
| `view [STACK]` | one stack: by number, `--pr N`, or the current branch's | `--pr` |
| `create PR...` | link pull requests, bottom first, into a stack (or grow the stack the first ones are in) | `--dry-run` |
| `add STACK PR...` | put pull requests on top of a stack | `--dry-run` |
| `unstack STACK` | dissolve a stack; its pull requests stay open | `--dry-run` |
| `adopt [PR\|BRANCH]` | stack the pull requests chained by base branch around one of them | `--dry-run` |
| `status [STACK]` | local branches against the stack's heads on GitHub | `--pr`, `--remote` |
| `restack [STACK]` | replay each layer onto its parent, bottom up; a conflict stops it (exit 4) until `--continue` or `--abort` | `--pr`, `--remote`, `--dry-run`, `--continue`, `--abort` |
| `push [STACK]` | push the branches that differ from GitHub (`--force-with-lease`) | `--pr`, `--remote`, `--dry-run` |
| `submit [BRANCH...]` | push, open or retarget each pull request, link them | `--base`, `--remote`, `--draft`, `--message BRANCH=FILE`, `--dry-run` |
| `merge [PR]` | merge or queue a pull request and every layer below it; a PR number needs `--repo` | `--method merge\|squash\|rebase`, `--queue`, `--direct`, `--wait`, `--interval`, `--timeout`, `--dry-run` |

## JSON

`list` and `view` print GitHub's stack objects as the [REST API][api] returns
them. The other commands print what they did:

```jsonc
// create, add, adopt
{"action": "created|added|unchanged|would-created|would-added", "pull_requests": [62, 63], "added": [63], "stack": {...}}
// status
{"stack": 65, "base": "master", "layers": [{"number": 62, "branch": "...", "parent": "origin/master",
  "local_sha": "...", "remote_sha": "...", "sync": "in_sync", "needs_restack": false, "merged_or_closed": false}]}
// submit
{"pushed": ["..."], "pull_requests": [{"branch": "...", "number": 64, "base": "...", "action": "created|retargeted|unchanged"}], "stack": {...}}
// merge
{"repository": "owner/repo", "status": "pending|merged|enqueued|failed", "uuid": "...", "message": "...", "pull_request": 64, "pull_requests": [62, 63, 64]}
```

[api]: https://docs.github.com/en/rest/pulls/stacks

## Exit codes

| Code | Meaning |
|---|---|
| 0 | done (including "nothing to change") |
| 1 | error (git, network, unreadable file) |
| 2 | wrong usage, or a refused request (no description, PR number without `--repo`) |
| 3 | not found (stack, pull request, branch) |
| 4 | conflict (restack conflict, merge already pending, branch behind GitHub, branch already merged) |
| 5 | validation failed (pull requests that do not chain, a fork, a PR in another stack) |
| 6 | merge failed |

## Develop

```sh
make check   # gofmt, go vet, revive, tests with an 80% coverage floor
make build
gh extension install .
```
