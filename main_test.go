package main

import (
	"bytes"
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestRunReportsAnHTTPClientError(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{"list"}, &out, &errOut, func() (*http.Client, error) { return nil, errors.New("no token") })
	if code != 1 || errOut.String() != "no token\n" {
		t.Fatalf("code=%d err=%q", code, errOut.String())
	}
}

func TestRunDispatchesToTheCLI(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run([]string{"help"}, &out, &errOut, func() (*http.Client, error) { return &http.Client{}, nil })
	if code != 0 || !strings.Contains(out.String(), "usage:") {
		t.Fatalf("code=%d out=%q", code, out.String())
	}
}

func TestResolveRepoParsesTheOverride(t *testing.T) {
	owner, name, err := resolveRepo("manz/ff4")
	if err != nil || owner != "manz" || name != "ff4" {
		t.Fatalf("%s/%s err=%v", owner, name, err)
	}
	if _, _, err := resolveRepo("not a repo"); err == nil {
		t.Fatal("expected a parse error")
	}
}
