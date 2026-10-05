package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// conflicted is stack 65 whose base moved; restacking b2 stops on a conflict
// after b1 has already moved.
func conflicted(t *testing.T) *harness {
	t.Helper()
	h := synced()
	h.git.commit("m1", "m0")
	h.git.refs["refs/remotes/origin/main"] = "m1"
	h.git.conflicts["b2"] = true
	if code := h.run("restack"); code != ExitConflict || !strings.Contains(h.err.String(), "b2 conflicts with b1: resolve it, git add, then restack --continue") {
		t.Fatalf("code=%d err=%q", code, h.err.String())
	}
	return h
}

func statePath(h *harness) string { return filepath.Join(h.git.gitDir, restackStateFile) }

func TestRestackStopsOnAConflictAndRefusesToStartAgain(t *testing.T) {
	h := conflicted(t)
	if _, err := os.Stat(statePath(h)); err != nil {
		t.Fatal("no saved state")
	}
	if code := h.run("restack"); code != ExitUsage || !strings.Contains(h.err.String(), "restack --continue, or restack --abort") {
		t.Fatalf("code=%d err=%q", code, h.err.String())
	}
}

func TestRestackContinueFinishesTheStack(t *testing.T) {
	h := conflicted(t)
	if code := h.run("restack", "--continue"); code != ExitOK || h.out.String() != "restacked: b1 b2 b3\n" {
		t.Fatalf("code=%d out=%q err=%q", code, h.out.String(), h.err.String())
	}
	if !h.git.IsAncestor("m1", h.git.refs["refs/heads/b3"]) || h.git.checkouts[len(h.git.checkouts)-1] != "b2" {
		t.Fatalf("b3 not on m1 or not back on b2: %v", h.git.checkouts)
	}
	if _, err := os.Stat(statePath(h)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("state left behind")
	}
}

func TestRestackContinueAfterTheLastLayerReportsRestacked(t *testing.T) {
	h := synced()
	h.git.commit("m1", "m0")
	h.git.refs["refs/remotes/origin/main"] = "m1"
	h.git.conflicts["b3"] = true
	if code := h.run("restack"); code != ExitConflict {
		t.Fatalf("code=%d", code)
	}
	if code := h.run("restack", "--continue"); code != ExitOK || h.out.String() != "restacked: b1 b2 b3\n" {
		t.Fatalf("code=%d out=%q err=%q", code, h.out.String(), h.err.String())
	}
}

func TestRestackAbortPutsEveryBranchBack(t *testing.T) {
	h := conflicted(t)
	if code := h.run("restack", "--abort"); code != ExitOK || h.out.String() != "aborted: every branch is back where it was\n" {
		t.Fatalf("code=%d out=%q err=%q", code, h.out.String(), h.err.String())
	}
	if h.git.refs["refs/heads/b1"] != "sha-b1" || h.git.pending != nil || h.git.checkouts[len(h.git.checkouts)-1] != "b2" {
		t.Fatalf("b1=%s pending=%v checkouts=%v", h.git.refs["refs/heads/b1"], h.git.pending, h.git.checkouts)
	}
	if _, err := os.Stat(statePath(h)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("state left behind")
	}
}

func TestRestackFlagMisuse(t *testing.T) {
	h := synced()
	for _, args := range [][]string{{"restack", "--continue", "--abort"}, {"restack", "--continue", "--dry-run"}, {"restack", "--continue"}, {"restack", "--abort"}} {
		if code := h.run(args...); code != ExitUsage {
			t.Errorf("%v code=%d err=%q", args, code, h.err.String())
		}
	}
}

func TestRestackContinueWhileStillConflicted(t *testing.T) {
	h := conflicted(t)
	h.git.failOp["RebaseContinue"] = errors.New("you must edit all merge conflicts")
	if code := h.run("restack", "--continue"); code != ExitConflict || !strings.Contains(h.err.String(), "edit all merge conflicts") {
		t.Fatalf("code=%d err=%q", code, h.err.String())
	}
}

func TestRestackStateErrors(t *testing.T) {
	h := synced()
	h.git.failOp["GitPath"] = errors.New("not a git repository")
	if code := h.run("restack"); code != ExitError {
		t.Fatalf("code=%d", code)
	}
	h = synced()
	if err := os.WriteFile(statePath(h), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if code := h.run("restack"); code != ExitError || !strings.Contains(h.err.String(), "reading") {
		t.Fatalf("code=%d err=%q", code, h.err.String())
	}
	for _, op := range []string{"RebaseAbort", "SetBranch", "Checkout"} {
		h = conflicted(t)
		h.git.failOp[op] = errors.New(op + " broke")
		if code := h.run("restack", "--abort"); code != ExitError {
			t.Errorf("%s: code=%d", op, code)
		}
	}
	h = conflicted(t)
	h.git.err = errors.New("detached")
	if code := h.run("restack", "--abort"); code != ExitError {
		t.Fatalf("code=%d", code)
	}
	h = synced()
	h.git.commit("m1", "m0")
	h.git.refs["refs/remotes/origin/main"] = "m1"
	h.git.gitDir = filepath.Join(h.git.gitDir, "missing", "dir")
	if code := h.run("restack"); code != ExitError {
		t.Fatalf("unwritable state: code=%d", code)
	}
}
