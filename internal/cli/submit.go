package cli

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/google/go-github/v92/github"
	"github.com/manz/gh-stack-extension/internal/stackapi"
)

func init() {
	register("submit", command{usage: "push BRANCH... (bottom first, default: the current stack), open or fix their pull requests, link the stack", setup: submitFlags})
}

type submitOpts struct {
	base     string
	remote   string
	draft    bool
	dryRun   bool
	messages map[string]string // branch -> file holding "title\n\nbody"
}

func submitFlags(fs *flag.FlagSet) func(*env, []string) error {
	o := submitOpts{messages: map[string]string{}}
	fs.StringVar(&o.base, "base", "", "branch the bottom pull request targets (default: its current base)")
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
	return func(e *env, args []string) error { return runSubmit(e, args, o) }
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

func runSubmit(e *env, branches []string, o submitOpts) error {
	if len(branches) == 0 {
		var err error
		if branches, err = e.currentBranches(); err != nil {
			return err
		}
	}
	for b := range o.messages {
		if !contains(branches, b) {
			return usagef("--message for %s, which is not submitted", b)
		}
	}
	trunk, err := e.trunk(branches[0], o.base)
	if err != nil {
		return err
	}
	res := submitResult{}
	if res.Pushed, err = e.pushChanged(branches, o); err != nil {
		return err
	}
	if res.PullRequests, err = e.submitAll(branches, trunk, o); err != nil {
		return err
	}
	if res.Stack, err = e.linkSubmitted(res.PullRequests, o.dryRun); err != nil {
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

// submitAll opens or fixes each branch's pull request, each based on the one below.
func (e *env) submitAll(branches []string, trunk string, o submitOpts) ([]submittedPR, error) {
	var out []submittedPR
	for i, b := range branches {
		base, upstream := trunk, o.remote+"/"+trunk
		if i > 0 {
			base, upstream = branches[i-1], branches[i-1]
		}
		sp, err := e.submitOne(b, base, upstream, o)
		if err != nil {
			return nil, err
		}
		out = append(out, sp)
	}
	return out, nil
}

// linkSubmitted links two or more submitted pull requests into one stack.
func (e *env) linkSubmitted(prs []submittedPR, dryRun bool) (*linkResult, error) {
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
	link, err := e.link(nums, dryRun)
	return &link, err
}

// currentBranches is what a bare submit means: the current branch's stack;
// or, for a branch without a pull request, the open stack whose top branch
// it was cut from, plus the branch as a new layer; or the branch alone.
func (e *env) currentBranches() ([]string, error) {
	current, err := e.Git.CurrentBranch()
	if err != nil {
		return nil, err
	}
	p, err := e.openPRForHead(current)
	if err != nil {
		return nil, err
	}
	if p != nil {
		if p.Stack == nil || p.Stack.Number == nil {
			return []string{current}, nil
		}
		s, _, err := e.PRs.GetStack(e.ctx, e.owner, e.repo, *p.Stack.Number)
		if err != nil {
			return nil, stackapi.Classify(err)
		}
		return openBranches(s), nil
	}
	below, err := e.stackBelow(current)
	if err != nil || below == nil {
		return []string{current}, err
	}
	return append(below, current), nil
}

// stackBelow returns the open branches of the stack whose top branch is in
// branch's history, or nil when branch sits on no stack.
func (e *env) stackBelow(branch string) ([]string, error) {
	head, err := e.Git.BranchSHA(branch)
	if err != nil || head == "" {
		return nil, err
	}
	stacks, err := e.listStacks(0)
	if err != nil {
		return nil, err
	}
	for _, m := range stacks {
		if !m.Open {
			continue
		}
		s, _, err := e.PRs.GetStack(e.ctx, e.owner, e.repo, m.Number)
		if err != nil {
			return nil, stackapi.Classify(err)
		}
		open := openBranches(s)
		if len(open) == 0 {
			continue
		}
		top, err := e.Git.BranchSHA(open[len(open)-1])
		if err != nil {
			return nil, err
		}
		if top != "" && e.Git.IsAncestor(top, head) {
			return open, nil
		}
	}
	return nil, nil
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

// trunk is the branch the bottom pull request targets: --base, or the base
// of the bottom branch's open pull request.
func (e *env) trunk(bottom, flagBase string) (string, error) {
	if flagBase != "" {
		return flagBase, nil
	}
	p, err := e.openPRForHead(bottom)
	if err != nil {
		return "", err
	}
	if p == nil {
		return "", usagef("%s has no pull request yet: pass --base", bottom)
	}
	return p.GetBase().GetRef(), nil
}

func (e *env) submitOne(branch, base, upstream string, o submitOpts) (submittedPR, error) {
	sp := submittedPR{Branch: branch, Base: base}
	existing, err := e.openPRForHead(branch)
	if err != nil {
		return sp, err
	}
	if existing != nil {
		sp.Number = existing.GetNumber()
		if existing.GetBase().GetRef() == base {
			sp.Action = actionUnchanged
			return sp, nil
		}
		sp.Action = dry("retargeted", o.dryRun)
		if o.dryRun {
			return sp, nil
		}
		_, _, err := e.PRs.Edit(e.ctx, e.owner, e.repo, sp.Number, &github.PullRequest{Base: &github.PullRequestBranch{Ref: github.Ptr(base)}})
		return sp, stackapi.Classify(err)
	}
	title, body, err := e.message(branch, upstream, o)
	if err != nil {
		return sp, err
	}
	sp.Action = dry(actionCreated, o.dryRun)
	if o.dryRun {
		return sp, nil
	}
	p, _, err := e.PRs.Create(e.ctx, e.owner, e.repo, github.CreatePullRequest{
		Title: github.Ptr(title), Body: github.Ptr(body), Head: branch, Base: base, Draft: github.Ptr(o.draft),
	})
	if err != nil {
		return sp, stackapi.Classify(err)
	}
	sp.Number = p.GetNumber()
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
