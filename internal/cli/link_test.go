package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func decodeLink(t *testing.T, h *harness) linkResult {
	t.Helper()
	var r linkResult
	if err := json.Unmarshal(h.out.Bytes(), &r); err != nil {
		t.Fatalf("json=%q err=%v", h.out.String(), err)
	}
	return r
}

func TestCreateLinksThenIsUnchanged(t *testing.T) {
	h := newHarness()
	h.chain(3)
	if code := h.run("create", "1", "2", "3"); code != ExitOK || h.out.String() != "created stack 101: #1 → #2 → #3\n" {
		t.Fatalf("code=%d out=%q err=%q", code, h.out.String(), h.err.String())
	}
	if code := h.run("create", "1", "2", "3", "--json"); code != ExitOK {
		t.Fatal(code)
	}
	if r := decodeLink(t, h); r.Action != actionUnchanged || r.Stack.Number != 101 {
		t.Fatalf("%+v", r)
	}
	if code := h.run("create", "1", "2", "3"); code != ExitOK || h.out.String() != "unchanged: stack 101 is #1 → #2 → #3\n" {
		t.Fatalf("out=%q", h.out.String())
	}
}

func TestCreateGrowsAnExistingPrefix(t *testing.T) {
	h := newHarness()
	h.chain(3)
	h.gh.setStack(65, []int{1, 2})
	if code := h.run("create", "1", "2", "3"); code != ExitOK || h.out.String() != "added #3 to stack 65: #1 → #2 → #3\n" {
		t.Fatalf("code=%d out=%q err=%q", code, h.out.String(), h.err.String())
	}
}

func TestCreateDryRunChangesNothing(t *testing.T) {
	h := newHarness()
	h.chain(3)
	if code := h.run("create", "1", "2", "--dry-run"); code != ExitOK || h.out.String() != "would-created stack: #1 → #2\n" {
		t.Fatalf("out=%q", h.out.String())
	}
	h.gh.setStack(65, []int{1, 2})
	if code := h.run("create", "1", "2", "3", "--dry-run", "--json"); code != ExitOK {
		t.Fatal(code)
	}
	if r := decodeLink(t, h); r.Action != "would-added" || fmt.Sprint(r.Added) != "[3]" {
		t.Fatalf("%+v", r)
	}
	for _, c := range h.gh.calls {
		if strings.HasPrefix(c, "CreateStack") || strings.HasPrefix(c, "AddToStack") {
			t.Fatalf("dry run mutated: %v", h.gh.calls)
		}
	}
}

func TestCreateRefusesAPullRequestInAnotherStack(t *testing.T) {
	h := newHarness()
	h.chain(3)
	h.gh.setStack(65, []int{1, 2})
	if code := h.run("create", "2", "3"); code != ExitValidation || !strings.Contains(h.err.String(), "#2 is in stack 65") {
		t.Fatalf("code=%d err=%q", code, h.err.String())
	}
}

func TestCreateArgumentErrors(t *testing.T) {
	h := newHarness()
	for _, args := range [][]string{{"create", "1"}, {"create", "1", "x"}, {"create", "1", "1"}, {"create", "0", "2"}} {
		if code := h.run(args...); code != ExitUsage {
			t.Errorf("%v code=%d", args, code)
		}
	}
}

func TestCreateReportsAPIErrors(t *testing.T) {
	h := newHarness()
	if code := h.run("create", "8", "9"); code != ExitNotFound {
		t.Fatalf("code=%d", code)
	}
	h.chain(2)
	h.gh.fail["CreateStack"] = apiError(422, "bases do not chain")
	if code := h.run("create", "1", "2"); code != ExitValidation || !strings.Contains(h.err.String(), "bases do not chain") {
		t.Fatalf("code=%d err=%q", code, h.err.String())
	}
	h.gh.setStack(65, []int{1})
	h.gh.fail["GetStack"] = apiError(404, "gone")
	if code := h.run("create", "1", "2"); code != ExitNotFound {
		t.Fatalf("code=%d", code)
	}
}

func TestAddPutsPullRequestsOnTopAndIsIdempotent(t *testing.T) {
	h := newHarness()
	h.chain(4)
	h.gh.setStack(65, []int{1, 2})
	if code := h.run("add", "65", "3", "4"); code != ExitOK || h.out.String() != "added #3 → #4 to stack 65: #1 → #2 → #3 → #4\n" {
		t.Fatalf("code=%d out=%q err=%q", code, h.out.String(), h.err.String())
	}
	if code := h.run("add", "65", "3", "4"); code != ExitOK || !strings.HasPrefix(h.out.String(), "unchanged") {
		t.Fatalf("out=%q", h.out.String())
	}
}

func TestAddArgumentAndAPIErrors(t *testing.T) {
	h := newHarness()
	for _, args := range [][]string{{"add"}, {"add", "x", "1"}, {"add", "65"}, {"add", "65", "y"}} {
		if code := h.run(args...); code != ExitUsage {
			t.Errorf("%v code=%d", args, code)
		}
	}
	if code := h.run("add", "65", "3"); code != ExitNotFound {
		t.Fatalf("code=%d", code)
	}
}

func TestUnstackDissolvesAndSupportsDryRun(t *testing.T) {
	h := newHarness()
	h.chain(2)
	h.gh.setStack(65, []int{1, 2})
	if code := h.run("unstack", "65", "--dry-run"); code != ExitOK || h.out.String() != "would-unstacked stack 65: #1 → #2\n" || h.gh.stacks[65] == nil {
		t.Fatalf("out=%q", h.out.String())
	}
	if code := h.run("unstack", "65", "--json"); code != ExitOK || h.gh.stacks[65] != nil {
		t.Fatalf("code=%d", code)
	}
	var r unstackResult
	if err := json.Unmarshal(h.out.Bytes(), &r); err != nil || r.Action != actionUnstacked {
		t.Fatalf("%+v %v", r, err)
	}
	if code := h.run("unstack", "65"); code != ExitNotFound {
		t.Fatalf("code=%d", code)
	}
}

func TestUnstackErrors(t *testing.T) {
	h := newHarness()
	if code := h.run("unstack", "65", "66"); code != ExitUsage {
		t.Fatalf("code=%d", code)
	}
	h.chain(2)
	h.gh.setStack(65, []int{1, 2})
	h.gh.fail["Unstack"] = apiError(409, "merging")
	if code := h.run("unstack", "65"); code != ExitConflict {
		t.Fatalf("code=%d", code)
	}
}

func TestAdoptFindsTheWholeChainFromAnyLayer(t *testing.T) {
	for _, start := range [][]string{{"adopt", "2"}, {"adopt", "b3"}, {"adopt"}} {
		h := newHarness()
		h.chain(3)
		h.git.branch = "b1"
		if code := h.run(start...); code != ExitOK || h.out.String() != "created stack 101: #1 → #2 → #3\n" {
			t.Fatalf("%v code=%d out=%q err=%q", start, code, h.out.String(), h.err.String())
		}
	}
}

func TestAdoptCompletesAHalfLinkedChain(t *testing.T) {
	h := newHarness()
	h.chain(3)
	h.gh.setStack(65, []int{1, 2})
	if code := h.run("adopt", "3"); code != ExitOK || h.out.String() != "added #3 to stack 65: #1 → #2 → #3\n" {
		t.Fatalf("out=%q err=%q", h.out.String(), h.err.String())
	}
}

func TestAdoptRefusesForksAndLonePullRequests(t *testing.T) {
	h := newHarness()
	h.chain(2)
	h.gh.addPR(9, "b9", "b1")
	if code := h.run("adopt", "1"); code != ExitValidation || !strings.Contains(h.err.String(), "several pull requests on top (#2 → #9)") {
		t.Fatalf("code=%d err=%q", code, h.err.String())
	}
	h = newHarness()
	h.chain(1)
	if code := h.run("adopt", "1"); code != ExitValidation || !strings.Contains(h.err.String(), "#1 has no pull request") {
		t.Fatalf("code=%d err=%q", code, h.err.String())
	}
}

func TestAdoptErrors(t *testing.T) {
	h := newHarness()
	if code := h.run("adopt", "a", "b"); code != ExitUsage {
		t.Fatalf("code=%d", code)
	}
	if code := h.run("adopt", "nobranch"); code != ExitNotFound {
		t.Fatalf("code=%d", code)
	}
	if code := h.run("adopt", "7"); code != ExitNotFound {
		t.Fatalf("code=%d", code)
	}
	h.git.err = fmt.Errorf("detached HEAD")
	if code := h.run("adopt"); code != ExitError {
		t.Fatalf("code=%d", code)
	}
	h = newHarness()
	h.chain(2)
	h.gh.fail["List"] = apiError(422, "bad")
	if code := h.run("adopt", "2"); code != ExitValidation {
		t.Fatalf("code=%d", code)
	}
}
