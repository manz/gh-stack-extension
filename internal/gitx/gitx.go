// Package gitx runs git in a working tree.
package gitx

import (
	"fmt"
	"os/exec"
	"strings"
)

// Repo is a git working tree; Dir empty means the current directory.
type Repo struct{ Dir string }

func (r Repo) git(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = r.Dir
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, text)
	}
	return text, nil
}

// CurrentBranch returns the checked-out branch.
func (r Repo) CurrentBranch() (string, error) {
	return r.git("symbolic-ref", "--short", "HEAD")
}

// BranchSHA returns a local branch's commit, or "" if the branch is missing.
func (r Repo) BranchSHA(branch string) (string, error) {
	out, err := r.git("for-each-ref", "--format=%(objectname)", "refs/heads/"+branch)
	return out, err
}

// RefSHA returns any ref's commit, or "" if it does not exist.
func (r Repo) RefSHA(ref string) (string, error) {
	out, err := r.git("for-each-ref", "--format=%(objectname)", ref)
	return out, err
}

// HasCommit reports whether the commit is in the local object store.
func (r Repo) HasCommit(sha string) bool {
	_, err := r.git("cat-file", "-e", sha+"^{commit}")
	return err == nil
}

// IsAncestor reports whether a is an ancestor of (or equal to) b.
func (r Repo) IsAncestor(a, b string) bool {
	_, err := r.git("merge-base", "--is-ancestor", a, b)
	return err == nil
}

// RebaseOnto replays branch's commits after oldBase onto onto.
func (r Repo) RebaseOnto(onto, oldBase, branch string) error {
	_, err := r.git("rebase", "--onto", onto, oldBase, branch)
	return err
}

// MergeBase returns the best common ancestor of a and b.
func (r Repo) MergeBase(a, b string) (string, error) {
	return r.git("merge-base", a, b)
}

// Checkout switches to branch.
func (r Repo) Checkout(branch string) error {
	_, err := r.git("checkout", "-q", branch)
	return err
}

// Push pushes branches to remote, refusing to overwrite unseen remote work.
func (r Repo) Push(remote string, branches []string) error {
	_, err := r.git(append([]string{"push", "--force-with-lease", remote}, branches...)...)
	return err
}

// FirstCommitMessage returns the subject and body of the oldest commit on
// branch that upstream lacks.
func (r Repo) FirstCommitMessage(upstream, branch string) (subject, body string, err error) {
	shas, err := r.git("rev-list", "--reverse", upstream+".."+branch)
	if err != nil {
		return "", "", err
	}
	if shas == "" {
		return "", "", fmt.Errorf("%s has no commits on top of %s", branch, upstream)
	}
	msg, err := r.git("log", "-1", "--format=%B", strings.SplitN(shas, "\n", 2)[0])
	if err != nil {
		return "", "", err
	}
	subject, body, _ = strings.Cut(msg, "\n")
	return subject, strings.TrimSpace(body), nil
}

// LocalBranches maps every local branch to its commit.
func (r Repo) LocalBranches() (map[string]string, error) {
	out, err := r.git("for-each-ref", "--format=%(refname:short) %(objectname)", "refs/heads")
	if err != nil {
		return nil, err
	}
	branches := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if name, sha, ok := strings.Cut(line, " "); ok {
			branches[name] = sha
		}
	}
	return branches, nil
}

// DefaultBranch is the remote's default branch, from refs/remotes/REMOTE/HEAD.
func (r Repo) DefaultBranch(remote string) (string, error) {
	ref, err := r.git("symbolic-ref", "--short", "refs/remotes/"+remote+"/HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimPrefix(ref, remote+"/"), nil
}
