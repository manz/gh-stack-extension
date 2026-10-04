package cli

import (
	"flag"
	"fmt"
	"io"
	"strconv"

	"github.com/google/go-github/v92/github"
	"github.com/manz/gh-stack-extension/internal/stackapi"
)

func init() {
	register("list", command{usage: "list the repository's stacks", setup: noFlags(runList)})
	register("view", command{usage: "show a stack: STACK, --pr N, or the current branch's", setup: func(fs *flag.FlagSet) func(*env, []string) error {
		pr := fs.Int("pr", 0, "the stack containing this pull request")
		return func(e *env, args []string) error { return runView(e, args, *pr) }
	}})
}

const pageSize = 100

func runList(e *env, args []string) error {
	if len(args) != 0 {
		return usagef("list takes no arguments")
	}
	stacks, err := e.listStacks(0)
	if err != nil {
		return err
	}
	return e.emit(stacks, func(w io.Writer) {
		for _, s := range stacks {
			printStackLine(w, s.Number, s.Base, s.Open, minimalNumbers(s))
		}
	})
}

// listStacks reads every page of stacks, or only those containing pr.
func (e *env) listStacks(pr int) ([]*github.PullRequestStackMinimal, error) {
	opts := &github.PullRequestListStacksOptions{PullRequest: pr, ListOptions: github.ListOptions{PerPage: pageSize}}
	var all []*github.PullRequestStackMinimal
	for {
		batch, resp, err := e.PRs.ListStacks(e.ctx, e.owner, e.repo, opts)
		if err != nil {
			return nil, stackapi.Classify(err)
		}
		all = append(all, batch...)
		if resp == nil || resp.NextPage == 0 {
			return all, nil
		}
		opts.Page = resp.NextPage
	}
}

func runView(e *env, args []string, pr int) error {
	number, err := e.stackNumber(args, pr)
	if err != nil {
		return err
	}
	s, _, err := e.PRs.GetStack(e.ctx, e.owner, e.repo, number)
	if err != nil {
		return stackapi.Classify(err)
	}
	return e.emit(s, func(w io.Writer) { printStack(w, s) })
}

// stackNumber resolves which stack a command means: an explicit number, the
// stack holding --pr, or the stack holding the current branch's pull request.
func (e *env) stackNumber(args []string, pr int) (int, error) {
	switch {
	case len(args) > 1:
		return 0, usagef("expected at most one stack number")
	case len(args) == 1 && pr != 0:
		return 0, usagef("give a stack number or --pr, not both")
	case len(args) == 1:
		n, err := strconv.Atoi(args[0])
		if err != nil || n <= 0 {
			return 0, usagef("not a stack number: %q", args[0])
		}
		return n, nil
	}
	if pr == 0 {
		branch, err := e.Git.CurrentBranch()
		if err != nil {
			return 0, err
		}
		p, err := e.openPRForHead(branch)
		if err != nil {
			return 0, err
		}
		if p == nil {
			return 0, fmt.Errorf("%w: no open pull request for branch %s", stackapi.ErrNotFound, branch)
		}
		pr = p.GetNumber()
	}
	p, _, err := e.PRs.Get(e.ctx, e.owner, e.repo, pr)
	if err != nil {
		return 0, stackapi.Classify(err)
	}
	if p.Stack == nil || p.Stack.Number == nil {
		return 0, fmt.Errorf("%w: pull request #%d is not in a stack", stackapi.ErrNotFound, pr)
	}
	return *p.Stack.Number, nil
}

// openPRForHead returns the open pull request whose head is branch, or nil.
func (e *env) openPRForHead(branch string) (*github.PullRequest, error) {
	prs, _, err := e.PRs.List(e.ctx, e.owner, e.repo, &github.PullRequestListOptions{State: "open", Head: e.owner + ":" + branch})
	if err != nil {
		return nil, stackapi.Classify(err)
	}
	if len(prs) == 0 {
		return nil, nil
	}
	return prs[0], nil
}

func minimalNumbers(s *github.PullRequestStackMinimal) []int {
	out := make([]int, 0, len(s.PullRequests))
	for _, p := range s.PullRequests {
		out = append(out, p.Number)
	}
	return out
}

func detailNumbers(s *github.PullRequestStackDetails) []int {
	out := make([]int, 0, len(s.PullRequests))
	for _, p := range s.PullRequests {
		out = append(out, p.Number)
	}
	return out
}

func printStackLine(w io.Writer, number int, base *github.PullRequestStackRef, open bool, prs []int) {
	state := "open"
	if !open {
		state = "closed"
	}
	ref := ""
	if base != nil {
		ref = base.Ref
	}
	fmt.Fprintf(w, "stack %d (%s) onto %s: %s\n", number, state, ref, joinNumbers(prs))
}

func printStack(w io.Writer, s *github.PullRequestStackDetails) {
	printStackLine(w, s.Number, s.Base, s.Open, detailNumbers(s))
	for i, p := range s.PullRequests {
		head := ""
		if p.Head != nil {
			head = p.Head.Ref
		}
		state := p.State
		if p.MergedAt != nil {
			state = "merged"
		} else if p.Draft {
			state = "draft"
		}
		fmt.Fprintf(w, "  %d. #%d %s [%s] %s\n", i+1, p.Number, head, state, p.Title)
	}
}
