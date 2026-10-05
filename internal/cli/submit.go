package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/google/go-github/v92/github"
	"github.com/manz/gh-stack-extension/internal/stackapi"
)

func init() {
	register("submit", command{usage: "push BRANCH... (bottom first; default: the branches between the trunk and HEAD), open or fix their pull requests, link the stack", setup: submitFlags})
}

type submitOpts struct {
	base     string
	remote   string
	draft    bool
	dryRun   bool
	messages map[string]string // branch -> file holding "title\n\nbody"
}

func submitFlags(fs *flag.FlagSet) func(context.Context, *env, []string) error {
	o := submitOpts{messages: map[string]string{}}
	fs.StringVar(&o.base, "base", "", "branch the bottom pull request targets (default: its current base, else the remote's default branch)")
	fs.StringVar(&o.remote, "remote", "origin", "git remote to push to")
	fs.BoolVar(&o.draft, "draft", false, "open new pull requests as drafts")
	fs.BoolVar(&o.dryRun, "dry-run", false, "print what would change, change nothing")
	fs.Func("message", "BRANCH=FILE: title and body for BRANCH's new pull request (repeatable)", func(v string) error {
		branch, file, ok := strings.Cut(v, "=")
		if !ok || branch == "" || file == "" {
			return fmt.Errorf("want BRANCH=FILE, got %q", v)
		}
		o.messages[branch] = file
		return nil
	})
	return func(ctx context.Context, e *env, args []string) error { return runSubmit(ctx, e, args, o) }
}

// submittedPR is what submit did for one branch.
type submittedPR struct {
	Branch string `json:"branch"`
	Number int    `json:"number"`
	Base   string `json:"base"`
	Action string `json:"action"` // created, retargeted, unchanged (would- under --dry-run)
}

type submitResult struct {
	Pushed       []string      `json:"pushed"`
	PullRequests []submittedPR `json:"pull_requests"`
	Stack        *linkResult   `json:"stack,omitempty"`
}

func runSubmit(ctx context.Context, e *env, branches []string, o submitOpts) error {
	if len(branches) == 0 {
		var err error
		if branches, err = e.currentBranches(ctx, o); err != nil {
			return err
		}
	}
	for b := range o.messages {
		if !contains(branches, b) {
			return usagef("--message for %s, which is not submitted", b)
		}
	}
	trunk, err := e.trunk(ctx, branches[0], o)
	if err != nil {
		return err
	}
	plan, err := e.planSubmit(ctx, branches, trunk, o)
	if err != nil {
		return err
	}
	res := submitResult{}
	if res.Pushed, err = e.pushChanged(branches, o); err != nil {
		return err
	}
	if res.PullRequests, err = e.applySubmit(ctx, plan, o); err != nil {
		return err
	}
	if res.Stack, err = e.linkSubmitted(ctx, res.PullRequests, o.dryRun); err != nil {
		return err
	}
	return e.emit(res, func(w io.Writer) { printSubmit(w, res, o) })
}

// pushChanged pushes the branches that differ from their remote-tracking ref.
func (e *env) pushChanged(branches []string, o submitOpts) ([]string, error) {
	toPush, err := e.changedBranches(o.remote, branches)
	if err != nil || o.dryRun || len(toPush) == 0 {
		return toPush, err
	}
	return toPush, e.Git.Push(o.remote, toPush)
}

// linkSubmitted links two or more submitted pull requests into one stack.
func (e *env) linkSubmitted(ctx context.Context, prs []submittedPR, dryRun bool) (*linkResult, error) {
	nums := make([]int, len(prs))
	for i, p := range prs {
		nums[i] = p.Number
	}
	switch {
	case len(nums) < 2:
		return nil, nil
	case contains0(nums):
		return &linkResult{Action: "would-link", PullRequests: nums}, nil
	}
	link, err := e.link(ctx, nums, dryRun)
	return &link, err
}

// currentBranches is what a bare submit means: the current branch's stack
// when it is in one; otherwise the local branches cut one from another
// between the trunk and the current branch, bottom first, then the branch.
func (e *env) currentBranches(ctx context.Context, o submitOpts) ([]string, error) {
	current, err := e.Git.CurrentBranch()
	if err != nil {
		return nil, err
	}
	p, err := e.openPRForHead(ctx, current)
	if err != nil {
		return nil, err
	}
	if p != nil && p.Stack != nil && p.Stack.Number != nil {
		s, _, err := e.PRs.GetStack(ctx, e.owner, e.repo, *p.Stack.Number)
		if err != nil {
			return nil, stackapi.Classify(err)
		}
		return openBranches(s), nil
	}
	below, err := e.branchesBelow(ctx, current, o)
	if err != nil {
		return nil, err
	}
	return append(below, current), nil
}

// branchesBelow is the local branches in current's history and not yet in
// the trunk, bottom first. They must form one line: a fork is an error.
func (e *env) branchesBelow(ctx context.Context, current string, o submitOpts) ([]string, error) {
	trunk, err := e.trunkName(o)
	if err != nil {
		return nil, err
	}
	trunkSHA, err := e.Git.RefSHA("refs/remotes/" + o.remote + "/" + trunk)
	if err != nil {
		return nil, err
	}
	locals, err := e.Git.LocalBranches()
	if err != nil {
		return nil, err
	}
	head := locals[current]
	var below []string
	for name, sha := range locals {
		inTrunk := trunkSHA != "" && e.Git.IsAncestor(sha, trunkSHA)
		if name != current && name != trunk && !inTrunk && e.Git.IsAncestor(sha, head) {
			below = append(below, name)
		}
	}
	if err := e.refuseMerged(ctx, below, o.remote, trunk); err != nil {
		return nil, err
	}
	return e.orderChain(below, locals)
}

// refuseMerged stops on a branch whose pull request already merged: the local
// trunk is stale, and submitting would open a second pull request for it.
func (e *env) refuseMerged(ctx context.Context, branches []string, remote, trunk string) error {
	for _, b := range branches {
		prs, _, err := e.PRs.List(ctx, e.owner, e.repo, &github.PullRequestListOptions{State: "closed", Head: e.owner + ":" + b})
		if err != nil {
			return stackapi.Classify(err)
		}
		for _, p := range prs {
			if p.MergedAt != nil {
				return fmt.Errorf("%w: %s already merged as #%d; run git fetch %s and rebase onto %s/%s", stackapi.ErrConflict, b, p.GetNumber(), remote, remote, trunk)
			}
		}
	}
	return nil
}

// orderChain sorts branches so each one's tip is in the next one's history.
func (e *env) orderChain(branches []string, tips map[string]string) ([]string, error) {
	sort.Strings(branches) // stable error messages
	sort.SliceStable(branches, func(i, j int) bool {
		return tips[branches[i]] != tips[branches[j]] && e.Git.IsAncestor(tips[branches[i]], tips[branches[j]])
	})
	for i := 1; i < len(branches); i++ {
		a, b := branches[i-1], branches[i]
		if tips[a] == tips[b] || !e.Git.IsAncestor(tips[a], tips[b]) {
			return nil, fmt.Errorf("%w: branches %s and %s are not one on top of the other; pass the branches to submit", stackapi.ErrValidation, a, b)
		}
	}
	return branches, nil
}

// trunkName is --base, or the remote's default branch.
func (e *env) trunkName(o submitOpts) (string, error) {
	if o.base != "" {
		return o.base, nil
	}
	return e.Git.DefaultBranch(o.remote)
}

func openBranches(s *github.PullRequestStackDetails) []string {
	var out []string
	for _, p := range s.PullRequests {
		if p.MergedAt == nil && p.State == "open" && p.Head != nil {
			out = append(out, p.Head.Ref)
		}
	}
	return out
}

// changedBranches are the branches whose remote-tracking ref differs from the
// local branch: pushing only those keeps a rerun from triggering push hooks.
func (e *env) changedBranches(remote string, branches []string) ([]string, error) {
	out := []string{}
	for _, b := range branches {
		local, err := e.Git.BranchSHA(b)
		if err != nil {
			return nil, err
		}
		if local == "" {
			return nil, fmt.Errorf("%w: branch %s does not exist locally", stackapi.ErrNotFound, b)
		}
		tracked, err := e.Git.RefSHA("refs/remotes/" + remote + "/" + b)
		if err != nil {
			return nil, err
		}
		if tracked != local {
			out = append(out, b)
		}
	}
	return out, nil
}

// trunk is the branch the bottom pull request targets: --base, the base of
// the bottom branch's open pull request, or the remote's default branch.
func (e *env) trunk(ctx context.Context, bottom string, o submitOpts) (string, error) {
	if o.base != "" {
		return o.base, nil
	}
	p, err := e.openPRForHead(ctx, bottom)
	if err != nil {
		return "", err
	}
	if p != nil {
		return p.GetBase().GetRef(), nil
	}
	return e.trunkName(o)
}

// plannedPR is what submit will do for one branch, decided before pushing.
type plannedPR struct {
	branch, base string
	existing     *github.PullRequest
	title, body  string // for a new pull request
}

// planSubmit finds each branch's open pull request, or the title and body a
// new one gets, each based on the branch below. A new pull request without
// a body is refused before anything is pushed.
func (e *env) planSubmit(ctx context.Context, branches []string, trunk string, o submitOpts) ([]plannedPR, error) {
	var plan []plannedPR
	for i, b := range branches {
		p := plannedPR{branch: b, base: trunk}
		upstream := o.remote + "/" + trunk
		if i > 0 {
			p.base, upstream = branches[i-1], branches[i-1]
		}
		var err error
		if p.existing, err = e.openPRForHead(ctx, b); err != nil {
			return nil, err
		}
		if p.existing == nil {
			if p.title, p.body, err = e.message(b, upstream, o); err != nil {
				return nil, err
			}
			if strings.TrimSpace(p.title) == "" || strings.TrimSpace(p.body) == "" {
				return nil, usagef("%s would open a pull request without a description: give its first commit a body, or pass --message %s=FILE", b, b)
			}
		}
		plan = append(plan, p)
	}
	return plan, nil
}

// applySubmit creates the new pull requests and retargets the misbased ones.
func (e *env) applySubmit(ctx context.Context, plan []plannedPR, o submitOpts) ([]submittedPR, error) {
	var out []submittedPR
	for _, p := range plan {
		sp, err := e.applyOne(ctx, p, o)
		if err != nil {
			return nil, err
		}
		out = append(out, sp)
	}
	return out, nil
}

func (e *env) applyOne(ctx context.Context, p plannedPR, o submitOpts) (submittedPR, error) {
	sp := submittedPR{Branch: p.branch, Base: p.base}
	if p.existing != nil {
		sp.Number = p.existing.GetNumber()
		if p.existing.GetBase().GetRef() == p.base {
			sp.Action = actionUnchanged
			return sp, nil
		}
		sp.Action = dry("retargeted", o.dryRun)
		if o.dryRun {
			return sp, nil
		}
		_, _, err := e.PRs.Edit(ctx, e.owner, e.repo, sp.Number, &github.PullRequest{Base: &github.PullRequestBranch{Ref: github.Ptr(p.base)}})
		return sp, stackapi.Classify(err)
	}
	sp.Action = dry(actionCreated, o.dryRun)
	if o.dryRun {
		return sp, nil
	}
	created, _, err := e.PRs.Create(ctx, e.owner, e.repo, github.CreatePullRequest{
		Title: github.Ptr(p.title), Body: github.Ptr(p.body), Head: p.branch, Base: p.base, Draft: github.Ptr(o.draft),
	})
	if err != nil {
		return sp, stackapi.Classify(err)
	}
	sp.Number = created.GetNumber()
	return sp, nil
}

// message is the new pull request's title and body: --message's file, else
// the branch's first commit.
func (e *env) message(branch, upstream string, o submitOpts) (title, body string, err error) {
	file, ok := o.messages[branch]
	if !ok {
		return e.Git.FirstCommitMessage(upstream, branch)
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		return "", "", err
	}
	title, body, _ = strings.Cut(strings.TrimSpace(string(raw)), "\n")
	return title, strings.TrimSpace(body), nil
}

// contains0 reports a pull request a dry run has not created yet.
func contains0(prs []int) bool {
	for _, n := range prs {
		if n == 0 {
			return true
		}
	}
	return false
}

func contains(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

func printSubmit(w io.Writer, r submitResult, o submitOpts) {
	verb := "pushed"
	if o.dryRun {
		verb = "would-push"
	}
	if len(r.Pushed) == 0 {
		fmt.Fprintf(w, "nothing to push: %s matches every branch\n", o.remote)
	} else {
		fmt.Fprintf(w, "%s to %s: %s\n", verb, o.remote, strings.Join(r.Pushed, " "))
	}
	for _, p := range r.PullRequests {
		num := "new"
		if p.Number != 0 {
			num = fmt.Sprintf("#%d", p.Number)
		}
		fmt.Fprintf(w, "  %s %s (%s → %s)\n", p.Action, num, p.Branch, p.Base)
	}
	if r.Stack != nil {
		printLink(w, *r.Stack)
	}
}
