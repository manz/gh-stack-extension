package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/google/go-github/v92/github"
	"github.com/manz/gh-stack-extension/internal/stackapi"
)

func init() {
	register("status", command{usage: "compare local branches with the stack: STACK, --pr N, or the current branch's", setup: stackFlags(runStatus)})
	register("restack", command{usage: "replay each layer onto its parent, bottom up", setup: stackFlags(runRestack)})
	register("push", command{usage: "push the stack's branches that differ from GitHub", setup: stackFlags(runPush)})
}

type stackOpts struct {
	pr     int
	remote string
	dryRun bool
}

func stackFlags(run func(ctx context.Context, e *env, args []string, o stackOpts) error) func(*flag.FlagSet) func(context.Context, *env, []string) error {
	return func(fs *flag.FlagSet) func(context.Context, *env, []string) error {
		pr := fs.Int("pr", 0, "the stack containing this pull request")
		remote := fs.String("remote", "origin", "git remote the branches live on")
		dry := fs.Bool("dry-run", false, "print what would change, change nothing")
		return func(ctx context.Context, e *env, args []string) error {
			return run(ctx, e, args, stackOpts{pr: *pr, remote: *remote, dryRun: *dry})
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

func (e *env) loadStack(ctx context.Context, args []string, o stackOpts) (*github.PullRequestStackDetails, error) {
	number, err := e.stackNumber(ctx, args, o.pr)
	if err != nil {
		return nil, err
	}
	s, _, err := e.PRs.GetStack(ctx, e.owner, e.repo, number)
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

func runStatus(ctx context.Context, e *env, args []string, o stackOpts) error {
	s, err := e.loadStack(ctx, args, o)
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
	Action    string            `json:"action"`
	Restacked []string          `json:"restacked"` // bottom first
	Branches  map[string]string `json:"branches"`  // branch -> local sha after
}

// runRestack walks the open layers bottom up. A layer whose parent's tip is
// not in its history gets its own commits replayed onto the parent: the old
// parent tip is GitHub's head of the layer below (what it was built on), or
// the merge-base for the bottom layer. Fixing a middle layer so carries up.
func runRestack(ctx context.Context, e *env, args []string, o stackOpts) error {
	open, err := e.openLayers(ctx, args, o)
	if err != nil {
		return err
	}
	current, err := e.Git.CurrentBranch()
	if err != nil {
		return err
	}
	res := restackResult{Action: actionUnchanged, Restacked: []string{}, Branches: map[string]string{}}
	moving := false // under --dry-run, everything above a moved layer moves too
	for i, l := range open {
		moved, err := e.restackLayer(open, i, moving, o.dryRun)
		if err != nil {
			return err
		}
		if moved {
			res.Restacked = append(res.Restacked, l.Branch)
			moving = o.dryRun
		}
		if res.Branches[l.Branch], err = e.Git.BranchSHA(l.Branch); err != nil {
			return err
		}
	}
	if len(res.Restacked) > 0 {
		res.Action = dry("restacked", o.dryRun)
		if !o.dryRun {
			if err := e.Git.Checkout(current); err != nil {
				return err
			}
		}
	}
	return e.emit(res, func(w io.Writer) { printRestack(w, res) })
}

// openLayers is the stack's open layers, all checked out locally.
func (e *env) openLayers(ctx context.Context, args []string, o stackOpts) ([]layer, error) {
	s, err := e.loadStack(ctx, args, o)
	if err != nil {
		return nil, err
	}
	ls, err := e.layers(s, o.remote)
	if err != nil {
		return nil, err
	}
	var open []layer
	for _, l := range ls {
		if l.MergedOrClosed {
			continue
		}
		if l.Sync == syncMissing {
			return nil, fmt.Errorf("%w: branch %s (#%d) is not checked out locally", stackapi.ErrNotFound, l.Branch, l.Number)
		}
		open = append(open, l)
	}
	if len(open) == 0 {
		return nil, fmt.Errorf("%w: stack %d has no open pull requests", stackapi.ErrValidation, s.Number)
	}
	return open, nil
}

// restackLayer replays layer i onto its parent when the parent's tip is not
// in its history (or, under --dry-run, when a layer below would move).
func (e *env) restackLayer(open []layer, i int, moving, dryRun bool) (bool, error) {
	parent, parentSHA, oldBase, err := e.parentOf(open, i)
	if err != nil {
		return false, err
	}
	local, err := e.Git.BranchSHA(open[i].Branch)
	if err != nil {
		return false, err
	}
	if !moving && (parentSHA == "" || e.Git.IsAncestor(parentSHA, local)) {
		return false, nil
	}
	if dryRun {
		return true, nil
	}
	if oldBase == "" || !e.Git.HasCommit(oldBase) || !e.Git.IsAncestor(oldBase, local) {
		if oldBase, err = e.Git.MergeBase(parentSHA, local); err != nil {
			return false, err
		}
	}
	return true, e.Git.RebaseOnto(parent, oldBase, open[i].Branch)
}

func printRestack(w io.Writer, res restackResult) {
	if len(res.Restacked) == 0 {
		fmt.Fprintln(w, "unchanged: every layer sits on its parent")
		return
	}
	fmt.Fprintf(w, "%s: %s\n", res.Action, strings.Join(res.Restacked, " "))
}

// parentOf is what layer i sits on: the branch or ref name, its local tip,
// and the tip GitHub knows for it (empty for the base).
func (e *env) parentOf(open []layer, i int) (parent, localTip, githubTip string, err error) {
	if i == 0 {
		localTip, err = e.Git.RefSHA("refs/remotes/" + open[0].Parent)
		return open[0].Parent, localTip, "", err
	}
	below := open[i-1]
	localTip, err = e.Git.BranchSHA(below.Branch)
	return below.Branch, localTip, below.RemoteSHA, err
}

type pushResult struct {
	Action   string   `json:"action"`
	Remote   string   `json:"remote"`
	Branches []string `json:"branches"`
}

func runPush(ctx context.Context, e *env, args []string, o stackOpts) error {
	s, err := e.loadStack(ctx, args, o)
	if err != nil {
		return err
	}
	ls, err := e.layers(s, o.remote)
	if err != nil {
		return err
	}
	branches, err := pushable(ls)
	if err != nil {
		return err
	}
	res := pushResult{Action: actionUnchanged, Remote: o.remote, Branches: branches}
	if len(branches) > 0 {
		res.Action = dry("pushed", o.dryRun)
		if !o.dryRun {
			if err := e.Git.Push(o.remote, branches); err != nil {
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

// pushable is the open layers' branches that differ from GitHub; a missing
// or behind branch stops the push.
func pushable(ls []layer) ([]string, error) {
	out := []string{}
	for _, l := range ls {
		switch {
		case l.MergedOrClosed || l.Sync == syncInSync:
		case l.Sync == syncMissing:
			return nil, fmt.Errorf("%w: branch %s (#%d) is not checked out locally", stackapi.ErrNotFound, l.Branch, l.Number)
		case l.Sync == syncBehind:
			return nil, fmt.Errorf("%w: branch %s (#%d) is behind GitHub; pull it before pushing", stackapi.ErrConflict, l.Branch, l.Number)
		default:
			out = append(out, l.Branch)
		}
	}
	return out, nil
}
