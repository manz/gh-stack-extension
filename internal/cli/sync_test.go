package cli

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/go-github/v92/github"
)

// synced builds stack 65 = #1 b1 <- #2 b2 <- #3 b3 onto main, with local
// branches equal to GitHub's heads: m0 <- sha-b1 <- sha-b2 <- sha-b3.
func synced() *harness {
	h := newHarness()
	h.chain(3)
	h.gh.setStack(65, []int{1, 2, 3})
	h.git.commit("m0", "")
	h.git.commit("sha-b1", "m0")
	h.git.commit("sha-b2", "sha-b1")
	h.git.commit("sha-b3", "sha-b2")
	h.git.refs["refs/remotes/origin/main"] = "m0"
	for _, b := range []string{"b1", "b2", "b3"} {
		h.git.refs["refs/heads/"+b] = "sha-" + b
	}
	h.git.branch = "b2"
	return h
}

func statusOf(t *testing.T, h *harness, args ...string) statusResult {
	t.Helper()
	if code := h.run(append([]string{"status", "--json"}, args...)...); code != ExitOK {
		t.Fatalf("code=%d err=%q", code, h.err.String())
	}
	var r statusResult
	if err := json.Unmarshal(h.out.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func syncs(r statusResult) string {
	var parts []string
	for _, l := range r.Layers {
		s := l.Branch + "=" + l.Sync
		if l.NeedsRestack {
			s += "+restack"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " ")
}

func TestStatusInSync(t *testing.T) {
	h := synced()
	if got := syncs(statusOf(t, h)); got != "b1=in_sync b2=in_sync b3=in_sync" {
		t.Fatal(got)
	}
	if code := h.run("status"); code != ExitOK || !strings.Contains(h.out.String(), "stack 65 onto main\n  #1 b1: in_sync") {
		t.Fatalf("out=%q", h.out.String())
	}
}

func TestStatusStates(t *testing.T) {
	h := synced()
	h.git.commit("x2", "sha-b2") // b2 gained a commit: push, and b3 needs a restack
	h.git.refs["refs/heads/b2"] = "x2"
	h.git.commit("y1", "m0") // b1 rewritten: diverged
	h.git.refs["refs/heads/b1"] = "y1"
	if got := syncs(statusOf(t, h, "65")); got != "b1=diverged b2=ahead+restack b3=in_sync+restack" {
		t.Fatal(got)
	}
	h = synced()
	h.git.refs["refs/heads/b1"] = "m0"              // behind GitHub
	h.gh.stacks[65].PullRequests[1].Head.SHA = "zz" // never fetched
	delete(h.git.refs, "refs/heads/b3")             // not checked out
	if got := syncs(statusOf(t, h, "--pr", "1")); got != "b1=behind b2=unknown b3=missing" {
		t.Fatal(got)
	}
}

func TestStatusSkipsMergedLayers(t *testing.T) {
	h := synced()
	h.gh.stacks[65].PullRequests[0].MergedAt = &github.Timestamp{}
	h.gh.stacks[65].PullRequests[0].State = "closed"
	r := statusOf(t, h)
	if !r.Layers[0].MergedOrClosed || r.Layers[1].Parent != "origin/main" {
		t.Fatalf("%+v", r.Layers)
	}
	if code := h.run("status"); code != ExitOK || !strings.Contains(h.out.String(), "#1 b1: merged or closed") {
		t.Fatalf("out=%q", h.out.String())
	}
}

func TestStatusErrors(t *testing.T) {
	h := synced()
	if code := h.run("status", "99"); code != ExitNotFound {
		t.Fatalf("code=%d", code)
	}
	h.git.failOp["RefSHA"] = errors.New("git broke")
	if code := h.run("status"); code != ExitError {
		t.Fatalf("code=%d", code)
	}
	h = synced()
	h.git.failOp["BranchSHA"] = errors.New("git broke")
	if code := h.run("status"); code != ExitError {
		t.Fatalf("code=%d", code)
	}
	h = synced()
	h.git.branch = "unrelated"
	if code := h.run("status"); code != ExitNotFound {
		t.Fatalf("code=%d", code)
	}
}

func TestRestackRebasesTheTopOntoTheBase(t *testing.T) {
	h := synced()
	if code := h.run("restack"); code != ExitOK || h.out.String() != "unchanged: b3 onto origin/main\n" || len(h.git.restacks) != 0 {
		t.Fatalf("out=%q restacks=%v", h.out.String(), h.git.restacks)
	}
	h.git.commit("m1", "m0") // main moved
	h.git.refs["refs/remotes/origin/main"] = "m1"
	if code := h.run("restack", "--dry-run"); code != ExitOK || h.out.String() != "would-restacked: b3 onto origin/main\n" || len(h.git.restacks) != 0 {
		t.Fatalf("out=%q", h.out.String())
	}
	if code := h.run("restack", "--json"); code != ExitOK || len(h.git.restacks) != 1 || h.git.restacks[0] != "origin/main b3" {
		t.Fatalf("restacks=%v err=%q", h.git.restacks, h.err.String())
	}
	var r restackResult
	if err := json.Unmarshal(h.out.Bytes(), &r); err != nil || r.Action != "restacked" || r.Branches["b2"] != "sha-b2" {
		t.Fatalf("%+v %v", r, err)
	}
}

func TestRestackStartsAboveMergedLayers(t *testing.T) {
	h := synced()
	h.gh.stacks[65].PullRequests[0].MergedAt = &github.Timestamp{}
	h.gh.stacks[65].PullRequests[0].State = "closed"
	h.git.commit("m1", "sha-b1") // b1 merged into main
	h.git.refs["refs/remotes/origin/main"] = "m1"
	if code := h.run("restack"); code != ExitOK || h.git.restacks[0] != "origin/main b3" {
		t.Fatalf("restacks=%v err=%q", h.git.restacks, h.err.String())
	}
}

func TestRestackErrors(t *testing.T) {
	h := synced()
	for _, p := range h.gh.stacks[65].PullRequests {
		p.State = "closed"
	}
	if code := h.run("restack"); code != ExitValidation {
		t.Fatalf("code=%d", code)
	}
	h = synced()
	delete(h.git.refs, "refs/heads/b3")
	if code := h.run("restack"); code != ExitNotFound || !strings.Contains(h.err.String(), "b3 (#3) is not checked out") {
		t.Fatalf("code=%d err=%q", code, h.err.String())
	}
	h = synced()
	h.git.commit("m1", "m0")
	h.git.refs["refs/remotes/origin/main"] = "m1"
	h.git.failOp["RestackOnto"] = errors.New("conflict in b2")
	if code := h.run("restack"); code != ExitError || !strings.Contains(h.err.String(), "conflict in b2") {
		t.Fatalf("code=%d err=%q", code, h.err.String())
	}
	if code := h.run("restack", "99"); code != ExitNotFound {
		t.Fatalf("code=%d", code)
	}
	h = synced()
	h.git.failOp["RefSHA"] = errors.New("git broke")
	if code := h.run("restack"); code != ExitError {
		t.Fatalf("code=%d", code)
	}
}

func TestRestackReportsBranchReadErrors(t *testing.T) {
	h := synced()
	h.git.failOp["BranchSHA"] = errors.New("git broke")
	if code := h.run("restack"); code != ExitError {
		t.Fatalf("code=%d", code)
	}
}

func TestPushSendsOnlyBranchesThatDiffer(t *testing.T) {
	h := synced()
	if code := h.run("push"); code != ExitOK || h.out.String() != "unchanged: every branch matches GitHub\n" || len(h.git.pushes) != 0 {
		t.Fatalf("out=%q", h.out.String())
	}
	h.git.commit("x2", "sha-b2")
	h.git.refs["refs/heads/b2"] = "x2"
	h.git.commit("x3", "x2")
	h.git.refs["refs/heads/b3"] = "x3"
	if code := h.run("push", "--dry-run"); code != ExitOK || h.out.String() != "would-pushed to origin: b2 b3\n" || len(h.git.pushes) != 0 {
		t.Fatalf("out=%q", h.out.String())
	}
	if code := h.run("push", "--remote", "fork", "--json"); code != ExitOK || h.git.pushes[0] != "fork b2 b3" {
		t.Fatalf("pushes=%v err=%q", h.git.pushes, h.err.String())
	}
}

func TestPushRefusesMissingAndBehindBranches(t *testing.T) {
	h := synced()
	delete(h.git.refs, "refs/heads/b2")
	if code := h.run("push", "65"); code != ExitNotFound {
		t.Fatalf("code=%d", code)
	}
	h = synced()
	h.git.refs["refs/heads/b1"] = "m0"
	if code := h.run("push"); code != ExitConflict || !strings.Contains(h.err.String(), "behind GitHub") {
		t.Fatalf("code=%d err=%q", code, h.err.String())
	}
	h = synced()
	h.git.commit("x1", "sha-b1")
	h.git.refs["refs/heads/b1"] = "x1"
	h.git.failOp["Push"] = errors.New("rejected")
	if code := h.run("push"); code != ExitError {
		t.Fatalf("code=%d", code)
	}
	if code := h.run("push", "99"); code != ExitNotFound {
		t.Fatalf("code=%d", code)
	}
	h.git.failOp["RefSHA"] = errors.New("git broke")
	if code := h.run("push"); code != ExitError {
		t.Fatalf("code=%d", code)
	}
}

func TestPushSkipsMergedLayers(t *testing.T) {
	h := synced()
	h.gh.stacks[65].PullRequests[0].State = "closed"
	h.git.refs["refs/heads/b1"] = "m0" // would be "behind" if it counted
	if code := h.run("push"); code != ExitOK {
		t.Fatalf("code=%d err=%q", code, h.err.String())
	}
}
