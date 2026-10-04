// Package cli implements the gh-stack-extension commands. Every command is
// non-interactive, prints JSON with --json, and maps failures to exit codes.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/manz/gh-stack-extension/internal/stackapi"
)

// Exit codes.
const (
	ExitOK          = 0
	ExitError       = 1
	ExitUsage       = 2
	ExitNotFound    = 3
	ExitConflict    = 4
	ExitValidation  = 5
	ExitMergeFailed = 6
)

// Git is the local repository the commands read and change.
type Git interface {
	CurrentBranch() (string, error)
	BranchSHA(branch string) (string, error)
	RefSHA(ref string) (string, error)
	HasCommit(sha string) bool
	IsAncestor(a, b string) bool
	RestackOnto(upstream, top string) error
	Push(remote string, branches []string) error
}

// Deps are the outside world: output streams, GitHub, git and the repository.
type Deps struct {
	Out, Err io.Writer
	PRs      stackapi.Service
	Git      Git
	// Repo resolves owner and name, honouring --repo when set.
	Repo func(override string) (owner, name string, err error)
}

// env is what a command runs with once flags are parsed.
type env struct {
	Deps
	ctx   context.Context
	owner string
	repo  string
	json  bool
}

// command declares its flags in setup, which returns the function that runs
// it once the flags are parsed.
type command struct {
	usage string
	setup func(fs *flag.FlagSet) func(e *env, args []string) error
}

var commands = map[string]command{}

func register(name string, c command) { commands[name] = c }

// noFlags adapts a command without flags of its own.
func noFlags(run func(e *env, args []string) error) func(*flag.FlagSet) func(*env, []string) error {
	return func(*flag.FlagSet) func(*env, []string) error { return run }
}

// usageError is a wrong invocation; it exits with ExitUsage.
type usageError struct{ msg string }

func (u usageError) Error() string { return u.msg }

func usagef(format string, args ...interface{}) error {
	return usageError{fmt.Sprintf(format, args...)}
}

// Run executes one command and returns the process exit code.
func Run(ctx context.Context, args []string, d Deps) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		printUsage(d.Out)
		if len(args) == 0 {
			return ExitUsage
		}
		return ExitOK
	}
	c, ok := commands[args[0]]
	if !ok {
		fmt.Fprintf(d.Err, "unknown command %q\n", args[0])
		printUsage(d.Err)
		return ExitUsage
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(d.Err)
	asJSON := fs.Bool("json", false, "print JSON")
	repoFlag := fs.String("repo", "", "OWNER/REPO (default: the current repository)")
	run := c.setup(fs)
	positional, err := parseInterspersed(fs, args[1:])
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return ExitOK
		}
		return ExitUsage
	}
	owner, name, err := d.Repo(*repoFlag)
	if err != nil {
		fmt.Fprintln(d.Err, err)
		return ExitError
	}
	e := &env{Deps: d, ctx: ctx, owner: owner, repo: name, json: *asJSON}
	if err := run(e, positional); err != nil {
		fmt.Fprintln(d.Err, err)
		return exitCode(err)
	}
	return ExitOK
}

// parseInterspersed parses flags wherever they appear among the positional
// arguments (the flag package stops at the first positional); "--" ends flags.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var positional []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return positional, nil
		}
		if len(args) > len(rest) && args[len(args)-len(rest)-1] == "--" {
			return append(positional, rest...), nil
		}
		positional = append(positional, rest[0])
		args = rest[1:]
	}
}

func exitCode(err error) int {
	var u usageError
	switch {
	case errors.As(err, &u):
		return ExitUsage
	case errors.Is(err, stackapi.ErrNotFound):
		return ExitNotFound
	case errors.Is(err, stackapi.ErrConflict):
		return ExitConflict
	case errors.Is(err, stackapi.ErrValidation):
		return ExitValidation
	case errors.Is(err, errMergeFailed):
		return ExitMergeFailed
	}
	return ExitError
}

var errMergeFailed = errors.New("merge failed")

func printUsage(w io.Writer) {
	names := make([]string, 0, len(commands))
	for n := range commands {
		names = append(names, n)
	}
	sort.Strings(names)
	fmt.Fprintln(w, "usage: gh stack-extension <command> [--json] [--repo OWNER/REPO] [args]")
	for _, n := range names {
		fmt.Fprintf(w, "  %-8s %s\n", n, commands[n].usage)
	}
}

// emit prints v as JSON with --json, or calls text otherwise.
func (e *env) emit(v interface{}, text func(w io.Writer)) error {
	if e.json {
		enc := json.NewEncoder(e.Out)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}
	text(e.Out)
	return nil
}

func joinNumbers(prs []int) string {
	parts := make([]string, len(prs))
	for i, n := range prs {
		parts[i] = fmt.Sprintf("#%d", n)
	}
	return strings.Join(parts, " → ")
}
