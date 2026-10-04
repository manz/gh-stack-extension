package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/google/go-github/v92/github"
	"github.com/manz/gh-stack-extension/internal/stackapi"
)

func init() {
	register("merge", command{usage: "merge or queue PR (default: the current branch's) and every stack layer below it", setup: mergeFlags})
}

type mergeOpts struct {
	method   string
	queue    bool
	direct   bool
	wait     bool
	interval time.Duration
	timeout  time.Duration
	dryRun   bool
}

func mergeFlags(fs *flag.FlagSet) func(*env, []string) error {
	var o mergeOpts
	fs.StringVar(&o.method, "method", "", "merge, squash or rebase (default: the repository's)")
	fs.BoolVar(&o.queue, "queue", false, "add to the merge queue")
	fs.BoolVar(&o.direct, "direct", false, "merge directly, bypassing the merge queue")
	fs.BoolVar(&o.wait, "wait", false, "poll until merged, enqueued or failed")
	fs.DurationVar(&o.interval, "interval", 5*time.Second, "poll interval with --wait")
	fs.DurationVar(&o.timeout, "timeout", 15*time.Minute, "give up waiting after this long")
	fs.BoolVar(&o.dryRun, "dry-run", false, "print what would merge, merge nothing")
	return func(e *env, args []string) error { return runMerge(e, args, o) }
}

// Async merge statuses.
const (
	statusPending  = "pending"
	statusMerged   = "merged"
	statusEnqueued = "enqueued"
	statusFailed   = "failed"
)

type mergeResult struct {
	Status       string `json:"status"`
	UUID         string `json:"uuid,omitempty"`
	Message      string `json:"message,omitempty"`
	PullRequest  int    `json:"pull_request"`
	PullRequests []int  `json:"pull_requests"` // what lands, bottom first
}

func runMerge(e *env, args []string, o mergeOpts) error {
	req, err := o.request()
	if err != nil {
		return err
	}
	res, err := e.mergePlan(args)
	if err != nil {
		return err
	}
	if o.dryRun {
		res.Status = "would-merge"
		return e.emit(res, func(w io.Writer) { fmt.Fprintf(w, "would merge %s\n", joinNumbers(res.PullRequests)) })
	}
	r, err := accepted(e.PRs.MergeAsync(e.ctx, e.owner, e.repo, res.PullRequest, req))
	if err != nil {
		return stackapi.Classify(err)
	}
	res.fill(r)
	if o.wait {
		if err := e.waitForMerge(&res, o); err != nil {
			return err
		}
	}
	if err := e.emit(res, func(w io.Writer) { printMerge(w, res) }); err != nil {
		return err
	}
	if res.Status == statusFailed {
		return fmt.Errorf("%w: %s", errMergeFailed, res.Message)
	}
	return nil
}

// mergePlan is the pull request to merge and every open layer that lands with it.
func (e *env) mergePlan(args []string) (mergeResult, error) {
	pr, err := e.mergeTarget(args)
	if err != nil {
		return mergeResult{}, err
	}
	res := mergeResult{PullRequest: pr.GetNumber(), PullRequests: []int{pr.GetNumber()}}
	if pr.Stack == nil || pr.Stack.Number == nil {
		return res, nil
	}
	s, _, err := e.PRs.GetStack(e.ctx, e.owner, e.repo, *pr.Stack.Number)
	if err != nil {
		return res, stackapi.Classify(err)
	}
	res.PullRequests = upTo(openNumbers(s), pr.GetNumber())
	return res, nil
}

// waitForMerge polls until the merge request leaves pending or times out.
func (e *env) waitForMerge(res *mergeResult, o mergeOpts) error {
	deadline := time.Now().Add(o.timeout)
	for res.Status == statusPending {
		if time.Now().After(deadline) {
			return fmt.Errorf("still pending after %s: merge request %s", o.timeout, res.UUID)
		}
		e.sleep(o.interval)
		r, err := accepted(e.PRs.GetMergeAsyncResult(e.ctx, e.owner, e.repo, res.PullRequest, res.UUID))
		if err != nil {
			return stackapi.Classify(err)
		}
		res.fill(r)
	}
	return nil
}

// accepted turns go-github's AcceptedError (HTTP 202, the normal answer to
// an async merge) back into the merge result its body carries.
func accepted(r *github.PullRequestMergeAsyncResult, _ *github.Response, err error) (*github.PullRequestMergeAsyncResult, error) {
	var acc *github.AcceptedError
	if !errors.As(err, &acc) {
		return r, err
	}
	r = &github.PullRequestMergeAsyncResult{}
	if jsonErr := json.Unmarshal(acc.Raw, r); jsonErr != nil {
		return nil, fmt.Errorf("reading the accepted merge request: %w", jsonErr)
	}
	return r, nil
}

func printMerge(w io.Writer, res mergeResult) {
	fmt.Fprintf(w, "%s: %s", res.Status, joinNumbers(res.PullRequests))
	if res.Message != "" {
		fmt.Fprintf(w, " (%s)", res.Message)
	}
	fmt.Fprintln(w)
}

func (o mergeOpts) request() (github.PullRequestMergeAsyncRequest, error) {
	var req github.PullRequestMergeAsyncRequest
	switch o.method {
	case "":
	case "merge", "squash", "rebase":
		req.MergeMethod = github.Ptr(o.method)
	default:
		return req, usagef("--method must be merge, squash or rebase")
	}
	switch {
	case o.queue && o.direct:
		return req, usagef("--queue and --direct are exclusive")
	case o.queue:
		req.MergeAction = github.Ptr("merge_queue")
	case o.direct:
		req.MergeAction = github.Ptr("direct_merge")
	}
	return req, nil
}

func (r *mergeResult) fill(m *github.PullRequestMergeAsyncResult) {
	r.Status = m.GetStatus()
	if d := m.Details; d != nil {
		if d.UUID != nil {
			r.UUID = *d.UUID
		}
		if d.Message != nil {
			r.Message = *d.Message
		}
	}
}

func (e *env) sleep(d time.Duration) {
	if e.Sleep != nil {
		e.Sleep(d)
		return
	}
	time.Sleep(d)
}

// mergeTarget is the pull request named by number, or the current branch's.
func (e *env) mergeTarget(args []string) (*github.PullRequest, error) {
	switch len(args) {
	case 0:
		branch, err := e.Git.CurrentBranch()
		if err != nil {
			return nil, err
		}
		p, err := e.openPRForHead(branch)
		if err != nil {
			return nil, err
		}
		if p == nil {
			return nil, fmt.Errorf("%w: no open pull request for branch %s", stackapi.ErrNotFound, branch)
		}
		p, _, err = e.PRs.Get(e.ctx, e.owner, e.repo, p.GetNumber())
		return p, stackapi.Classify(err)
	case 1:
		n, err := strconv.Atoi(args[0])
		if err != nil || n <= 0 {
			return nil, usagef("not a pull request number: %q", args[0])
		}
		p, _, err := e.PRs.Get(e.ctx, e.owner, e.repo, n)
		return p, stackapi.Classify(err)
	}
	return nil, usagef("merge takes at most one pull request")
}

// upTo is prs from the bottom through pr.
func upTo(prs []int, pr int) []int {
	for i, n := range prs {
		if n == pr {
			return prs[:i+1]
		}
	}
	return []int{pr}
}
