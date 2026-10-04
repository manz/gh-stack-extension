package stackapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-github/v92/github"
)

func TestNewGitHubSendsThePinnedVersion(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("X-GitHub-Api-Version")
		_, _ = w.Write([]byte(`{"number":65,"pull_requests":[{"number":62}]}`))
	}))
	defer srv.Close()
	base := srv.URL + "/"
	gh, err := NewGitHub(&http.Client{}, github.WithURLs(&base, &base))
	if err != nil {
		t.Fatal(err)
	}
	var svc Service = gh.PullRequests
	s, _, err := svc.GetStack(context.Background(), "manz", "ff4", 65)
	if err != nil || got != APIVersion || s.Number != 65 {
		t.Fatalf("version=%q stack=%+v err=%v", got, s, err)
	}
}

func errResponse(status int) error {
	return &github.ErrorResponse{Response: &http.Response{StatusCode: status}, Message: "nope"}
}

func TestClassifyMapsStatusCodes(t *testing.T) {
	for status, want := range map[int]error{404: ErrNotFound, 409: ErrConflict, 422: ErrValidation} {
		err := Classify(errResponse(status))
		if !errors.Is(err, want) || !strings.Contains(err.Error(), "nope") {
			t.Errorf("status %d: %v", status, err)
		}
	}
}

func TestClassifyPassesOtherErrorsThrough(t *testing.T) {
	other := errResponse(500)
	plain := errors.New("offline")
	if Classify(other) != other || Classify(plain) != plain || Classify(nil) != nil {
		t.Fatal("non-mapped errors must pass through unchanged")
	}
}
