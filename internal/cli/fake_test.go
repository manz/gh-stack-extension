package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/go-github/v92/github"
)

// fakeGitHub keeps stacks and pull requests in memory, like the API would.
type fakeGitHub struct {
	stacks    map[int]*github.PullRequestStackDetails
	prs       map[int]*github.PullRequest
	nextStack int
	nextPR    int
	calls     []string
	fail      map[string]error // by call name
	pageSize  int              // ListStacks page size; 0 = all at once
	merges    []string         // statuses MergeAsync then each poll return
	mergeMsg  string
	accept    bool   // answer like GitHub: HTTP 202 (AcceptedError)
	acceptRaw string // body to put in the AcceptedError, if not the result
}

func newFake() *fakeGitHub {
	return &fakeGitHub{stacks: map[int]*github.PullRequestStackDetails{}, prs: map[int]*github.PullRequest{}, nextStack: 100, nextPR: 200, fail: map[string]error{}}
}

func apiError(status int, msg string) error {
	return &github.ErrorResponse{Response: &http.Response{StatusCode: status}, Message: msg}
}

func (f *fakeGitHub) record(name string, args ...any) error {
	f.calls = append(f.calls, strings.TrimSuffix(name+" "+fmt.Sprintln(args...), "\n"))
	return f.fail[name]
}

// addPR registers an open pull request head -> base.
func (f *fakeGitHub) addPR(number int, head, base string) *github.PullRequest {
	pr := &github.PullRequest{Number: github.Ptr(number), State: github.Ptr("open"), Title: github.Ptr("PR " + head),
		Head: &github.PullRequestBranch{Ref: github.Ptr(head), SHA: github.Ptr("sha-" + head)},
		Base: &github.PullRequestBranch{Ref: github.Ptr(base)}}
	f.prs[number] = pr
	return pr
}

func (f *fakeGitHub) stackOf(pr int) *github.PullRequestStackDetails {
	for _, s := range f.stacks {
		for _, e := range s.PullRequests {
			if e.Number == pr {
				return s
			}
		}
	}
	return nil
}

func (f *fakeGitHub) setStack(number int, prs []int) *github.PullRequestStackDetails {
	s := f.stacks[number]
	if s == nil {
		s = &github.PullRequestStackDetails{Number: number, Open: true}
		f.stacks[number] = s
	}
	s.PullRequests = nil
	for _, n := range prs {
		pr := f.prs[n]
		s.PullRequests = append(s.PullRequests, &github.PullRequestStackEntry{Number: n, State: "open", Title: pr.GetTitle(),
			Head: &github.PullRequestStackBranch{Ref: pr.GetHead().GetRef(), SHA: pr.GetHead().GetSHA()}})
	}
	bottom := f.prs[prs[0]].GetBase().GetRef()
	s.Base = &github.PullRequestStackRef{Ref: bottom}
	for i, n := range prs {
		f.prs[n].Stack = &github.PullRequestStack{Number: github.Ptr(number), Position: github.Ptr(i + 1), Size: github.Ptr(len(prs)),
			Base: &github.PullRequestStackBase{Ref: bottom}}
	}
	return s
}

func minimal(s *github.PullRequestStackDetails) *github.PullRequestStackMinimal {
	m := &github.PullRequestStackMinimal{Number: s.Number, Open: s.Open, Base: s.Base}
	for _, e := range s.PullRequests {
		m.PullRequests = append(m.PullRequests, &github.PullRequestStackMinimalEntry{Number: e.Number, State: e.State,
			Head: &github.PullRequestStackMinimalHead{Ref: e.Head.Ref, SHA: e.Head.SHA}})
	}
	return m
}

func (f *fakeGitHub) ListStacks(_ context.Context, _, _ string, o *github.PullRequestListStacksOptions) ([]*github.PullRequestStackMinimal, *github.Response, error) {
	if err := f.record("ListStacks", o.PullRequest, o.Page); err != nil {
		return nil, nil, err
	}
	var nums []int
	for n, s := range f.stacks {
		if o.PullRequest == 0 || f.stackOf(o.PullRequest) == s {
			nums = append(nums, n)
		}
	}
	sort.Ints(nums)
	var all []*github.PullRequestStackMinimal
	for _, n := range nums {
		all = append(all, minimal(f.stacks[n]))
	}
	resp := &github.Response{}
	if f.pageSize > 0 {
		start := (max(o.Page, 1) - 1) * f.pageSize
		end := min(start+f.pageSize, len(all))
		if end < len(all) {
			resp.NextPage = max(o.Page, 1) + 1
		}
		all = all[start:end]
	}
	return all, resp, nil
}

func (f *fakeGitHub) GetStack(_ context.Context, _, _ string, n int) (*github.PullRequestStackDetails, *github.Response, error) {
	if err := f.record("GetStack", n); err != nil {
		return nil, nil, err
	}
	s := f.stacks[n]
	if s == nil {
		return nil, nil, apiError(404, "Not Found")
	}
	return s, nil, nil
}

func (f *fakeGitHub) CreateStack(_ context.Context, _, _ string, b github.PullRequestCreateStackRequest) (*github.PullRequestStackDetails, *github.Response, error) {
	if err := f.record("CreateStack", b.PullRequests); err != nil {
		return nil, nil, err
	}
	for _, n := range b.PullRequests {
		if f.stackOf(n) != nil {
			return nil, nil, apiError(422, fmt.Sprintf("#%d is already in a stack", n))
		}
	}
	f.nextStack++
	return f.setStack(f.nextStack, b.PullRequests), nil, nil
}

func (f *fakeGitHub) AddToStack(_ context.Context, _, _ string, n int, b github.PullRequestAddToStackRequest) (*github.PullRequestStackDetails, *github.Response, error) {
	if err := f.record("AddToStack", n, b.PullRequests); err != nil {
		return nil, nil, err
	}
	s := f.stacks[n]
	if s == nil {
		return nil, nil, apiError(404, "Not Found")
	}
	return f.setStack(n, append(detailNumbers(s), b.PullRequests...)), nil, nil
}

func (f *fakeGitHub) Unstack(_ context.Context, _, _ string, n int) (*github.PullRequestStackDetails, *github.Response, error) {
	if err := f.record("Unstack", n); err != nil {
		return nil, nil, err
	}
	s := f.stacks[n]
	if s == nil {
		return nil, nil, apiError(404, "Not Found")
	}
	for _, e := range s.PullRequests {
		f.prs[e.Number].Stack = nil
	}
	delete(f.stacks, n)
	return s, nil, nil
}

func (f *fakeGitHub) Get(_ context.Context, _, _ string, n int) (*github.PullRequest, *github.Response, error) {
	if err := f.record("Get", n); err != nil {
		return nil, nil, err
	}
	pr := f.prs[n]
	if pr == nil {
		return nil, nil, apiError(404, "Not Found")
	}
	return pr, nil, nil
}

func (f *fakeGitHub) List(_ context.Context, owner, _ string, o *github.PullRequestListOptions) ([]*github.PullRequest, *github.Response, error) {
	if err := f.record("List", o.Head, o.Base); err != nil {
		return nil, nil, err
	}
	var nums []int
	for n, pr := range f.prs {
		headOK := o.Head == "" || o.Head == owner+":"+pr.GetHead().GetRef()
		baseOK := o.Base == "" || o.Base == pr.GetBase().GetRef()
		if headOK && baseOK && pr.GetState() == "open" {
			nums = append(nums, n)
		}
	}
	sort.Ints(nums)
	var out []*github.PullRequest
	for _, n := range nums {
		out = append(out, f.prs[n])
	}
	return out, nil, nil
}

// fakeGit is a commit graph (each commit knows its parent) plus refs.
type fakeGit struct {
	branch    string
	err       error
	parents   map[string]string // sha -> parent sha
	refs      map[string]string // "refs/heads/x", "refs/remotes/origin/x" -> sha
	restacks  []string
	pushes    []string
	checkouts []string
	failOp    map[string]error
}

func (g *fakeGit) CurrentBranch() (string, error) { return g.branch, g.err }

func (g *fakeGit) BranchSHA(b string) (string, error) {
	return g.refs["refs/heads/"+b], g.failOp["BranchSHA"]
}

func (g *fakeGit) RefSHA(ref string) (string, error) { return g.refs[ref], g.failOp["RefSHA"] }

func (g *fakeGit) HasCommit(sha string) bool {
	_, ok := g.parents[sha]
	return ok
}

func (g *fakeGit) IsAncestor(a, b string) bool {
	for cur, ok := b, true; ok; cur, ok = g.parents[cur] {
		if cur == a {
			return true
		}
		if cur == "" {
			return false
		}
	}
	return false
}

// commit adds sha on top of parent ("" for a root).
func (g *fakeGit) commit(sha, parent string) { g.parents[sha] = parent }

// RebaseOnto moves branch to a new commit sitting on onto's tip.
func (g *fakeGit) RebaseOnto(onto, oldBase, branch string) error {
	g.restacks = append(g.restacks, onto+" "+oldBase+" "+branch)
	if err := g.failOp["RebaseOnto"]; err != nil {
		return err
	}
	tip := g.refs["refs/heads/"+onto]
	if tip == "" {
		tip = g.refs["refs/remotes/"+onto]
	}
	moved := g.refs["refs/heads/"+branch] + "'"
	g.commit(moved, tip)
	g.refs["refs/heads/"+branch] = moved
	return nil
}

func (g *fakeGit) MergeBase(a, b string) (string, error) {
	if err := g.failOp["MergeBase"]; err != nil {
		return "", err
	}
	for x, ok := a, true; ok && x != ""; x, ok = g.parents[x] {
		if g.IsAncestor(x, b) {
			return x, nil
		}
	}
	return "", nil
}

func (g *fakeGit) Checkout(branch string) error {
	g.checkouts = append(g.checkouts, branch)
	return g.failOp["Checkout"]
}

func (g *fakeGit) Push(remote string, branches []string) error {
	g.pushes = append(g.pushes, remote+" "+strings.Join(branches, " "))
	return g.failOp["Push"]
}

// harness runs commands against a fake GitHub and git.
type harness struct {
	gh       *fakeGitHub
	git      *fakeGit
	out, err bytes.Buffer
	slept    []time.Duration
}

func (h *harness) sleep(d time.Duration) { h.slept = append(h.slept, d) }

func newHarness() *harness {
	return &harness{gh: newFake(), git: &fakeGit{branch: "main", parents: map[string]string{}, refs: map[string]string{}, failOp: map[string]error{}}}
}

func (h *harness) run(args ...string) int {
	h.out.Reset()
	h.err.Reset()
	return Run(context.Background(), args, Deps{Out: &h.out, Err: &h.err, PRs: h.gh, Git: h.git, Sleep: h.sleep,
		Repo: func(override string) (string, string, error) {
			if override == "bad" {
				return "", "", errors.New("bad repo")
			}
			return "manz", "ff4", nil
		}})
}

// chain registers open pull requests 1..n stacked on main: b1 <- b2 <- ...
func (h *harness) chain(n int) {
	base := "main"
	for i := 1; i <= n; i++ {
		head := fmt.Sprintf("b%d", i)
		h.gh.addPR(i, head, base)
		base = head
	}
}

func apiErrorClassified(status int) error {
	return classifyForTest(apiError(status, "x"))
}

func (f *fakeGitHub) Create(_ context.Context, _, _ string, b github.CreatePullRequest) (*github.PullRequest, *github.Response, error) {
	if err := f.record("Create", b.Head, b.Base, b.GetTitle(), b.GetDraft()); err != nil {
		return nil, nil, err
	}
	f.nextPR++
	pr := f.addPR(f.nextPR, b.Head, b.Base)
	pr.Title, pr.Body, pr.Draft = b.Title, b.Body, b.Draft
	return pr, nil, nil
}

func (f *fakeGitHub) Edit(_ context.Context, _, _ string, n int, p *github.PullRequest) (*github.PullRequest, *github.Response, error) {
	if err := f.record("Edit", n, p.GetBase().GetRef()); err != nil {
		return nil, nil, err
	}
	f.prs[n].Base = &github.PullRequestBranch{Ref: github.Ptr(p.GetBase().GetRef())}
	return f.prs[n], nil, nil
}

func (g *fakeGit) FirstCommitMessage(upstream, branch string) (subject, body string, err error) {
	if err := g.failOp["FirstCommitMessage"]; err != nil {
		return "", "", err
	}
	return "Subject of " + branch, "Body of " + branch + " on " + upstream, nil
}

// local creates branches with fresh commits that the remote has not seen.
func (h *harness) local(branches ...string) {
	for _, b := range branches {
		h.git.commit("local-"+b, "")
		h.git.refs["refs/heads/"+b] = "local-" + b
	}
}

func (f *fakeGitHub) mergeResult() *github.PullRequestMergeAsyncResult {
	status := "pending"
	if len(f.merges) > 0 {
		status, f.merges = f.merges[0], f.merges[1:]
	}
	return &github.PullRequestMergeAsyncResult{Status: github.Ptr(status),
		Details: &github.PullRequestMergeAsyncDetails{UUID: github.Ptr("u-1"), Message: github.Ptr(f.mergeMsg)}}
}

func (f *fakeGitHub) MergeAsync(_ context.Context, _, _ string, n int, b github.PullRequestMergeAsyncRequest) (*github.PullRequestMergeAsyncResult, *github.Response, error) {
	if err := f.record("MergeAsync", n, b.GetMergeMethod(), b.GetMergeAction()); err != nil {
		return nil, nil, err
	}
	return f.answer()
}

func (f *fakeGitHub) GetMergeAsyncResult(_ context.Context, _, _ string, n int, uuid string) (*github.PullRequestMergeAsyncResult, *github.Response, error) {
	if err := f.record("GetMergeAsyncResult", n, uuid); err != nil {
		return nil, nil, err
	}
	return f.answer()
}

func (f *fakeGitHub) answer() (*github.PullRequestMergeAsyncResult, *github.Response, error) {
	r := f.mergeResult()
	// GitHub answers a pending merge with HTTP 202, which go-github returns
	// as an AcceptedError; a settled one with 200.
	if !f.accept && r.GetStatus() != statusPending {
		return r, nil, nil
	}
	raw, _ := json.Marshal(r)
	if f.acceptRaw != "" {
		raw = []byte(f.acceptRaw)
	}
	return nil, nil, &github.AcceptedError{Raw: raw}
}
