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
	// a gains a fix; rebasing b onto it replays only b's own commit.
	r.must(t, "checkout", "-q", "a")
	r.must(t, "commit", "-q", "--allow-empty", "-m", "fix a")
	fixed, _ := r.BranchSHA("a")
	if base, err := r.MergeBase(fixed, b); err != nil || base != a {
		t.Fatalf("merge-base=%q err=%v", base, err)
	}
	if err := r.RebaseOnto("a", a, "b"); err != nil {
		t.Fatal(err)
	}
	b2, _ := r.BranchSHA("b")
	if b2 == b || !r.IsAncestor(fixed, b2) {
		t.Fatal("b was not moved onto the fixed a")
	}
	if n, _ := r.git("rev-list", "--count", fixed+".."+b2); n != "1" {
		t.Fatalf("replayed %s commits, want 1", n)
	}
	if err := r.Checkout("main"); err != nil {
		t.Fatal(err)
	}
	if cur, _ := r.CurrentBranch(); cur != "main" {
		t.Fatal(cur)
	}
	if err := r.RebaseOnto("nope", a, "b"); err == nil {
		t.Fatal("rebase onto a missing ref must fail")
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

func TestFirstCommitMessage(t *testing.T) {
	r := newRepo(t)
	r.must(t, "checkout", "-q", "-b", "feat")
	r.must(t, "commit", "-q", "--allow-empty", "-m", "Add the thing\n\nWhy it matters.")
	r.must(t, "commit", "-q", "--allow-empty", "-m", "Follow-up")
	subject, body, err := r.FirstCommitMessage("main", "feat")
	if err != nil || subject != "Add the thing" || body != "Why it matters." {
		t.Fatalf("%q %q %v", subject, body, err)
	}
	if _, _, err := r.FirstCommitMessage("feat", "main"); err == nil || !strings.Contains(err.Error(), "no commits") {
		t.Fatalf("err=%v", err)
	}
	if _, _, err := r.FirstCommitMessage("main", "nope"); err == nil {
		t.Fatal("expected an error for a missing branch")
	}
}
