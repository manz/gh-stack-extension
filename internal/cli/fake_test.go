package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

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
}

func newFake() *fakeGitHub {
	return &fakeGitHub{stacks: map[int]*github.PullRequestStackDetails{}, prs: map[int]*github.PullRequest{}, nextStack: 100, nextPR: 200, fail: map[string]error{}}
}

func apiError(status int, msg string) error {
	return &github.ErrorResponse{Response: &http.Response{StatusCode: status}, Message: msg}
}

func (f *fakeGitHub) record(name string, args ...interface{}) error {
	f.calls = append(f.calls, strings.TrimSpace(name+" "+fmt.Sprint(args...)))
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

type fakeGit struct {
	branch string
	err    error
}

func (g *fakeGit) CurrentBranch() (string, error) { return g.branch, g.err }

// harness runs commands against a fake GitHub and git.
type harness struct {
	gh       *fakeGitHub
	git      *fakeGit
	out, err bytes.Buffer
}

func newHarness() *harness { return &harness{gh: newFake(), git: &fakeGit{branch: "main"}} }

func (h *harness) run(args ...string) int {
	h.out.Reset()
	h.err.Reset()
	return Run(context.Background(), args, Deps{Out: &h.out, Err: &h.err, PRs: h.gh, Git: h.git,
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
