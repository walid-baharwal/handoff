package handoff

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

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
