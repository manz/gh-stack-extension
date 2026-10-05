package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	"github.com/manz/gh-stack-extension/internal/stackapi"
)

func init() {
	register("restack", command{usage: "replay each layer onto its parent, bottom up (--continue / --abort after a conflict)", setup: restackFlags})
}

type restackOpts struct {
	stackOpts
	resume, abort bool
}

func restackFlags(flags *flag.FlagSet) func(context.Context, *env, []string) error {
	var o restackOpts
	flags.IntVar(&o.pr, "pr", 0, "the stack containing this pull request")
	flags.StringVar(&o.remote, "remote", "origin", "git remote the branches live on")
	flags.BoolVar(&o.dryRun, "dry-run", false, "print what would change, change nothing")
	flags.BoolVar(&o.resume, "continue", false, "after resolving a conflict: finish it and restack the rest")
	flags.BoolVar(&o.abort, "abort", false, "after a conflict: put every branch back where it was")
	return func(ctx context.Context, e *env, args []string) error { return runRestack(ctx, e, args, o) }
}

type restackResult struct {
	Action    string            `json:"action"`
	Restacked []string          `json:"restacked"` // bottom first
	Branches  map[string]string `json:"branches"`  // branch -> local sha after
}

// restackState is written before the first rebase and removed when the
// restack ends: it is what --abort restores.
type restackState struct {
	Original string            `json:"original"`           // branch checked out before
	Branches map[string]string `json:"branches"`           // branch -> sha before
	Rebasing string            `json:"rebasing,omitempty"` // branch the stopped rebase was moving
	Moved    []string          `json:"moved,omitempty"`    // branches moved so far, bottom first
}

const restackStateFile = "gh-stack-extension-restack.json"

var errRestackInProgress = errors.New("a restack stopped on a conflict: resolve it and run restack --continue, or restack --abort")

// runRestack walks the open layers bottom up. A layer whose parent's tip is
// not in its history gets its own commits replayed onto the parent: the old
// parent tip is GitHub's head of the layer below (what it was built on), or
// the merge-base for the bottom layer. Fixing a middle layer so carries up.
// A conflict stops it; --continue finishes that layer and restacks the rest.
func runRestack(ctx context.Context, e *env, args []string, o restackOpts) error {
	if o.resume && o.abort || (o.resume || o.abort) && o.dryRun {
		return usagef("--continue, --abort and --dry-run are exclusive")
	}
	path, err := e.Git.GitPath(restackStateFile)
	if err != nil {
		return err
	}
	state, err := loadRestackState(path)
	if err != nil {
		return err
	}
	if o.abort {
		return e.abortRestack(path, state)
	}
	if err := e.resumeRestack(o.resume, state); err != nil {
		return err
	}
	return e.restackAll(ctx, args, o, &restacker{e: e, path: path, state: state, dryRun: o.dryRun})
}

// resumeRestack finishes a stopped rebase for --continue; without it, a
// saved state means a restack is still waiting on a conflict.
func (e *env) resumeRestack(resume bool, state *restackState) error {
	switch {
	case !resume && state != nil:
		return usageError{errRestackInProgress.Error()}
	case !resume:
		return nil
	case state == nil:
		return usagef("no restack to continue")
	case !e.Git.RebaseInProgress():
		return nil
	}
	if err := e.Git.RebaseContinue(); err != nil {
		return fmt.Errorf("%w: %v", stackapi.ErrConflict, err)
	}
	state.Moved = append(state.Moved, state.Rebasing)
	return nil
}

// restacker replays layers and keeps the state --abort restores on disk.
type restacker struct {
	e      *env
	path   string
	state  *restackState
	dryRun bool
}

func (e *env) restackAll(ctx context.Context, args []string, o restackOpts, r *restacker) error {
	open, err := e.openLayers(ctx, args, o.stackOpts)
	if err != nil {
		return err
	}
	if r.state == nil {
		if r.state, err = e.snapshot(open); err != nil {
			return err
		}
	}
	state := r.state
	res := restackResult{Action: actionUnchanged, Restacked: append([]string{}, state.Moved...), Branches: map[string]string{}}
	moving := false // under --dry-run, everything above a moved layer moves too
	for i, l := range open {
		moved, err := r.layer(open, i, moving)
		if err != nil {
			return err
		}
		if moved {
			res.Restacked = append(res.Restacked, l.Branch)
			state.Moved = res.Restacked
			moving = o.dryRun
		}
		if res.Branches[l.Branch], err = e.Git.BranchSHA(l.Branch); err != nil {
			return err
		}
	}
	if err := e.finishRestack(r.path, state, &res, o.dryRun); err != nil {
		return err
	}
	return e.emit(res, func(w io.Writer) { printRestack(w, res) })
}

// snapshot records the branches and the checkout --abort would restore.
func (e *env) snapshot(open []layer) (*restackState, error) {
	current, err := e.Git.CurrentBranch()
	if err != nil {
		return nil, err
	}
	s := &restackState{Original: current, Branches: map[string]string{}}
	for _, l := range open {
		if s.Branches[l.Branch], err = e.Git.BranchSHA(l.Branch); err != nil {
			return nil, err
		}
	}
	return s, nil
}

// finishRestack puts the original branch back and forgets the state.
func (e *env) finishRestack(path string, state *restackState, res *restackResult, dryRun bool) error {
	if dryRun {
		if len(res.Restacked) > 0 {
			res.Action = dry("restacked", true)
		}
		return nil
	}
	if len(res.Restacked) > 0 {
		res.Action = "restacked"
		if err := e.Git.Checkout(state.Original); err != nil {
			return err
		}
	}
	return removeRestackState(path)
}

// abortRestack stops the rebase and puts every branch back.
func (e *env) abortRestack(path string, state *restackState) error {
	if state == nil {
		return usagef("no restack to abort")
	}
	if e.Git.RebaseInProgress() {
		if err := e.Git.RebaseAbort(); err != nil {
			return err
		}
	}
	current, err := e.Git.CurrentBranch()
	if err != nil {
		return err
	}
	for b, sha := range state.Branches {
		if b == current {
			continue // the rebase abort already put it back
		}
		if err := e.Git.SetBranch(b, sha); err != nil {
			return err
		}
	}
	if err := e.Git.Checkout(state.Original); err != nil {
		return err
	}
	if err := removeRestackState(path); err != nil {
		return err
	}
	res := restackResult{Action: "aborted", Restacked: []string{}, Branches: state.Branches}
	return e.emit(res, func(w io.Writer) { fmt.Fprintln(w, "aborted: every branch is back where it was") })
}

// layer replays layer i onto its parent when the parent's tip is not in its
// history (or, under --dry-run, when a layer below would move).
func (r *restacker) layer(open []layer, i int, moving bool) (bool, error) {
	git := r.e.Git
	parent, parentSHA, oldBase, err := r.e.parentOf(open, i)
	if err != nil {
		return false, err
	}
	local, err := git.BranchSHA(open[i].Branch)
	if err != nil {
		return false, err
	}
	if !moving && (parentSHA == "" || git.IsAncestor(parentSHA, local)) {
		return false, nil
	}
	if r.dryRun {
		return true, nil
	}
	if oldBase, err = r.oldBase(oldBase, parentSHA, local); err != nil {
		return false, err
	}
	return true, r.replay(parent, oldBase, open[i].Branch)
}

// oldBase is where the layer's own commits start: GitHub's head of the layer
// below when the branch contains it, else the merge-base with the parent.
func (r *restacker) oldBase(githubTip, parentSHA, local string) (string, error) {
	git := r.e.Git
	if githubTip != "" && git.HasCommit(githubTip) && git.IsAncestor(githubTip, local) {
		return githubTip, nil
	}
	return git.MergeBase(parentSHA, local)
}

// replay saves the state, then rebases; a conflict leaves both for --continue.
func (r *restacker) replay(parent, oldBase, branch string) error {
	r.state.Rebasing = branch
	if err := saveRestackState(r.path, r.state); err != nil {
		return err
	}
	if err := r.e.Git.RebaseOnto(parent, oldBase, branch); err != nil {
		if r.e.Git.RebaseInProgress() {
			return fmt.Errorf("%w: %s conflicts with %s: resolve it, git add, then restack --continue (or restack --abort)", stackapi.ErrConflict, branch, parent)
		}
		return err
	}
	return nil
}

func printRestack(w io.Writer, res restackResult) {
	if len(res.Restacked) == 0 {
		fmt.Fprintln(w, "unchanged: every layer sits on its parent")
		return
	}
	fmt.Fprintf(w, "%s: %s\n", res.Action, strings.Join(res.Restacked, " "))
}

func loadRestackState(path string) (*restackState, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var s restackState
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	return &s, nil
}

func saveRestackState(path string, s *restackState) error {
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600)
}

func removeRestackState(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}
