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
		// CI runners have no git identity; restacking commits needs one.
		{"config", "user.name", "t"},
		{"config", "user.email", "t@t"},
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

func (r Repo) must(t *testing.T, args ...string) string {
	t.Helper()
	out, err := r.git(append([]string{"-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRefsAncestryRestackAndPush(t *testing.T) {
	r := newRepo(t)
	r.must(t, "checkout", "-q", "-b", "a")
	r.must(t, "commit", "-q", "--allow-empty", "-m", "a")
	r.must(t, "checkout", "-q", "-b", "b")
	r.must(t, "commit", "-q", "--allow-empty", "-m", "b")
	a, _ := r.BranchSHA("a")
	b, _ := r.BranchSHA("b")
	if missing, _ := r.BranchSHA("nope"); missing != "" || a == "" || !r.HasCommit(a) || r.HasCommit("0123456789abcdef0123456789abcdef01234567") {
		t.Fatal("branch lookup")
	}
	if !r.IsAncestor(a, b) || r.IsAncestor(b, a) {
		t.Fatal("ancestry")
	}
	// main moves on; restacking b onto main carries a along.
	r.must(t, "checkout", "-q", "main")
	r.must(t, "commit", "-q", "--allow-empty", "-m", "main 2")
	main, _ := r.RefSHA("refs/heads/main")
	r.must(t, "checkout", "-q", "b")
	if err := r.RestackOnto("main", "b"); err != nil {
		t.Fatal(err)
	}
	a2, _ := r.BranchSHA("a")
	b2, _ := r.BranchSHA("b")
	if a2 == a || b2 == b || !r.IsAncestor(main, a2) || !r.IsAncestor(a2, b2) {
		t.Fatal("restack did not move both branches onto main")
	}
	// push to a bare remote
	remote := t.TempDir()
	if _, err := (Repo{Dir: remote}).git("init", "-q", "--bare"); err != nil {
		t.Fatal(err)
	}
	r.must(t, "remote", "add", "origin", remote)
	if err := r.Push("origin", []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}
	if pushed, _ := r.RefSHA("refs/remotes/origin/b"); pushed != b2 {
		t.Fatalf("remote b=%q want %q", pushed, b2)
	}
	if err := r.Push("nowhere", []string{"a"}); err == nil {
		t.Fatal("push to a missing remote must fail")
	}
}
