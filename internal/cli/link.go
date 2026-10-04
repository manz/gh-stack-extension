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
	register("create", command{usage: "link pull requests, bottom first, into a stack", setup: withDryRun(runCreate)})
	register("add", command{usage: "put pull requests on top of STACK", setup: withDryRun(runAdd)})
	register("unstack", command{usage: "dissolve STACK (its pull requests stay open)", setup: withDryRun(runUnstack)})
	register("adopt", command{usage: "stack the pull requests chained by base branch around PR or BRANCH (default: current branch)", setup: withDryRun(runAdopt)})
}

func withDryRun(run func(e *env, args []string, dryRun bool) error) func(*flag.FlagSet) func(*env, []string) error {
	return func(fs *flag.FlagSet) func(*env, []string) error {
		dry := fs.Bool("dry-run", false, "print what would change, change nothing")
		return func(e *env, args []string) error { return run(e, args, *dry) }
	}
}

// Link actions; "would-" prefixes them under --dry-run.
const (
	actionCreated   = "created"
	actionAdded     = "added"
	actionUnchanged = "unchanged"
	actionUnstacked = "unstacked"
)

// linkResult is what create, add and adopt report.
type linkResult struct {
	Action       string                          `json:"action"`
	PullRequests []int                           `json:"pull_requests"`
	Added        []int                           `json:"added,omitempty"`
	Stack        *github.PullRequestStackDetails `json:"stack,omitempty"`
}

func dry(action string, dryRun bool) string {
	if dryRun && action != actionUnchanged {
		return "would-" + action
	}
	return action
}

func parsePRs(args []string) ([]int, error) {
	prs := make([]int, 0, len(args))
	seen := map[int]bool{}
	for _, a := range args {
		n, err := strconv.Atoi(a)
		if err != nil || n <= 0 {
			return nil, usagef("not a pull request number: %q", a)
		}
		if seen[n] {
			return nil, usagef("#%d is listed twice", n)
		}
		seen[n] = true
		prs = append(prs, n)
	}
	return prs, nil
}

func runCreate(e *env, args []string, dryRun bool) error {
	prs, err := parsePRs(args)
	if err != nil {
		return err
	}
	if len(prs) < 2 {
		return usagef("a stack needs at least two pull requests")
	}
	return e.linkAndReport(prs, dryRun)
}

func (e *env) linkAndReport(prs []int, dryRun bool) error {
	res, err := e.link(prs, dryRun)
	if err != nil {
		return err
	}
	return e.emit(res, func(w io.Writer) { printLink(w, res) })
}

// link makes prs (bottom first) one stack, doing only what is missing.
func (e *env) link(prs []int, dryRun bool) (linkResult, error) {
	res := linkResult{PullRequests: prs}
	existing, err := e.stackHolding(prs[0])
	if err != nil {
		return res, err
	}
	if existing != nil {
		have := openNumbers(existing)
		if !isPrefix(have, prs) {
			return res, fmt.Errorf("%w: #%d is in stack %d (%s), not the bottom of %s", stackapi.ErrValidation, prs[0], existing.Number, joinNumbers(have), joinNumbers(prs))
		}
		res.Stack = existing
		res.Added = prs[len(have):]
		if len(res.Added) == 0 {
			res.Action = actionUnchanged
			return res, nil
		}
		res.Action = dry(actionAdded, dryRun)
		if dryRun {
			return res, nil
		}
		s, _, err := e.PRs.AddToStack(e.ctx, e.owner, e.repo, existing.Number, github.PullRequestAddToStackRequest{PullRequests: res.Added})
		res.Stack = s
		return res, stackapi.Classify(err)
	}
	res.Action = dry(actionCreated, dryRun)
	if dryRun {
		return res, nil
	}
	s, _, err := e.PRs.CreateStack(e.ctx, e.owner, e.repo, github.PullRequestCreateStackRequest{PullRequests: prs})
	res.Stack = s
	return res, stackapi.Classify(err)
}

// stackHolding returns the stack a pull request is in, or nil.
func (e *env) stackHolding(pr int) (*github.PullRequestStackDetails, error) {
	p, _, err := e.PRs.Get(e.ctx, e.owner, e.repo, pr)
	if err != nil {
		return nil, stackapi.Classify(err)
	}
	if p.Stack == nil || p.Stack.Number == nil {
		return nil, nil
	}
	s, _, err := e.PRs.GetStack(e.ctx, e.owner, e.repo, *p.Stack.Number)
	return s, stackapi.Classify(err)
}

// openNumbers is the stack's layers still open: merged ones stay in the
// stack's history but are no longer part of what gets linked or grown.
func openNumbers(s *github.PullRequestStackDetails) []int {
	out := []int{}
	for _, p := range s.PullRequests {
		if p.MergedAt == nil && p.State == "open" {
			out = append(out, p.Number)
		}
	}
	return out
}

func isPrefix(prefix, of []int) bool {
	if len(prefix) > len(of) {
		return false
	}
	for i := range prefix {
		if prefix[i] != of[i] {
			return false
		}
	}
	return true
}

func printLink(w io.Writer, r linkResult) {
	switch {
	case r.Stack != nil && r.Action == actionUnchanged:
		fmt.Fprintf(w, "unchanged: stack %d is %s\n", r.Stack.Number, joinNumbers(r.PullRequests))
	case r.Stack != nil && len(r.Added) > 0:
		fmt.Fprintf(w, "%s %s to stack %d: %s\n", r.Action, joinNumbers(r.Added), r.Stack.Number, joinNumbers(r.PullRequests))
	case r.Stack != nil:
		fmt.Fprintf(w, "%s stack %d: %s\n", r.Action, r.Stack.Number, joinNumbers(r.PullRequests))
	default:
		fmt.Fprintf(w, "%s stack: %s\n", r.Action, joinNumbers(r.PullRequests))
	}
}

func stackArg(args []string) (int, []string, error) {
	if len(args) == 0 {
		return 0, nil, usagef("missing stack number")
	}
	n, err := strconv.Atoi(args[0])
	if err != nil || n <= 0 {
		return 0, nil, usagef("not a stack number: %q", args[0])
	}
	return n, args[1:], nil
}

func runAdd(e *env, args []string, dryRun bool) error {
	number, rest, err := stackArg(args)
	if err != nil {
		return err
	}
	prs, err := parsePRs(rest)
	if err != nil {
		return err
	}
	if len(prs) == 0 {
		return usagef("missing pull requests to add")
	}
	s, _, err := e.PRs.GetStack(e.ctx, e.owner, e.repo, number)
	if err != nil {
		return stackapi.Classify(err)
	}
	have := openNumbers(s)
	return e.linkAndReport(append(have, withoutTop(have, prs)...), dryRun)
}

// withoutTop drops the leading prs that already sit at the top of have, so
// re-running add with the same arguments is a no-op.
func withoutTop(have, prs []int) []int {
	for k := len(prs); k > 0; k-- {
		if k <= len(have) && isPrefix(prs[:k], have[len(have)-k:]) {
			return prs[k:]
		}
	}
	return prs
}

type unstackResult struct {
	Action       string `json:"action"`
	Stack        int    `json:"stack"`
	PullRequests []int  `json:"pull_requests"`
}

func runUnstack(e *env, args []string, dryRun bool) error {
	number, rest, err := stackArg(args)
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return usagef("unstack takes one stack number")
	}
	s, _, err := e.PRs.GetStack(e.ctx, e.owner, e.repo, number)
	if err != nil {
		return stackapi.Classify(err)
	}
	res := unstackResult{Action: dry(actionUnstacked, dryRun), Stack: number, PullRequests: detailNumbers(s)}
	if !dryRun {
		if _, _, err := e.PRs.Unstack(e.ctx, e.owner, e.repo, number); err != nil {
			return stackapi.Classify(err)
		}
	}
	return e.emit(res, func(w io.Writer) {
		fmt.Fprintf(w, "%s stack %d: %s\n", res.Action, res.Stack, joinNumbers(res.PullRequests))
	})
}

func runAdopt(e *env, args []string, dryRun bool) error {
	if len(args) > 1 {
		return usagef("adopt takes one pull request or branch")
	}
	start, err := e.adoptStart(args)
	if err != nil {
		return err
	}
	chain, err := e.chainAround(start)
	if err != nil {
		return err
	}
	if len(chain) < 2 {
		return fmt.Errorf("%w: #%d has no pull request below or above it to stack with", stackapi.ErrValidation, start.GetNumber())
	}
	return e.linkAndReport(chain, dryRun)
}

// adoptStart resolves a pull request number, a branch, or the current branch.
func (e *env) adoptStart(args []string) (*github.PullRequest, error) {
	if len(args) == 1 {
		if n, err := strconv.Atoi(args[0]); err == nil {
			p, _, err := e.PRs.Get(e.ctx, e.owner, e.repo, n)
			return p, stackapi.Classify(err)
		}
	}
	branch := ""
	if len(args) == 1 {
		branch = args[0]
	} else {
		b, err := e.Git.CurrentBranch()
		if err != nil {
			return nil, err
		}
		branch = b
	}
	p, err := e.openPRForHead(branch)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, fmt.Errorf("%w: no open pull request for branch %s", stackapi.ErrNotFound, branch)
	}
	return p, nil
}

// chainAround follows base branches down and child pull requests up from p,
// returning the chain bottom first.
func (e *env) chainAround(p *github.PullRequest) ([]int, error) {
	var below []int
	for cur := p; ; {
		parent, err := e.openPRForHead(cur.GetBase().GetRef())
		if err != nil {
			return nil, err
		}
		if parent == nil {
			break
		}
		below = append([]int{parent.GetNumber()}, below...)
		cur = parent
	}
	chain := append(below, p.GetNumber())
	for cur := p; ; {
		kids, _, err := e.PRs.List(e.ctx, e.owner, e.repo, &github.PullRequestListOptions{State: "open", Base: cur.GetHead().GetRef()})
		if err != nil {
			return nil, stackapi.Classify(err)
		}
		if len(kids) == 0 {
			return chain, nil
		}
		if len(kids) > 1 {
			nums := make([]int, len(kids))
			for i, k := range kids {
				nums[i] = k.GetNumber()
			}
			return nil, fmt.Errorf("%w: branch %s has several pull requests on top (%s); adopt the one to follow", stackapi.ErrValidation, cur.GetHead().GetRef(), joinNumbers(nums))
		}
		chain = append(chain, kids[0].GetNumber())
		cur = kids[0]
	}
}
