package cli

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func decodeSubmit(t *testing.T, h *harness) submitResult {
	t.Helper()
	var r submitResult
	if err := json.Unmarshal(h.out.Bytes(), &r); err != nil {
		t.Fatalf("json=%q err=%v", h.out.String(), err)
	}
	return r
}

func TestSubmitCreatesPullRequestsAndTheStack(t *testing.T) {
	h := newHarness()
	h.local("b1", "b2", "b3")
	if code := h.run("submit", "--base", "main", "--json", "b1", "b2", "b3"); code != ExitOK {
		t.Fatalf("code=%d err=%q", code, h.err.String())
	}
	r := decodeSubmit(t, h)
	if strings.Join(r.Pushed, " ") != "b1 b2 b3" || len(h.git.pushes) != 1 || h.git.pushes[0] != "origin b1 b2 b3" {
		t.Fatalf("pushed=%v pushes=%v", r.Pushed, h.git.pushes)
	}
	if r.PullRequests[0].Base != "main" || r.PullRequests[2].Base != "b2" || r.PullRequests[1].Action != actionCreated {
		t.Fatalf("%+v", r.PullRequests)
	}
	if r.Stack == nil || r.Stack.Action != actionCreated || r.Stack.Stack.Number != 101 {
		t.Fatalf("%+v", r.Stack)
	}
	pr := h.gh.prs[r.PullRequests[0].Number]
	if pr.GetTitle() != "Subject of b1" || pr.GetBody() != "Body of b1 on origin/main" || pr.GetDraft() {
		t.Fatalf("title=%q body=%q draft=%v", pr.GetTitle(), pr.GetBody(), pr.GetDraft())
	}
	if pr2 := h.gh.prs[r.PullRequests[1].Number]; pr2.GetBody() != "Body of b2 on b1" {
		t.Fatalf("body=%q", pr2.GetBody())
	}
}

func TestSubmitIsIdempotentAndFixesBases(t *testing.T) {
	h := newHarness()
	h.chain(2)
	h.gh.setStack(65, []int{1, 2})
	h.gh.addPR(3, "b3", "main") // opened against the wrong base
	h.local("b1", "b2", "b3")
	if code := h.run("submit", "b1", "b2", "b3"); code != ExitOK {
		t.Fatalf("code=%d err=%q", code, h.err.String())
	}
	want := "pushed to origin: b1 b2 b3\n  unchanged #1 (b1 → main)\n  unchanged #2 (b2 → b1)\n  retargeted #3 (b3 → b2)\nadded #3 to stack 65: #1 → #2 → #3\n"
	if h.out.String() != want {
		t.Fatalf("out=%q", h.out.String())
	}
	h.git.branch = "b2"
	if code := h.run("submit"); code != ExitOK || !strings.Contains(h.out.String(), "unchanged: stack 65") {
		t.Fatalf("rerun from the current stack: code=%d out=%q err=%q", code, h.out.String(), h.err.String())
	}
}

func TestSubmitFromTheCurrentStackSkipsMergedLayers(t *testing.T) {
	h := newHarness()
	h.chain(3)
	h.gh.setStack(65, []int{1, 2, 3})
	h.gh.stacks[65].PullRequests[0].State = "closed"
	h.gh.prs[2].Base.Ref = githubPtr("main")
	h.local("b2", "b3")
	h.git.branch = "b3"
	if code := h.run("submit", "--json"); code != ExitOK {
		t.Fatalf("code=%d err=%q", code, h.err.String())
	}
	if r := decodeSubmit(t, h); strings.Join(r.Pushed, " ") != "b2 b3" {
		t.Fatalf("pushed=%v", r.Pushed)
	}
}

func TestSubmitDryRunChangesNothing(t *testing.T) {
	h := newHarness()
	h.chain(1)
	h.gh.addPR(2, "b2", "main")
	h.local("b1", "b2", "b3")
	if code := h.run("submit", "--dry-run", "b1", "b2", "b3"); code != ExitOK {
		t.Fatalf("code=%d err=%q", code, h.err.String())
	}
	want := "would-push to origin: b1 b2 b3\n  unchanged #1 (b1 → main)\n  would-retargeted #2 (b2 → b1)\n  would-created new (b3 → b2)\nwould-link stack: #1 → #2 → new\n"
	if h.out.String() != want || len(h.git.pushes) != 0 {
		t.Fatalf("out=%q pushes=%v", h.out.String(), h.git.pushes)
	}
	for _, c := range h.gh.calls {
		if strings.HasPrefix(c, "Create") || strings.HasPrefix(c, "Edit") {
			t.Fatalf("dry run mutated: %v", h.gh.calls)
		}
	}
}

func TestSubmitMessageFileAndDraft(t *testing.T) {
	h := newHarness()
	msg := filepath.Join(t.TempDir(), "m")
	if err := os.WriteFile(msg, []byte("Custom title\n\nCustom body\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h.local("b1")
	if code := h.run("submit", "--base", "main", "--draft", "--message", "b1="+msg, "b1"); code != ExitOK {
		t.Fatalf("code=%d err=%q", code, h.err.String())
	}
	pr := h.gh.prs[201]
	if pr.GetTitle() != "Custom title" || pr.GetBody() != "Custom body" || !pr.GetDraft() {
		t.Fatalf("title=%q body=%q draft=%v", pr.GetTitle(), pr.GetBody(), pr.GetDraft())
	}
	if !strings.Contains(h.out.String(), "created #201 (b1 → main)") || strings.Contains(h.out.String(), "stack") {
		t.Fatalf("one branch is not a stack: %q", h.out.String())
	}
}

func TestSubmitErrors(t *testing.T) {
	h := newHarness()
	h.local("b1", "lonely")
	if code := h.run("submit", "b1"); code != ExitUsage || !strings.Contains(h.err.String(), "pass --base") {
		t.Fatalf("code=%d err=%q", code, h.err.String())
	}
	if code := h.run("submit", "--message", "bad", "b1"); code != ExitUsage {
		t.Fatalf("code=%d", code)
	}
	if code := h.run("submit", "--base", "main", "--message", "zz=/tmp/x", "b1"); code != ExitUsage || !strings.Contains(h.err.String(), "not submitted") {
		t.Fatalf("code=%d err=%q", code, h.err.String())
	}
	if code := h.run("submit", "--base", "main", "--message", "b1=/nonexistent/file", "b1"); code != ExitError {
		t.Fatalf("code=%d", code)
	}
	h.git.branch = "lonely"
	if code := h.run("submit"); code != ExitUsage || !strings.Contains(h.err.String(), "lonely has no pull request yet") {
		t.Fatalf("code=%d err=%q", code, h.err.String())
	}
}

func TestSubmitStopsOnFailures(t *testing.T) {
	cases := []struct {
		name string
		set  func(h *harness)
		code int
	}{
		{"push", func(h *harness) { h.git.failOp["Push"] = errors.New("rejected") }, ExitError},
		{"commit message", func(h *harness) { h.git.failOp["FirstCommitMessage"] = errors.New("no commits") }, ExitError},
		{"create", func(h *harness) { h.gh.fail["Create"] = apiError(422, "no commits between") }, ExitValidation},
		{"list", func(h *harness) { h.gh.fail["List"] = apiError(422, "bad head") }, ExitValidation},
		{"stack", func(h *harness) { h.gh.fail["CreateStack"] = apiError(422, "nope") }, ExitValidation},
	}
	for _, c := range cases {
		h := newHarness()
		h.local("b1", "b2")
		c.set(h)
		if code := h.run("submit", "--base", "main", "b1", "b2"); code != c.code {
			t.Errorf("%s: code=%d err=%q", c.name, code, h.err.String())
		}
	}
	h := newHarness()
	h.chain(1)
	h.gh.addPR(2, "b2", "main")
	h.local("b1", "b2")
	h.gh.fail["Edit"] = apiError(422, "base")
	if code := h.run("submit", "b1", "b2"); code != ExitValidation {
		t.Fatalf("edit: code=%d", code)
	}
	h = newHarness()
	h.gh.fail["List"] = apiError(404, "nope")
	if code := h.run("submit", "b1"); code != ExitNotFound {
		t.Fatalf("trunk lookup: code=%d", code)
	}
}

func TestSubmitPutsANewBranchOnTopOfTheStackItWasCutFrom(t *testing.T) {
	h := synced() // stack 65 = b1 <- b2 <- b3, local branches in sync
	h.git.commit("n4", "sha-b3")
	h.git.refs["refs/heads/b4"] = "n4"
	h.git.branch = "b4"
	if code := h.run("submit", "--json"); code != ExitOK {
		t.Fatalf("code=%d err=%q", code, h.err.String())
	}
	r := decodeSubmit(t, h)
	last := r.PullRequests[len(r.PullRequests)-1]
	if strings.Join(r.Pushed, " ") != "b4" || last.Branch != "b4" || last.Base != "b3" || last.Action != actionCreated {
		t.Fatalf("%+v", r)
	}
	if r.Stack.Action != actionAdded || r.Stack.Stack.Number != 65 {
		t.Fatalf("%+v", r.Stack)
	}
}

func TestSubmitABranchOnNoStackNeedsABase(t *testing.T) {
	h := synced()
	h.git.commit("z", "m0")
	h.git.refs["refs/heads/side"] = "z"
	h.git.branch = "side"
	if code := h.run("submit"); code != ExitUsage || !strings.Contains(h.err.String(), "pass --base") {
		t.Fatalf("code=%d err=%q", code, h.err.String())
	}
	if code := h.run("submit", "--base", "main"); code != ExitOK || !strings.Contains(h.out.String(), "created #201 (side → main)") {
		t.Fatalf("code=%d out=%q err=%q", code, h.out.String(), h.err.String())
	}
}

func TestSubmitALoneOpenPullRequest(t *testing.T) {
	h := newHarness()
	h.gh.addPR(7, "solo", "main")
	h.local("solo")
	h.git.branch = "solo"
	if code := h.run("submit"); code != ExitOK || !strings.Contains(h.out.String(), "unchanged #7 (solo → main)") {
		t.Fatalf("code=%d out=%q", code, h.out.String())
	}
}

func TestSubmitSkipsClosedAndUncheckedStacksWhenLookingBelow(t *testing.T) {
	h := synced()
	h.gh.stacks[65].Open = false
	h.chain(0)
	h.gh.addPR(8, "c1", "main")
	h.gh.addPR(9, "c2", "c1")
	h.gh.setStack(66, []int{8, 9}) // top branch c2 not checked out
	h.git.commit("n4", "sha-b3")
	h.git.refs["refs/heads/b4"] = "n4"
	h.git.branch = "b4"
	if code := h.run("submit"); code != ExitUsage {
		t.Fatalf("code=%d err=%q", code, h.err.String())
	}
}

func TestSubmitCurrentBranchErrors(t *testing.T) {
	h := synced()
	h.git.err = errors.New("detached HEAD")
	if code := h.run("submit"); code != ExitError {
		t.Fatalf("code=%d", code)
	}
	h = synced()
	h.git.branch = "b2"
	h.gh.fail["GetStack"] = apiError(404, "gone")
	if code := h.run("submit"); code != ExitNotFound {
		t.Fatalf("code=%d", code)
	}
	h = synced()
	h.git.commit("n4", "sha-b3")
	h.git.refs["refs/heads/b4"] = "n4"
	h.git.branch = "b4"
	h.gh.fail["ListStacks"] = apiError(422, "x")
	if code := h.run("submit"); code != ExitValidation {
		t.Fatalf("code=%d", code)
	}
	h.gh.fail = map[string]error{"GetStack": apiError(404, "x")}
	if code := h.run("submit"); code != ExitNotFound {
		t.Fatalf("code=%d", code)
	}
	h.gh.fail = map[string]error{}
	h.git.failOp["BranchSHA"] = errors.New("git broke")
	if code := h.run("submit"); code != ExitError {
		t.Fatalf("code=%d", code)
	}
}

func TestSubmitDryRunOfACompleteStackReportsUnchanged(t *testing.T) {
	h := synced()
	if code := h.run("submit", "--dry-run"); code != ExitOK || !strings.HasSuffix(h.out.String(), "unchanged: stack 65 is #1 → #2 → #3\n") {
		t.Fatalf("code=%d out=%q", code, h.out.String())
	}
}

func TestSubmitPushesOnlyChangedBranchesAndNeedsThemLocally(t *testing.T) {
	h := synced()
	h.git.commit("x2", "sha-b2")
	h.git.refs["refs/heads/b2"] = "x2"
	if code := h.run("submit", "--json"); code != ExitOK || h.git.pushes[0] != "origin b2" {
		t.Fatalf("pushes=%v err=%q", h.git.pushes, h.err.String())
	}
	h = synced()
	if code := h.run("submit"); code != ExitOK || !strings.HasPrefix(h.out.String(), "nothing to push: origin matches every branch") || len(h.git.pushes) != 0 {
		t.Fatalf("out=%q pushes=%v", h.out.String(), h.git.pushes)
	}
	delete(h.git.refs, "refs/heads/b3")
	if code := h.run("submit", "b1", "b2", "b3"); code != ExitNotFound || !strings.Contains(h.err.String(), "b3 does not exist locally") {
		t.Fatalf("code=%d err=%q", code, h.err.String())
	}
	h = synced()
	h.git.failOp["RefSHA"] = errors.New("git broke")
	if code := h.run("submit", "b1"); code != ExitError {
		t.Fatalf("code=%d", code)
	}
}
