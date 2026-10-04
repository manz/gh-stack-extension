// Command gh-stack-extension is a gh extension for GitHub stacked pull requests.
package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/cli/go-gh/v2/pkg/api"
	"github.com/cli/go-gh/v2/pkg/repository"
	"github.com/manz/gh-stack-extension/internal/cli"
	"github.com/manz/gh-stack-extension/internal/gitx"
	"github.com/manz/gh-stack-extension/internal/stackapi"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, api.DefaultHTTPClient))
}

func run(args []string, stdout, stderr io.Writer, httpClient func() (*http.Client, error)) int {
	hc, err := httpClient()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return cli.ExitError
	}
	gh, err := stackapi.NewGitHub(hc)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return cli.ExitError
	}
	return cli.Run(context.Background(), args, cli.Deps{
		Out: stdout, Err: stderr, PRs: gh.PullRequests, Git: gitx.Repo{}, Repo: resolveRepo,
	})
}

// resolveRepo returns --repo when given, else the current repository.
func resolveRepo(override string) (string, string, error) {
	var r repository.Repository
	var err error
	if override != "" {
		r, err = repository.Parse(override)
	} else {
		r, err = repository.Current()
	}
	return r.Owner, r.Name, err
}
