package cli

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/go-github/v92/github"
)

func stacked3() *harness {
	h := newHarness()
	h.chain(3)
	h.gh.setStack(65, []int{1, 2, 3})
	return h
}

func TestMergeDryRunListsWhatLands(t *testing.T) {
	h := stacked3()
	h.gh.stacks[65].PullRequests[0].MergedAt = &github.Timestamp{}
	if code := h.run("merge", "2", "--repo", "manz/ff4", "--dry-run"); code != ExitOK || h.out.String() != "would merge manz/ff4 #2\n" {
		t.Fatalf("code=%d out=%q", code, h.out.String())
	}
	h = stacked3()
	h.git.branch = "b3"
	if code := h.run("merge", "--dry-run"); code != ExitOK || h.out.String() != "would merge manz/ff4 #1 → #2 → #3\n" {
		t.Fatalf("out=%q err=%q", h.out.String(), h.err.String())
	}
	for _, c := range h.gh.calls {
		if strings.HasPrefix(c, "MergeAsync") {
			t.Fatal("dry run merged")
		}
	}
}

func TestMergeWaitsUntilMerged(t *testing.T) {
	h := stacked3()
	h.gh.merges = []string{"pending", "pending", "merged"}
	if code := h.run("merge", "3", "--repo", "manz/ff4", "--wait", "--method", "squash", "--direct", "--interval", "2s", "--json"); code != ExitOK {
		t.Fatalf("code=%d err=%q", code, h.err.String())
	}
	var r mergeResult
	if err := json.Unmarshal(h.out.Bytes(), &r); err != nil || r.Status != statusMerged || r.UUID != "u-1" || len(r.PullRequests) != 3 {
		t.Fatalf("%+v %v", r, err)
	}
	if len(h.slept) != 2 || h.slept[0] != 2*time.Second || h.gh.calls[len(h.gh.calls)-3] != "MergeAsync 3 squash direct_merge" {
		t.Fatalf("slept=%v calls=%v", h.slept, h.gh.calls)
	}
}

func TestMergeWithoutWaitReportsPendingAndQueue(t *testing.T) {
	h := stacked3()
	if code := h.run("merge", "1", "--repo", "manz/ff4", "--queue"); code != ExitOK || h.out.String() != "pending: manz/ff4 #1\n" || h.gh.calls[len(h.gh.calls)-1] != "MergeAsync 1  merge_queue" {
		t.Fatalf("out=%q calls=%v", h.out.String(), h.gh.calls)
	}
	h = stacked3()
	h.gh.merges = []string{"enqueued"}
	if code := h.run("merge", "2", "--repo", "manz/ff4", "--wait"); code != ExitOK || !strings.HasPrefix(h.out.String(), "enqueued: manz/ff4 #1 → #2") || len(h.slept) != 0 {
		t.Fatalf("out=%q", h.out.String())
	}
}

func TestMergeFailureExitsWithItsCode(t *testing.T) {
	h := stacked3()
	h.gh.merges = []string{"pending", "failed"}
	h.gh.mergeMsg = "required check failed"
	if code := h.run("merge", "2", "--repo", "manz/ff4", "--wait"); code != ExitMergeFailed || !strings.Contains(h.out.String(), "failed: manz/ff4 #1 → #2 (required check failed)") {
		t.Fatalf("code=%d out=%q", code, h.out.String())
	}
}

func TestMergeTimesOut(t *testing.T) {
	h := stacked3()
	if code := h.run("merge", "1", "--repo", "manz/ff4", "--wait", "--timeout", "-1s"); code != ExitError || !strings.Contains(h.err.String(), "still pending") {
		t.Fatalf("code=%d err=%q", code, h.err.String())
	}
}

func TestMergeArgumentErrors(t *testing.T) {
	h := stacked3()
	for _, args := range [][]string{
		{"merge", "1", "--repo", "manz/ff4", "--method", "fast-forward"}, {"merge", "1", "--repo", "manz/ff4", "--queue", "--direct"}, {"merge", "x", "--repo", "manz/ff4"}, {"merge", "1", "2", "--repo", "manz/ff4"},
	} {
		if code := h.run(args...); code != ExitUsage {
			t.Errorf("%v code=%d", args, code)
		}
	}
}

func TestMergeAPIErrors(t *testing.T) {
	h := stacked3()
	h.gh.fail["MergeAsync"] = apiError(409, "merge already pending")
	if code := h.run("merge", "1", "--repo", "manz/ff4"); code != ExitConflict || !strings.Contains(h.err.String(), "merge already pending") {
		t.Fatalf("code=%d err=%q", code, h.err.String())
	}
	h = stacked3()
	h.gh.fail["GetMergeAsyncResult"] = apiError(404, "expired")
	if code := h.run("merge", "1", "--repo", "manz/ff4", "--wait"); code != ExitNotFound {
		t.Fatalf("code=%d", code)
	}
	h = stacked3()
	h.gh.fail["GetStack"] = apiError(404, "gone")
	if code := h.run("merge", "1", "--repo", "manz/ff4"); code != ExitNotFound {
		t.Fatalf("code=%d", code)
	}
	if code := h.run("merge", "77", "--repo", "manz/ff4"); code != ExitNotFound {
		t.Fatalf("code=%d", code)
	}
}

func TestMergeCurrentBranchErrors(t *testing.T) {
	h := stacked3()
	h.git.branch = "nopr"
	if code := h.run("merge"); code != ExitNotFound {
		t.Fatalf("code=%d", code)
	}
	h.git.err = errors.New("detached")
	if code := h.run("merge"); code != ExitError {
		t.Fatalf("code=%d", code)
	}
	h = stacked3()
	h.git.branch = "b1"
	h.gh.fail["List"] = apiError(422, "x")
	if code := h.run("merge"); code != ExitValidation {
		t.Fatalf("code=%d", code)
	}
}

func TestMergeALonePullRequest(t *testing.T) {
	h := newHarness()
	h.gh.addPR(9, "solo", "main")
	h.gh.merges = []string{"merged"}
	if code := h.run("merge", "9", "--repo", "manz/ff4"); code != ExitOK || h.out.String() != "merged: manz/ff4 #9\n" {
		t.Fatalf("code=%d out=%q", code, h.out.String())
	}
}

func TestSleepDefaultsToTimeSleep(t *testing.T) {
	e := &env{}
	start := time.Now()
	e.sleep(time.Millisecond)
	if time.Since(start) < time.Millisecond {
		t.Fatal("did not sleep")
	}
}

func TestMergeWaitsThroughGitHubsAcceptedAnswers(t *testing.T) {
	h := stacked3()
	h.gh.accept = true // every answer is an HTTP 202, as GitHub sends it
	h.gh.merges = []string{"pending", "pending", "merged"}
	if code := h.run("merge", "3", "--repo", "manz/ff4", "--wait"); code != ExitOK || h.out.String() != "merged: manz/ff4 #1 → #2 → #3\n" || len(h.slept) != 2 {
		t.Fatalf("code=%d out=%q err=%q slept=%v", code, h.out.String(), h.err.String(), h.slept)
	}
}

func TestMergeReportsAnUnreadableAcceptedBody(t *testing.T) {
	h := stacked3()
	h.gh.accept = true
	h.gh.acceptRaw = "not json"
	if code := h.run("merge", "3", "--repo", "manz/ff4"); code != ExitError || !strings.Contains(h.err.String(), "reading the accepted merge request") {
		t.Fatalf("code=%d err=%q", code, h.err.String())
	}
}

func TestMergeOfANumberRequiresTheRepository(t *testing.T) {
	h := stacked3()
	if code := h.run("merge", "3", "--wait"); code != ExitUsage || !strings.Contains(h.err.String(), "merge 3 needs --repo OWNER/REPO") {
		t.Fatalf("code=%d err=%q", code, h.err.String())
	}
	for _, c := range h.gh.calls {
		if strings.HasPrefix(c, "MergeAsync") {
			t.Fatal("merged without --repo")
		}
	}
	h.git.branch = "b3" // the current branch's pull request needs no --repo
	h.gh.merges = []string{"merged"}
	if code := h.run("merge"); code != ExitOK || h.out.String() != "merged: manz/ff4 #1 → #2 → #3\n" {
		t.Fatalf("code=%d out=%q err=%q", code, h.out.String(), h.err.String())
	}
}
