package handoff

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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

func writeTestPackage(t *testing.T, path, repositoryID string) manifest {
	t.Helper()
	bundlePath := filepath.Join(t.TempDir(), "changes.bundle")
	writeFile(t, bundlePath, "test bundle bytes")
	metadata := manifest{
		BaseCommit:   strings.Repeat("a", 40),
		Commit:       strings.Repeat("b", 40),
		Ref:          "refs/handoff/outgoing/abcd",
		CreatedAt:    time.Now().UTC(),
		Message:      "Inbox test",
		Author:       "Walid",
		AuthorEmail:  "walid@example.test",
		Project:      "handoff",
		RepositoryID: repositoryID,
		Branch:       "feature/inbox",
		FileCount:    2,
		Files:        []string{"app.go", "server.go"},
	}
	if err := createPackage(path, bundlePath, metadata); err != nil {
		t.Fatal(err)
	}
	return metadata
}
