// Command gh-stack-extension is a gh extension for GitHub stacked pull requests.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/cli/go-gh/v2/pkg/api"
)

// restClient is the part of go-gh's REST client the commands use.
type restClient interface {
	Get(path string, response interface{}) error
}

func newRESTClient() (restClient, error) {
	return api.DefaultRESTClient()
}

func main() {
	os.Exit(run(os.Stdout, os.Stderr, newRESTClient))
}

// run prints who the extension runs as and returns the process exit code.
func run(stdout, stderr io.Writer, newClient func() (restClient, error)) int {
	client, err := newClient()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	var user struct{ Login string }
	if err := client.Get("user", &user); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "running as %s\n", user.Login)
	return 0
}
