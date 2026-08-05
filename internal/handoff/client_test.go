package handoff

import (
	"net/http"
	"net/http/httptest"
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
