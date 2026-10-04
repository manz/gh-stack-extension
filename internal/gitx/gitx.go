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
