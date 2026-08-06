package handoff

import (
	"bytes"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestInteractiveInboxPullEndToEnd(t *testing.T) {
	token := strings.Repeat("i", 32)
	service, err := newService(serviceConfig{
		Token:       token,
		DataDir:     t.TempDir(),
		DownloadDir: t.TempDir(),
		MaxBytes:    defaultMaxBytes,
		Retention:   time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(service.routes())
	defer server.Close()

	configRoot := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Setenv("APPDATA", configRoot)
	} else {
		t.Setenv("XDG_CONFIG_HOME", configRoot)
	}
	configFile, err := configPath()
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSONAtomic(configFile, clientConfig{Server: server.URL, Token: token}, 0o600); err != nil {
		t.Fatal(err)
	}

	sender, receiver := clonePair(t)
	writeFile(t, filepath.Join(sender, "app.txt"), "from inbox\n")
	packagePath := buildFrom(t, sender, nil)
	id, err := uploadPackage(clientConfig{Server: server.URL, Token: token}, packagePath)
	if err != nil {
		t.Fatal(err)
	}

	var cancelled bytes.Buffer
	inDirectory(t, receiver, func() {
		err = runPull([]string{id}, strings.NewReader("no\n"), &cancelled)
	})
	if err != nil {
		t.Fatal(err)
	}
	assertFile(t, filepath.Join(receiver, "app.txt"), "base\n")
	if !strings.Contains(cancelled.String(), "Pull cancelled.") {
		t.Fatalf("direct pull did not request confirmation:\n%s", cancelled.String())
	}

	var output bytes.Buffer
	inDirectory(t, receiver, func() {
		err = runPull(nil, strings.NewReader("1\nyes\n"), &output)
	})
	if err != nil {
		t.Fatal(err)
	}
	assertFile(t, filepath.Join(receiver, "app.txt"), "from inbox\n")
	if !strings.Contains(output.String(), "RECENT HANDOFFS FOR") ||
		!strings.Contains(output.String(), "From: Handoff Test (self-reported)") ||
		!strings.Contains(output.String(), "Handoff "+id+" applied successfully") {
		t.Fatalf("unexpected interactive output:\n%s", output.String())
	}
}

func TestJSONPullRequiresExplicitYesWithoutPrompting(t *testing.T) {
	var output bytes.Buffer
	err := runPull([]string{"--json", testID}, strings.NewReader(""), &output)
	if err == nil || !strings.Contains(err.Error(), "--yes is required") {
		t.Fatalf("expected explicit --yes error, got %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("JSON pull wrote an interactive prompt: %q", output.String())
	}
}

func TestInteractivePushDryRunDoesNotRequireServerConfiguration(t *testing.T) {
	sender, _ := clonePair(t)
	writeFile(t, filepath.Join(sender, "app.txt"), "selected preview\n")
	writeFile(t, filepath.Join(sender, "delete.txt"), "excluded preview\n")
	var output bytes.Buffer
	inDirectory(t, sender, func() {
		err := runPush(
			[]string{"--dry-run", "--interactive", "--exclude", "delete.txt", "-m", "preview"},
			strings.NewReader("all\n"),
			&output,
		)
		if err != nil {
			t.Fatal(err)
		}
	})
	for _, expected := range []string{
		"CHANGED PATHS",
		"app.txt",
		"HANDOFF PUSH PREVIEW",
		"Selection: all changes",
		"Message: preview",
		"Files: 1",
		"Dry run complete; no files were uploaded.",
	} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("dry-run output is missing %q:\n%s", expected, output.String())
		}
	}
	if strings.Contains(output.String(), "excluded preview") {
		t.Fatalf("dry-run output exposed excluded content:\n%s", output.String())
	}
	if staged := git(t, sender, "diff", "--cached", "--name-only"); staged != "" {
		t.Fatalf("dry run changed the sender index: %q", staged)
	}
}

func TestPushRejectsConflictingModes(t *testing.T) {
	err := runPush([]string{"--dry-run", "--staged", "--worktree"}, strings.NewReader(""), &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "cannot be used together") {
		t.Fatalf("expected conflicting mode error, got %v", err)
	}
}
