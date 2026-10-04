package cli

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/go-github/v92/github"
)

func TestNoArgumentsPrintsUsage(t *testing.T) {
	h := newHarness()
	if code := h.run(); code != ExitUsage || !strings.Contains(h.out.String(), "list") {
		t.Fatalf("code=%d out=%q", code, h.out.String())
	}
	if code := h.run("--help"); code != ExitOK {
		t.Fatalf("help code=%d", code)
	}
}

func TestUnknownCommand(t *testing.T) {
	h := newHarness()
	if code := h.run("frobnicate"); code != ExitUsage || !strings.Contains(h.err.String(), "unknown command") {
		t.Fatalf("code=%d err=%q", code, h.err.String())
	}
}

func TestBadFlagsAndHelpFlag(t *testing.T) {
	h := newHarness()
	if code := h.run("list", "--nope"); code != ExitUsage {
		t.Fatalf("code=%d", code)
	}
	if code := h.run("list", "-h"); code != ExitOK {
		t.Fatalf("code=%d", code)
	}
}

func TestRepoResolutionFailure(t *testing.T) {
	h := newHarness()
	if code := h.run("list", "--repo", "bad"); code != ExitError || !strings.Contains(h.err.String(), "bad repo") {
		t.Fatalf("code=%d err=%q", code, h.err.String())
	}
}

func TestListPrintsEveryStackAcrossPages(t *testing.T) {
	h := newHarness()
	h.chain(4)
	h.gh.setStack(65, []int{1, 2})
	h.gh.setStack(66, []int{3, 4})
	h.gh.pageSize = 1
	if code := h.run("list"); code != ExitOK {
		t.Fatalf("code=%d err=%q", code, h.err.String())
	}
	want := "stack 65 (open) onto main: #1 → #2\nstack 66 (open) onto b2: #3 → #4\n"
	if h.out.String() != want {
		t.Fatalf("out=%q", h.out.String())
	}
	if code := h.run("list", "--json"); code != ExitOK {
		t.Fatal(code)
	}
	var stacks []github.PullRequestStackMinimal
	if err := json.Unmarshal(h.out.Bytes(), &stacks); err != nil || len(stacks) != 2 || stacks[1].Number != 66 {
		t.Fatalf("json=%s err=%v", h.out.String(), err)
	}
}

func TestListRejectsArgumentsAndReportsAPIErrors(t *testing.T) {
	h := newHarness()
	if code := h.run("list", "x"); code != ExitUsage {
		t.Fatalf("code=%d", code)
	}
	h.gh.fail["ListStacks"] = apiError(404, "Not Found")
	if code := h.run("list"); code != ExitNotFound {
		t.Fatalf("code=%d", code)
	}
}

func TestViewByNumberPRAndCurrentBranch(t *testing.T) {
	h := newHarness()
	h.chain(3)
	h.gh.setStack(65, []int{1, 2, 3})
	h.gh.prs[2].Draft = github.Ptr(true)
	h.gh.stacks[65].PullRequests[1].Draft = true
	for _, args := range [][]string{{"view", "65"}, {"view", "--pr", "2"}} {
		if code := h.run(args...); code != ExitOK {
			t.Fatalf("%v code=%d err=%q", args, code, h.err.String())
		}
		if !strings.Contains(h.out.String(), "stack 65 (open) onto main: #1 → #2 → #3") || !strings.Contains(h.out.String(), "2. #2 b2 [draft]") {
			t.Fatalf("%v out=%q", args, h.out.String())
		}
	}
	h.git.branch = "b3"
	if code := h.run("view", "--json"); code != ExitOK {
		t.Fatalf("code=%d err=%q", code, h.err.String())
	}
	var s github.PullRequestStackDetails
	if err := json.Unmarshal(h.out.Bytes(), &s); err != nil || s.Number != 65 {
		t.Fatalf("json=%s err=%v", h.out.String(), err)
	}
}

func TestViewArgumentErrors(t *testing.T) {
	h := newHarness()
	for _, args := range [][]string{{"view", "1", "2"}, {"view", "x"}, {"view", "0"}, {"view", "5", "--pr", "1"}} {
		if code := h.run(args...); code != ExitUsage {
			t.Errorf("%v code=%d", args, code)
		}
	}
}

func TestViewWhenNothingIsStacked(t *testing.T) {
	h := newHarness()
	h.chain(1)
	if code := h.run("view", "--pr", "1"); code != ExitNotFound || !strings.Contains(h.err.String(), "not in a stack") {
		t.Fatalf("code=%d err=%q", code, h.err.String())
	}
	h.git.branch = "lonely"
	if code := h.run("view"); code != ExitNotFound || !strings.Contains(h.err.String(), "no open pull request for branch lonely") {
		t.Fatalf("code=%d err=%q", code, h.err.String())
	}
	h.git.err = errors.New("not a git repository")
	if code := h.run("view"); code != ExitError {
		t.Fatalf("code=%d", code)
	}
}

func TestViewReportsAPIErrors(t *testing.T) {
	h := newHarness()
	if code := h.run("view", "9"); code != ExitNotFound {
		t.Fatalf("code=%d", code)
	}
	if code := h.run("view", "--pr", "9"); code != ExitNotFound {
		t.Fatalf("code=%d", code)
	}
	h.gh.fail["List"] = apiError(422, "bad head")
	h.git.branch = "b1"
	if code := h.run("view"); code != ExitValidation {
		t.Fatalf("code=%d", code)
	}
}

func TestExitCodes(t *testing.T) {
	if exitCode(errMergeFailed) != ExitMergeFailed || exitCode(apiErrorClassified(409)) != ExitConflict || exitCode(errors.New("x")) != ExitError {
		t.Fatal("exit code mapping")
	}
}

func TestFlagsMayFollowArguments(t *testing.T) {
	h := newHarness()
	h.chain(1)
	h.gh.setStack(65, []int{1})
	if code := h.run("view", "65", "--json"); code != ExitOK || !strings.HasPrefix(h.out.String(), "{") {
		t.Fatalf("code=%d out=%q err=%q", code, h.out.String(), h.err.String())
	}
	if code := h.run("view", "--", "-3"); code != ExitUsage || !strings.Contains(h.err.String(), `not a stack number: "-3"`) {
		t.Fatalf("code=%d err=%q", code, h.err.String())
	}
}
