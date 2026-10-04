package cli

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/google/go-github/v92/github"
	"github.com/manz/gh-stack-extension/internal/stackapi"
)

func init() {
	register("status", command{usage: "compare local branches with the stack: STACK, --pr N, or the current branch's", setup: stackFlags(runStatus)})
	register("restack", command{usage: "rebase the stack's branches onto its base in one pass", setup: stackFlags(runRestack)})
	register("push", command{usage: "push the stack's branches that differ from GitHub", setup: stackFlags(runPush)})
}

type stackOpts struct {
	pr     int
	remote string
	dryRun bool
}

func stackFlags(run func(e *env, args []string, o stackOpts) error) func(*flag.FlagSet) func(*env, []string) error {
	return func(fs *flag.FlagSet) func(*env, []string) error {
		pr := fs.Int("pr", 0, "the stack containing this pull request")
		remote := fs.String("remote", "origin", "git remote the branches live on")
		dry := fs.Bool("dry-run", false, "print what would change, change nothing")
		return func(e *env, args []string) error {
			return run(e, args, stackOpts{pr: *pr, remote: *remote, dryRun: *dry})
		}
	}
}

// Branch sync states, local branch against the stack's head on GitHub.
const (
	syncInSync   = "in_sync"
	syncAhead    = "ahead"    // local has commits GitHub lacks: push
	syncBehind   = "behind"   // GitHub has commits the branch lacks
	syncDiverged = "diverged" // both moved: restack or push --force-with-lease
	syncMissing  = "missing"  // no local branch
	syncUnknown  = "unknown"  // GitHub's commit is not fetched
)

// layer is one pull request of a stack seen from the local repository.
type layer struct {
	Number         int    `json:"number"`
	Branch         string `json:"branch"`
	Parent         string `json:"parent"`
	LocalSHA       string `json:"local_sha"`
	RemoteSHA      string `json:"remote_sha"`
	Sync           string `json:"sync"`
	NeedsRestack   bool   `json:"needs_restack"`
	MergedOrClosed bool   `json:"merged_or_closed"`
}

type statusResult struct {
	Stack  int     `json:"stack"`
	Base   string  `json:"base"`
	Layers []layer `json:"layers"`
}

func (e *env) loadStack(args []string, o stackOpts) (*github.PullRequestStackDetails, error) {
	number, err := e.stackNumber(args, o.pr)
	if err != nil {
		return nil, err
	}
	s, _, err := e.PRs.GetStack(e.ctx, e.owner, e.repo, number)
	return s, stackapi.Classify(err)
}

// layers compares each open pull request's branch with GitHub. The parent of
// the bottom layer is the remote-tracking base branch.
func (e *env) layers(s *github.PullRequestStackDetails, remote string) ([]layer, error) {
	base := ""
	if s.Base != nil {
		base = s.Base.Ref
	}
	parentSHA, err := e.Git.RefSHA("refs/remotes/" + remote + "/" + base)
	if err != nil {
		return nil, err
	}
	parent := remote + "/" + base
	var out []layer
	for _, p := range s.PullRequests {
		l := layer{Number: p.Number, Parent: parent}
		if p.Head != nil {
			l.Branch, l.RemoteSHA = p.Head.Ref, p.Head.SHA
		}
		if p.MergedAt != nil || p.State != "open" {
			l.MergedOrClosed, l.Sync = true, syncInSync
			out = append(out, l)
			continue
		}
		if l.LocalSHA, err = e.Git.BranchSHA(l.Branch); err != nil {
			return nil, err
		}
		l.Sync = e.syncState(l.LocalSHA, l.RemoteSHA)
		l.NeedsRestack = l.LocalSHA != "" && parentSHA != "" && !e.Git.IsAncestor(parentSHA, l.LocalSHA)
		out = append(out, l)
		parent, parentSHA = l.Branch, l.LocalSHA
	}
	return out, nil
}

func (e *env) syncState(local, remote string) string {
	switch {
	case local == "":
		return syncMissing
	case local == remote:
		return syncInSync
	case !e.Git.HasCommit(remote):
		return syncUnknown
	case e.Git.IsAncestor(remote, local):
		return syncAhead
	case e.Git.IsAncestor(local, remote):
		return syncBehind
	}
	return syncDiverged
}

func runStatus(e *env, args []string, o stackOpts) error {
	s, err := e.loadStack(args, o)
	if err != nil {
		return err
	}
	ls, err := e.layers(s, o.remote)
	if err != nil {
		return err
	}
	res := statusResult{Stack: s.Number, Layers: ls}
	if s.Base != nil {
		res.Base = s.Base.Ref
	}
	return e.emit(res, func(w io.Writer) {
		fmt.Fprintf(w, "stack %d onto %s\n", res.Stack, res.Base)
		for _, l := range ls {
			note := l.Sync
			if l.MergedOrClosed {
				note = "merged or closed"
			}
			if l.NeedsRestack {
				note += ", needs restack onto " + l.Parent
			}
			fmt.Fprintf(w, "  #%d %s: %s\n", l.Number, l.Branch, note)
		}
	})
}

type restackResult struct {
	Action   string            `json:"action"`
	Upstream string            `json:"upstream"`
	Top      string            `json:"top"`
	Branches map[string]string `json:"branches"` // branch -> local sha after
}

func runRestack(e *env, args []string, o stackOpts) error {
	s, err := e.loadStack(args, o)
	if err != nil {
		return err
	}
	ls, err := e.layers(s, o.remote)
	if err != nil {
		return err
	}
	var open []layer
	for _, l := range ls {
		if !l.MergedOrClosed {
			open = append(open, l)
		}
	}
	if len(open) == 0 {
		return fmt.Errorf("%w: stack %d has no open pull requests", stackapi.ErrValidation, s.Number)
	}
	res := restackResult{Action: "unchanged", Upstream: open[0].Parent, Top: open[len(open)-1].Branch, Branches: map[string]string{}}
	need := false
	for _, l := range open {
		if l.Sync == syncMissing {
			return fmt.Errorf("%w: branch %s (#%d) is not checked out locally", stackapi.ErrNotFound, l.Branch, l.Number)
		}
		need = need || l.NeedsRestack
	}
	if need {
		res.Action = dry("restacked", o.dryRun)
		if !o.dryRun {
			if err := e.Git.RestackOnto(res.Upstream, res.Top); err != nil {
				return err
			}
		}
	}
	for _, l := range open {
		sha, err := e.Git.BranchSHA(l.Branch)
		if err != nil {
			return err
		}
		res.Branches[l.Branch] = sha
	}
	return e.emit(res, func(w io.Writer) {
		fmt.Fprintf(w, "%s: %s onto %s\n", res.Action, res.Top, res.Upstream)
	})
}

type pushResult struct {
	Action   string   `json:"action"`
	Remote   string   `json:"remote"`
	Branches []string `json:"branches"`
}

func runPush(e *env, args []string, o stackOpts) error {
	s, err := e.loadStack(args, o)
	if err != nil {
		return err
	}
	ls, err := e.layers(s, o.remote)
	if err != nil {
		return err
	}
	res := pushResult{Action: "unchanged", Remote: o.remote, Branches: []string{}}
	for _, l := range ls {
		if l.MergedOrClosed || l.Sync == syncInSync {
			continue
		}
		if l.Sync == syncMissing {
			return fmt.Errorf("%w: branch %s (#%d) is not checked out locally", stackapi.ErrNotFound, l.Branch, l.Number)
		}
		if l.Sync == syncBehind {
			return fmt.Errorf("%w: branch %s (#%d) is behind GitHub; pull it before pushing", stackapi.ErrConflict, l.Branch, l.Number)
		}
		res.Branches = append(res.Branches, l.Branch)
	}
	if len(res.Branches) > 0 {
		res.Action = dry("pushed", o.dryRun)
		if !o.dryRun {
			if err := e.Git.Push(o.remote, res.Branches); err != nil {
				return err
			}
		}
	}
	return e.emit(res, func(w io.Writer) {
		if len(res.Branches) == 0 {
			fmt.Fprintln(w, "unchanged: every branch matches GitHub")
			return
		}
		fmt.Fprintf(w, "%s to %s: %s\n", res.Action, res.Remote, strings.Join(res.Branches, " "))
	})
}
