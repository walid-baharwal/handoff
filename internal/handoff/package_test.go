package handoff

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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

func TestManifestValidation(t *testing.T) {
	valid := manifest{
		Version:    packageVersion,
		BaseCommit: strings.Repeat("a", 40),
		Commit:     strings.Repeat("b", 40),
		Ref:        "refs/handoff/outgoing/abcd",
	}
	if err := validateManifest(valid); err != nil {
		t.Fatal(err)
	}

	cases := []manifest{
		{Version: 2, BaseCommit: valid.BaseCommit, Commit: valid.Commit, Ref: valid.Ref},
		{Version: packageVersion, BaseCommit: "bad", Commit: valid.Commit, Ref: valid.Ref},
		{Version: packageVersion, BaseCommit: valid.BaseCommit, Commit: valid.Commit, Ref: "refs/heads/main"},
	}
	for _, value := range cases {
		if err := validateManifest(value); err == nil {
			t.Fatalf("accepted invalid manifest: %+v", value)
		}
	}
}

func TestCreatePackageRejectsOversizedMetadata(t *testing.T) {
	bundlePath := filepath.Join(t.TempDir(), "changes.bundle")
	writeFile(t, bundlePath, "bundle")
	files := make([]string, maxListedFiles)
	for index := range files {
		files[index] = strings.Repeat("x", 400) + string(rune('a'+index%26))
	}
	metadata := manifest{
		BaseCommit: strings.Repeat("a", 40),
		Commit:     strings.Repeat("b", 40),
		Ref:        "refs/handoff/outgoing/abcd",
		FileCount:  len(files),
		Files:      files,
	}
	err := createPackage(filepath.Join(t.TempDir(), "large.handoff"), bundlePath, metadata)
	if err == nil || !strings.Contains(err.Error(), "64 KiB") {
		t.Fatalf("expected metadata size rejection, got %v", err)
	}
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
