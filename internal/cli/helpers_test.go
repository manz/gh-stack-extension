package cli

import "github.com/manz/gh-stack-extension/internal/stackapi"

func classifyForTest(err error) error { return stackapi.Classify(err) }

func githubPtr(s string) *string { return &s }
