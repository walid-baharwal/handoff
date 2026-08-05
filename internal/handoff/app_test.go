package handoff

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunHelpVersionAndUnknownCommand(t *testing.T) {
	oldVersion := Version
	Version = "test-version"
	defer func() { Version = oldVersion }()

	var output bytes.Buffer
	if err := Run([]string{"version"}, strings.NewReader(""), &output, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if output.String() != "test-version\n" {
		t.Fatalf("version output = %q", output.String())
	}

	output.Reset()
	if err := Run(nil, strings.NewReader(""), &output, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "handoff push [-m MESSAGE] [--dry-run]") || !strings.Contains(output.String(), "handoff pull [--dry-run] [--yes] [ID]") {
		t.Fatalf("usage output = %q", output.String())
	}

	if err := Run([]string{"unknown"}, strings.NewReader(""), &output, &bytes.Buffer{}); err == nil {
		t.Fatal("unknown command was accepted")
	}
}

func TestNormalizeServerURL(t *testing.T) {
	valid := map[string]string{
		"https://handoff.example.com/": "https://handoff.example.com",
		"http://localhost:8080":        "http://localhost:8080",
		"http://127.0.0.1:8080":        "http://127.0.0.1:8080",
	}
	for input, want := range valid {
		got, err := normalizeServerURL(input)
		if err != nil || got != want {
			t.Fatalf("normalizeServerURL(%q) = %q, %v", input, got, err)
		}
	}
	for _, input := range []string{"http://example.com", "https://example.com/path", "not-a-url"} {
		if _, err := normalizeServerURL(input); err == nil {
			t.Fatalf("accepted invalid server URL %q", input)
		}
	}
}

func TestSecureTokenEqual(t *testing.T) {
	token := strings.Repeat("a", 32)
	if !secureTokenEqual(token, "Bearer "+token) {
		t.Fatal("valid bearer token was rejected")
	}
	for _, authorization := range []string{"", token, "Bearer wrong", "Basic " + token} {
		if secureTokenEqual(token, authorization) {
			t.Fatalf("accepted invalid authorization %q", authorization)
		}
	}
}
