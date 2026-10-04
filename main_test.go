package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"testing"
)

type fakeClient struct {
	body string
	err  error
}

func (f fakeClient) Get(_ string, response interface{}) error {
	if f.err != nil {
		return f.err
	}
	return json.Unmarshal([]byte(f.body), response)
}

func clientOf(c restClient, err error) func() (restClient, error) {
	return func() (restClient, error) { return c, err }
}

func TestRunPrintsTheLogin(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run(&out, &errOut, clientOf(fakeClient{body: `{"login":"manz"}`}, nil))
	if code != 0 || out.String() != "running as manz\n" {
		t.Fatalf("code=%d out=%q err=%q", code, out.String(), errOut.String())
	}
}

func TestRunReportsAClientError(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run(&out, &errOut, clientOf(nil, errors.New("no token")))
	if code != 1 || errOut.String() != "no token\n" {
		t.Fatalf("code=%d err=%q", code, errOut.String())
	}
}

func TestRunReportsAnAPIError(t *testing.T) {
	var out, errOut bytes.Buffer
	code := run(&out, &errOut, clientOf(fakeClient{err: errors.New("HTTP 401")}, nil))
	if code != 1 || errOut.String() != "HTTP 401\n" {
		t.Fatalf("code=%d err=%q", code, errOut.String())
	}
}
