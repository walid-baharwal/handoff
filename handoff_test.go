package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testID = "abcdef123456"

func TestServerUploadDownloadAndCleanup(t *testing.T) {
	dataDir := t.TempDir()
	service, err := newService(serviceConfig{
		Token:       strings.Repeat("t", 32),
		DataDir:     dataDir,
		DownloadDir: t.TempDir(),
		MaxBytes:    8,
		Retention:   time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(service.routes())
	defer server.Close()

	response := request(t, http.MethodPost, server.URL+"/api/v1/handoffs", "bad", []byte("code"))
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated upload status = %d", response.StatusCode)
	}
	response.Body.Close()

	response = request(t, http.MethodPost, server.URL+"/api/v1/handoffs", strings.Repeat("t", 32), []byte("code"))
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("upload status = %d: %s", response.StatusCode, readBody(response.Body))
	}
	var uploaded struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(response.Body).Decode(&uploaded); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if !idPattern.MatchString(uploaded.ID) {
		t.Fatalf("invalid id %q", uploaded.ID)
	}

	response = request(t, http.MethodGet, server.URL+"/api/v1/handoffs/"+uploaded.ID, strings.Repeat("t", 32), nil)
	if response.StatusCode != http.StatusOK || readBody(response.Body) != "code" {
		t.Fatal("download did not return the uploaded bytes")
	}

	response = request(t, http.MethodPost, server.URL+"/api/v1/handoffs", strings.Repeat("t", 32), []byte("too large"))
	if response.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized upload status = %d", response.StatusCode)
	}
	response.Body.Close()

	packagePath := filepath.Join(dataDir, uploaded.ID+".handoff")
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(packagePath, old, old); err != nil {
		t.Fatal(err)
	}
	service.cleanupExpired()
	if _, err := os.Stat(packagePath); !os.IsNotExist(err) {
		t.Fatal("expired package was not removed")
	}
}

func TestClientServerRoundTrip(t *testing.T) {
	token := strings.Repeat("c", 32)
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

	source := filepath.Join(t.TempDir(), "source.handoff")
	destination := filepath.Join(t.TempDir(), "downloaded.handoff")
	writeFile(t, source, "exact package bytes")
	cfg := clientConfig{Server: server.URL, Token: token}
	id, err := uploadPackage(cfg, source)
	if err != nil {
		t.Fatal(err)
	}
	if err := downloadPackage(cfg, id, destination); err != nil {
		t.Fatal(err)
	}
	assertFile(t, destination, "exact package bytes")
}

func TestPackageRejectsChecksumWhenBundleComesFirst(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.handoff")
	metadata := manifest{
		Version:      packageVersion,
		BaseCommit:   strings.Repeat("a", 40),
		Commit:       strings.Repeat("b", 40),
		Ref:          "refs/handoff/outgoing/abcd",
		CreatedAt:    time.Now().UTC(),
		BundleSHA256: strings.Repeat("0", 64),
	}
	manifestBytes, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	writeArchive(t, path, []archiveEntry{
		{name: "changes.bundle", data: []byte("tampered")},
		{name: "manifest.json", data: manifestBytes},
	})
	_, _, err = extractPackage(path, t.TempDir(), 1024)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("expected checksum error, got %v", err)
	}
}

func TestGitHandoffRegression(t *testing.T) {
	t.Run("abort before backup preserves untouched local changes", func(t *testing.T) {
		_, receiver := clonePair(t)
		writeFile(t, filepath.Join(receiver, "app.txt"), "local before backup\n")
		originalHead := git(t, receiver, "rev-parse", "HEAD")
		state := handoffState{
			ID:           testID,
			OriginalHead: originalHead,
			IncomingRef:  "refs/handoff/incoming/" + testID,
			Phase:        phasePrepare,
		}
		if err := saveState(statePath(receiver), state); err != nil {
			t.Fatal(err)
		}
		inDirectory(t, receiver, func() {
			if err := abortHandoff(testID, io.Discard); err != nil {
				t.Fatal(err)
			}
		})
		assertFile(t, filepath.Join(receiver, "app.txt"), "local before backup\n")
		assertNoState(t, receiver)
	})

	t.Run("continue cannot discard an incomplete local restore", func(t *testing.T) {
		_, receiver := clonePair(t)
		writeFile(t, filepath.Join(receiver, "app.txt"), "protected local work\n")
		backupHash := git(t, receiver, "stash", "create", "handoff backup "+testID)
		git(t, receiver, "update-ref", backupRef(testID), backupHash)
		state := handoffState{
			ID:            testID,
			OriginalHead:  git(t, receiver, "rev-parse", "HEAD"),
			IncomingRef:   "refs/handoff/incoming/" + testID,
			StashHash:     backupHash,
			PrivateBackup: true,
			Phase:         phaseRestore,
		}
		if err := saveState(statePath(receiver), state); err != nil {
			t.Fatal(err)
		}
		inDirectory(t, receiver, func() {
			err := continueHandoff(testID, io.Discard)
			if err == nil || !strings.Contains(err.Error(), "handoff abort") {
				t.Fatalf("expected safe abort instruction, got %v", err)
			}
		})
		if got := git(t, receiver, "rev-parse", backupRef(testID)); got != backupHash {
			t.Fatal("continue discarded the local backup")
		}
		inDirectory(t, receiver, func() {
			if err := abortHandoff(testID, io.Discard); err != nil {
				t.Fatal(err)
			}
		})
		assertFile(t, filepath.Join(receiver, "app.txt"), "protected local work\n")
		assertNoState(t, receiver)
	})

	t.Run("clean apply keeps receiver HEAD and sender index unchanged", func(t *testing.T) {
		sender, receiver := clonePair(t)
		writeFile(t, filepath.Join(sender, "app.txt"), "sender change\n")
		writeFile(t, filepath.Join(sender, "new.bin"), string([]byte{0, 1, 2, 3}))
		if err := os.Remove(filepath.Join(sender, "delete.txt")); err != nil {
			t.Fatal(err)
		}
		git(t, sender, "add", "app.txt")
		beforeIndex := git(t, sender, "diff", "--cached", "--name-only")
		packagePath := buildFrom(t, sender, nil)
		if got := git(t, sender, "diff", "--cached", "--name-only"); got != beforeIndex {
			t.Fatalf("push changed sender index: before %q after %q", beforeIndex, got)
		}
		beforeHead := git(t, receiver, "rev-parse", "HEAD")
		applyFrom(t, receiver, packagePath, false)
		if got := git(t, receiver, "rev-parse", "HEAD"); got != beforeHead {
			t.Fatal("pull created a commit")
		}
		assertFile(t, filepath.Join(receiver, "app.txt"), "sender change\n")
		assertFile(t, filepath.Join(receiver, "new.bin"), string([]byte{0, 1, 2, 3}))
		if _, err := os.Stat(filepath.Join(receiver, "delete.txt")); !os.IsNotExist(err) {
			t.Fatal("deleted file still exists")
		}
		if status := git(t, receiver, "status", "--porcelain"); status == "" {
			t.Fatal("applied changes should remain uncommitted")
		}
	})

	t.Run("selected paths exclude other changes", func(t *testing.T) {
		sender, receiver := clonePair(t)
		writeFile(t, filepath.Join(sender, "app.txt"), "selected\n")
		writeFile(t, filepath.Join(sender, "delete.txt"), "not selected\n")
		packagePath := buildFrom(t, sender, []string{"app.txt"})
		applyFrom(t, receiver, packagePath, false)
		assertFile(t, filepath.Join(receiver, "app.txt"), "selected\n")
		assertFile(t, filepath.Join(receiver, "delete.txt"), "delete me\n")
	})

	t.Run("dirty receiver merges non-overlapping local work", func(t *testing.T) {
		sender, receiver := clonePair(t)
		writeFile(t, filepath.Join(sender, "app.txt"), "sender\n")
		packagePath := buildFrom(t, sender, nil)
		writeFile(t, filepath.Join(receiver, "local.txt"), "receiver\n")
		applyFrom(t, receiver, packagePath, false)
		assertFile(t, filepath.Join(receiver, "app.txt"), "sender\n")
		assertFile(t, filepath.Join(receiver, "local.txt"), "receiver\n")
		if stash := git(t, receiver, "stash", "list"); stash != "" {
			t.Fatalf("backup stash was not removed: %s", stash)
		}
	})

	t.Run("untracked files remain in place while tracked work is backed up", func(t *testing.T) {
		sender, receiver := clonePair(t)
		writeFile(t, filepath.Join(sender, "app.txt"), "sender\n")
		packagePath := buildFrom(t, sender, nil)

		writeFile(t, filepath.Join(receiver, "delete.txt"), "receiver\n")
		logDir := filepath.Join(receiver, "runtime")
		if err := os.Mkdir(logDir, 0o700); err != nil {
			t.Fatal(err)
		}
		logPath := filepath.Join(logDir, "active.log")
		writeFile(t, logPath, "still running\n")
		activeLog, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer activeLog.Close()
		if err := os.Chmod(logDir, 0o500); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(logDir, 0o700)

		applyFrom(t, receiver, packagePath, false)
		assertFile(t, filepath.Join(receiver, "app.txt"), "sender\n")
		assertFile(t, filepath.Join(receiver, "delete.txt"), "receiver\n")
		assertFile(t, logPath, "still running\n")
		if stash := git(t, receiver, "stash", "list"); stash != "" {
			t.Fatalf("backup stash was not removed: %s", stash)
		}
	})

	t.Run("untracked collision restores tracked local work", func(t *testing.T) {
		sender, receiver := clonePair(t)
		writeFile(t, filepath.Join(sender, "collision.txt"), "incoming\n")
		packagePath := buildFrom(t, sender, nil)

		writeFile(t, filepath.Join(receiver, "collision.txt"), "local untracked\n")
		writeFile(t, filepath.Join(receiver, "delete.txt"), "local tracked\n")
		beforeHead := git(t, receiver, "rev-parse", "HEAD")
		err := applyFrom(t, receiver, packagePath, true)
		if err == nil || !strings.Contains(err.Error(), "would be overwritten") {
			t.Fatalf("expected untracked collision, got %v", err)
		}
		if got := git(t, receiver, "rev-parse", "HEAD"); got != beforeHead {
			t.Fatal("failed pull changed receiver HEAD")
		}
		assertFile(t, filepath.Join(receiver, "collision.txt"), "local untracked\n")
		assertFile(t, filepath.Join(receiver, "delete.txt"), "local tracked\n")
		assertNoState(t, receiver)
		if stash := git(t, receiver, "stash", "list"); stash != "" {
			t.Fatalf("failed pull left a stash: %s", stash)
		}
	})

	t.Run("existing user stash is untouched", func(t *testing.T) {
		sender, receiver := clonePair(t)
		writeFile(t, filepath.Join(sender, "app.txt"), "sender\n")
		packagePath := buildFrom(t, sender, nil)

		writeFile(t, filepath.Join(receiver, "app.txt"), "saved for later\n")
		git(t, receiver, "stash", "push", "--message", "user stash")
		before := git(t, receiver, "stash", "list", "--format=%H")
		writeFile(t, filepath.Join(receiver, "delete.txt"), "current local work\n")
		applyFrom(t, receiver, packagePath, false)
		if after := git(t, receiver, "stash", "list", "--format=%H"); after != before {
			t.Fatalf("user stash changed: before %q after %q", before, after)
		}
		assertFile(t, filepath.Join(receiver, "app.txt"), "sender\n")
		assertFile(t, filepath.Join(receiver, "delete.txt"), "current local work\n")
	})

	t.Run("stash conflict can continue without a commit", func(t *testing.T) {
		sender, receiver := clonePair(t)
		writeFile(t, filepath.Join(sender, "app.txt"), "sender\n")
		packagePath := buildFrom(t, sender, nil)
		writeFile(t, filepath.Join(receiver, "app.txt"), "receiver\n")
		beforeHead := git(t, receiver, "rev-parse", "HEAD")
		err := applyFrom(t, receiver, packagePath, true)
		if err == nil || !strings.Contains(err.Error(), "conflict") {
			t.Fatalf("expected conflict, got %v", err)
		}
		writeFile(t, filepath.Join(receiver, "app.txt"), "resolved\n")
		git(t, receiver, "add", "app.txt")
		inDirectory(t, receiver, func() {
			if err := continueHandoff(testID, io.Discard); err != nil {
				t.Fatal(err)
			}
		})
		if got := git(t, receiver, "rev-parse", "HEAD"); got != beforeHead {
			t.Fatal("continue created a commit")
		}
		assertFile(t, filepath.Join(receiver, "app.txt"), "resolved\n")
		assertNoState(t, receiver)
	})

	t.Run("incoming conflict abort restores receiver exactly", func(t *testing.T) {
		sender, receiver := clonePair(t)
		writeFile(t, filepath.Join(sender, "app.txt"), "sender\n")
		writeFile(t, filepath.Join(sender, "incoming.txt"), "incoming\n")
		packagePath := buildFrom(t, sender, nil)

		writeFile(t, filepath.Join(receiver, "app.txt"), "receiver committed\n")
		git(t, receiver, "add", "app.txt")
		git(t, receiver, "commit", "-m", "receiver advance")
		writeFile(t, filepath.Join(receiver, "local.txt"), "local uncommitted\n")
		beforeHead := git(t, receiver, "rev-parse", "HEAD")
		err := applyFrom(t, receiver, packagePath, true)
		if err == nil || !strings.Contains(err.Error(), "conflict") {
			t.Fatalf("expected conflict, got %v", err)
		}
		inDirectory(t, receiver, func() {
			if err := abortHandoff(testID, io.Discard); err != nil {
				t.Fatal(err)
			}
		})
		if got := git(t, receiver, "rev-parse", "HEAD"); got != beforeHead {
			t.Fatal("abort changed receiver HEAD")
		}
		assertFile(t, filepath.Join(receiver, "app.txt"), "receiver committed\n")
		assertFile(t, filepath.Join(receiver, "local.txt"), "local uncommitted\n")
		if _, err := os.Stat(filepath.Join(receiver, "incoming.txt")); !os.IsNotExist(err) {
			t.Fatal("abort left an incoming-only file")
		}
		assertNoState(t, receiver)
	})
}

func clonePair(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	origin := filepath.Join(root, "origin.git")
	git(t, root, "init", "--bare", origin)
	seed := filepath.Join(root, "seed")
	git(t, root, "-c", "core.autocrlf=false", "clone", origin, seed)
	configureGit(t, seed)
	writeFile(t, filepath.Join(seed, "app.txt"), "base\n")
	writeFile(t, filepath.Join(seed, "delete.txt"), "delete me\n")
	git(t, seed, "add", ".")
	git(t, seed, "commit", "-m", "base")
	git(t, seed, "push", "origin", "HEAD:main")
	git(t, origin, "symbolic-ref", "HEAD", "refs/heads/main")
	sender := filepath.Join(root, "sender")
	receiver := filepath.Join(root, "receiver")
	git(t, root, "-c", "core.autocrlf=false", "clone", origin, sender)
	git(t, root, "-c", "core.autocrlf=false", "clone", origin, receiver)
	configureGit(t, sender)
	configureGit(t, receiver)
	return sender, receiver
}

func configureGit(t *testing.T, dir string) {
	t.Helper()
	git(t, dir, "config", "user.name", "Handoff Test")
	git(t, dir, "config", "user.email", "handoff@example.test")
	git(t, dir, "config", "core.autocrlf", "false")
}

func buildFrom(t *testing.T, sender string, paths []string) string {
	t.Helper()
	packagePath := filepath.Join(t.TempDir(), "changes.handoff")
	inDirectory(t, sender, func() {
		if _, err := buildHandoffPackage(packagePath, "test", paths); err != nil {
			t.Fatal(err)
		}
	})
	return packagePath
}

func applyFrom(t *testing.T, receiver, packagePath string, wantError bool) error {
	t.Helper()
	var applyErr error
	inDirectory(t, receiver, func() {
		applyErr = applyHandoffPackage(testID, packagePath, t.TempDir(), io.Discard)
	})
	if !wantError && applyErr != nil {
		t.Fatal(applyErr)
	}
	return applyErr
}

func inDirectory(t *testing.T, dir string, run func()) {
	t.Helper()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(previous); err != nil {
			t.Fatal(err)
		}
	}()
	run()
}

func git(t *testing.T, dir string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = dir
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	if err != nil {
		t.Fatalf("git %s: %v\n%s%s", strings.Join(args, " "), err, stderr.String(), stdout.String())
	}
	return strings.TrimSpace(stdout.String())
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func assertFile(t *testing.T, path, want string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != want {
		t.Fatalf("%s = %q, want %q", path, data, want)
	}
}

func assertNoState(t *testing.T, repo string) {
	t.Helper()
	if _, err := os.Stat(statePath(repo)); !os.IsNotExist(err) {
		t.Fatalf("handoff state remains: %v", err)
	}
}

func request(t *testing.T, method, url, token string, body []byte) *http.Response {
	t.Helper()
	request, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	return response
}

func readBody(body io.ReadCloser) string {
	defer body.Close()
	data, _ := io.ReadAll(body)
	return string(data)
}

type archiveEntry struct {
	name string
	data []byte
}

func writeArchive(t *testing.T, path string, entries []archiveEntry) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, entry := range entries {
		if err := tarWriter.WriteHeader(&tar.Header{Name: entry.name, Mode: 0o600, Size: int64(len(entry.data))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarWriter.Write(entry.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func Example_usage() {
	fmt.Println("handoff push -m backend-ready")
	fmt.Println("handoff pull abcdef123456")
	// Output:
	// handoff push -m backend-ready
	// handoff pull abcdef123456
}
