package handoff

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

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
	repositoryID := strings.Repeat("a", 32)
	writeTestPackage(t, source, repositoryID)
	sourceBytes, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	cfg := clientConfig{Server: server.URL, Token: token}
	id, err := uploadPackage(cfg, source)
	if err != nil {
		t.Fatal(err)
	}
	if err := downloadPackage(cfg, id, destination); err != nil {
		t.Fatal(err)
	}
	destinationBytes, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(destinationBytes, sourceBytes) {
		t.Fatal("downloaded package bytes differ from upload")
	}
	items, err := listHandoffs(cfg, repositoryID, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].ID != id {
		t.Fatalf("unexpected inbox items: %+v", items)
	}
	metadata, err := getHandoffMetadata(cfg, id)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Author != "Walid" || metadata.Message != "Inbox test" {
		t.Fatalf("unexpected metadata: %+v", metadata)
	}
}

func TestDownloadRejectsOversizedContentLength(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", strconv.FormatInt(defaultMaxBytes+1, 10))
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	err := downloadPackage(clientConfig{Server: server.URL, Token: strings.Repeat("t", 32)}, testID, filepath.Join(t.TempDir(), "download"))
	if err == nil || !strings.Contains(err.Error(), "100 MB") {
		t.Fatalf("expected local size rejection, got %v", err)
	}
}
