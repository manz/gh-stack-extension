// Package stackapi is the only place that talks to GitHub: go-github's
// pull request and stack endpoints on gh's authenticated HTTP client.
package stackapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/google/go-github/v92/github"
)

// APIVersion is the REST API version the stacks endpoints are documented for.
const APIVersion = "2026-03-10"

// versionTransport sets APIVersion on every request. go-github always sends
// its own default (2022-11-28) and has no client option to change it.
type versionTransport struct{ next http.RoundTripper }

func (t versionTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set("X-GitHub-Api-Version", APIVersion)
	return t.next.RoundTrip(req)
}

// NewGitHub builds a go-github client on an authenticated http.Client (gh's).
func NewGitHub(hc *http.Client, opts ...github.ClientOptionsFunc) (*github.Client, error) {
	next := hc.Transport
	if next == nil {
		next = http.DefaultTransport
	}
	pinned := *hc
	pinned.Transport = versionTransport{next: next}
	return github.NewClient(append([]github.ClientOptionsFunc{github.WithHTTPClient(&pinned)}, opts...)...)
}

// Service is the part of go-github's PullRequestsService the commands use.
type Service interface {
	ListStacks(ctx context.Context, owner, repo string, opts *github.PullRequestListStacksOptions) ([]*github.PullRequestStackMinimal, *github.Response, error)
	GetStack(ctx context.Context, owner, repo string, number int) (*github.PullRequestStackDetails, *github.Response, error)
	CreateStack(ctx context.Context, owner, repo string, body github.PullRequestCreateStackRequest) (*github.PullRequestStackDetails, *github.Response, error)
	AddToStack(ctx context.Context, owner, repo string, number int, body github.PullRequestAddToStackRequest) (*github.PullRequestStackDetails, *github.Response, error)
	Unstack(ctx context.Context, owner, repo string, number int) (*github.PullRequestStackDetails, *github.Response, error)
	Get(ctx context.Context, owner, repo string, number int) (*github.PullRequest, *github.Response, error)
	List(ctx context.Context, owner, repo string, opts *github.PullRequestListOptions) ([]*github.PullRequest, *github.Response, error)
	Create(ctx context.Context, owner, repo string, body github.CreatePullRequest) (*github.PullRequest, *github.Response, error)
	Edit(ctx context.Context, owner, repo string, number int, pull *github.PullRequest) (*github.PullRequest, *github.Response, error)
}

// Sentinel errors the CLI maps to exit codes; wrapped with GitHub's message.
var (
	ErrNotFound   = errors.New("not found")
	ErrConflict   = errors.New("conflict")
	ErrValidation = errors.New("validation failed")
)

// Classify wraps a go-github error in the sentinel for its status code.
func Classify(err error) error {
	var resp *github.ErrorResponse
	if !errors.As(err, &resp) || resp.Response == nil {
		return err
	}
	var kind error
	switch resp.Response.StatusCode {
	case http.StatusNotFound:
		kind = ErrNotFound
	case http.StatusConflict:
		kind = ErrConflict
	case http.StatusUnprocessableEntity:
		kind = ErrValidation
	default:
		return err
	}
	return fmt.Errorf("%w: %s", kind, resp.Message)
}
