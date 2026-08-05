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
		MaxBytes:    4096,
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

	packageSource := filepath.Join(t.TempDir(), "source.handoff")
	repositoryID := strings.Repeat("a", 32)
	writeTestPackage(t, packageSource, repositoryID)
	packageBytes, err := os.ReadFile(packageSource)
	if err != nil {
		t.Fatal(err)
	}
	response = request(t, http.MethodPost, server.URL+"/api/v1/handoffs", strings.Repeat("t", 32), packageBytes)
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
	if response.StatusCode != http.StatusOK || !bytes.Equal([]byte(readBody(response.Body)), packageBytes) {
		t.Fatal("download did not return the uploaded bytes")
	}

	response = request(t, http.MethodGet, server.URL+"/api/v1/handoffs?repository_id="+repositoryID, strings.Repeat("t", 32), nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d: %s", response.StatusCode, readBody(response.Body))
	}
	var listed handoffListResponse
	if err := json.NewDecoder(response.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if len(listed.Handoffs) != 1 || listed.Handoffs[0].ID != uploaded.ID || listed.Handoffs[0].Author != "Walid" {
		t.Fatalf("unexpected inbox response: %+v", listed)
	}
	if len(listed.Handoffs[0].Files) != 0 {
		t.Fatal("list response should not include changed paths")
	}

	response = request(t, http.MethodGet, server.URL+"/api/v1/handoffs?repository_id="+strings.Repeat("b", 32), strings.Repeat("t", 32), nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("filtered list status = %d: %s", response.StatusCode, readBody(response.Body))
	}
	listed = handoffListResponse{}
	if err := json.NewDecoder(response.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if len(listed.Handoffs) != 0 {
		t.Fatalf("repository filter returned unrelated handoffs: %+v", listed)
	}

	response = request(t, http.MethodGet, server.URL+"/api/v1/handoffs/"+uploaded.ID+"/metadata", strings.Repeat("t", 32), nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("metadata status = %d: %s", response.StatusCode, readBody(response.Body))
	}
	var metadata handoffMetadata
	if err := json.NewDecoder(response.Body).Decode(&metadata); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if metadata.Project != "handoff" || metadata.FileCount != 2 || metadata.PackageBytes != int64(len(packageBytes)) {
		t.Fatalf("unexpected metadata: %+v", metadata)
	}

	response = request(t, http.MethodPost, server.URL+"/api/v1/handoffs", strings.Repeat("t", 32), make([]byte, 4097))
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
	if _, err := os.Stat(service.metadataPath(uploaded.ID)); !os.IsNotExist(err) {
		t.Fatal("expired package metadata was not removed")
	}
}

func TestServerDeleteRemovesPackageAndMetadata(t *testing.T) {
	token := strings.Repeat("d", 32)
	dataDir := t.TempDir()
	service, err := newService(serviceConfig{
		Token:       token,
		DataDir:     dataDir,
		DownloadDir: t.TempDir(),
		MaxBytes:    defaultMaxBytes,
		Retention:   time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(service.routes())
	defer server.Close()
	packageSource := filepath.Join(t.TempDir(), "source.handoff")
	writeTestPackage(t, packageSource, strings.Repeat("d", 32))
	packageBytes, err := os.ReadFile(packageSource)
	if err != nil {
		t.Fatal(err)
	}
	response := request(t, http.MethodPost, server.URL+"/api/v1/handoffs", token, packageBytes)
	if response.StatusCode != http.StatusCreated {
		t.Fatalf("upload status = %d: %s", response.StatusCode, readBody(response.Body))
	}
	var uploaded handoffMetadata
	if err := json.NewDecoder(response.Body).Decode(&uploaded); err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	response = request(t, http.MethodDelete, server.URL+"/api/v1/handoffs/"+uploaded.ID, token, nil)
	if response.StatusCode != http.StatusNoContent {
		t.Fatalf("delete status = %d: %s", response.StatusCode, readBody(response.Body))
	}
	response.Body.Close()
	for _, path := range []string{filepath.Join(dataDir, uploaded.ID+".handoff"), service.metadataPath(uploaded.ID)} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("deleted handoff file remains at %s", path)
		}
	}
}

func TestServerRejectsInvalidPackage(t *testing.T) {
	service, err := newService(serviceConfig{
		Token:       strings.Repeat("t", 32),
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
	response := request(t, http.MethodPost, server.URL+"/api/v1/handoffs", strings.Repeat("t", 32), []byte("not a package"))
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid package status = %d", response.StatusCode)
	}
}

func TestServerHomePage(t *testing.T) {
	service, err := newService(serviceConfig{
		DataDir:     t.TempDir(),
		DownloadDir: t.TempDir(),
		MaxBytes:    defaultMaxBytes,
		Retention:   time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "http://handoff.example.com/", nil)
	request.Header.Set("X-Forwarded-Proto", "https")
	response := httptest.NewRecorder()
	service.routes().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("home status = %d", response.Code)
	}
	if contentType := response.Header().Get("Content-Type"); contentType != "text/html; charset=utf-8" {
		t.Fatalf("home content type = %q", contentType)
	}
	if response.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("home response is missing a content security policy")
	}
	body := response.Body.String()
	for _, expected := range []string{
		"Share work in progress.",
		"handoff setup --server https://handoff.example.com",
		"/downloads/handoff-linux-amd64",
		"/downloads/handoff-darwin-arm64",
		"/downloads/handoff-windows-amd64.exe",
		"handoff inbox",
	} {
		if !strings.Contains(body, expected) {
			t.Errorf("home response does not contain %q", expected)
		}
	}
}

func TestServerHomePageEscapesHost(t *testing.T) {
	service, err := newService(serviceConfig{
		DataDir:     t.TempDir(),
		DownloadDir: t.TempDir(),
		MaxBytes:    defaultMaxBytes,
		Retention:   time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "http://handoff.example.com/", nil)
	request.Host = `handoff.example.com<script>alert(1)</script>`
	response := httptest.NewRecorder()
	service.routes().ServeHTTP(response, request)

	body := response.Body.String()
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Fatal("home response did not escape the request host")
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Fatal("home response does not contain the escaped request host")
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
