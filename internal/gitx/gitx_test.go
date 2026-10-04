package gitx

import (
	"os/exec"
	"strings"
	"testing"
)

// newRepo makes a repository with one commit on main.
func newRepo(t *testing.T) Repo {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"-c", "user.name=t", "-c", "user.email=t@t", "commit", "-q", "--allow-empty", "-m", "root"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, out)
		}
	}
	return Repo{Dir: dir}
}

func TestCurrentBranch(t *testing.T) {
	b, err := newRepo(t).CurrentBranch()
	if err != nil || b != "main" {
		t.Fatalf("branch=%q err=%v", b, err)
	}
}

func TestErrorsNameTheCommand(t *testing.T) {
	_, err := Repo{Dir: t.TempDir()}.CurrentBranch()
	if err == nil || !strings.Contains(err.Error(), "git symbolic-ref") {
		t.Fatalf("err=%v", err)
	}
}
