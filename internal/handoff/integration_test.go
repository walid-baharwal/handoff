package handoff

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

type decodedIntegrationEnvelope struct {
	SchemaVersion int               `json:"schema_version"`
	Command       string            `json:"command"`
	Data          json.RawMessage   `json:"data"`
	Error         *integrationError `json:"error"`
}

func TestPushJSONPreviewHasStableEnvelope(t *testing.T) {
	sender, _ := clonePair(t)
	writeFile(t, filepath.Join(sender, "app.txt"), "editor preview\n")

	var stdout, stderr bytes.Buffer
	inDirectory(t, sender, func() {
		if err := Run([]string{"push", "--dry-run", "--json", "-m", "editor preview"}, strings.NewReader(""), &stdout, &stderr); err != nil {
			t.Fatal(err)
		}
	})
	if stderr.Len() != 0 {
		t.Fatalf("unexpected stderr: %s", stderr.String())
	}
	response := decodeIntegrationResponse(t, stdout.Bytes(), "push")
	var data struct {
		Status  string             `json:"status"`
		DryRun  bool               `json:"dry_run"`
		Mode    string             `json:"mode"`
		Handoff integrationHandoff `json:"handoff"`
	}
	decodeIntegrationData(t, response.Data, &data)
	if data.Status != "previewed" || !data.DryRun || data.Mode != "all" {
		t.Fatalf("unexpected push result: %#v", data)
	}
	if data.Handoff.ID != "" || data.Handoff.Message != "editor preview" || data.Handoff.FileCount != 1 {
		t.Fatalf("unexpected preview metadata: %#v", data.Handoff)
	}
	if len(data.Handoff.Files) != 1 || data.Handoff.Files[0] != "app.txt" || data.Handoff.PackageBytes < 1 {
		t.Fatalf("unexpected preview files: %#v", data.Handoff)
	}
	if strings.Contains(stdout.String(), "HANDOFF PUSH PREVIEW") {
		t.Fatalf("JSON output contains human-readable text: %s", stdout.String())
	}
}

func TestJSONErrorsAreStructuredAndReportedOnce(t *testing.T) {
	var stdout, stderr bytes.Buffer
	err := Run(
		[]string{"push", "--dry-run", "--json", "--interactive"},
		strings.NewReader(""),
		&stdout,
		&stderr,
	)
	if err == nil || !IsReportedError(err) {
		t.Fatalf("expected a reported error, got %v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("error wrote to stdout: %s", stdout.String())
	}
	response := decodeIntegrationResponse(t, stderr.Bytes(), "push")
	if response.Error == nil || response.Error.Code != "invalid_arguments" {
		t.Fatalf("unexpected error response: %#v", response.Error)
	}
	if response.Data != nil {
		t.Fatalf("error response includes data: %s", response.Data)
	}
}

func TestStatusJSONReportsRecoveryConflict(t *testing.T) {
	sender, receiver := clonePair(t)
	writeFile(t, filepath.Join(sender, "app.txt"), "sender\n")
	packagePath := buildFrom(t, sender, nil)
	writeFile(t, filepath.Join(receiver, "app.txt"), "receiver\n")

	inDirectory(t, receiver, func() {
		if err := applyHandoffPackage(testID, packagePath, t.TempDir(), io.Discard); err == nil {
			t.Fatal("expected pull conflict")
		}
		defer func() {
			if err := abortHandoff(testID, io.Discard); err != nil {
				t.Errorf("abort conflicted handoff: %v", err)
			}
		}()

		var stdout, stderr bytes.Buffer
		if err := Run([]string{"status", "--json"}, strings.NewReader(""), &stdout, &stderr); err != nil {
			t.Fatal(err)
		}
		response := decodeIntegrationResponse(t, stdout.Bytes(), "status")
		var data struct {
			Recovery recoveryStatus `json:"recovery"`
		}
		decodeIntegrationData(t, response.Data, &data)
		if !data.Recovery.Active || data.Recovery.HandoffID != testID || data.Recovery.Stage != "local_changes" {
			t.Fatalf("unexpected recovery status: %#v", data.Recovery)
		}
		if data.Recovery.CanContinue || !data.Recovery.CanAbort {
			t.Fatalf("unexpected recovery actions: %#v", data.Recovery)
		}
		if len(data.Recovery.ConflictedFiles) != 1 || data.Recovery.ConflictedFiles[0] != "app.txt" {
			t.Fatalf("unexpected conflict list: %#v", data.Recovery.ConflictedFiles)
		}

		var errorOutput bytes.Buffer
		reported := reportIntegrationError(&errorOutput, "pull", conflictError(testID, "restored local work"))
		if reported == nil || !IsReportedError(reported) {
			t.Fatalf("expected reported conflict error, got %v", reported)
		}
		errorResponse := decodeIntegrationResponse(t, errorOutput.Bytes(), "pull")
		if errorResponse.Error == nil || errorResponse.Error.Code != "conflict" || errorResponse.Error.Recovery == nil {
			t.Fatalf("unexpected conflict error: %#v", errorResponse.Error)
		}
		if errorResponse.Error.Recovery.HandoffID != testID || len(errorResponse.Error.Recovery.ConflictedFiles) != 1 {
			t.Fatalf("unexpected conflict recovery: %#v", errorResponse.Error.Recovery)
		}
	})
}

func TestInboxInspectAndPullJSONWorkflow(t *testing.T) {
	token := strings.Repeat("j", 32)
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
	configureTestClient(t, clientConfig{Server: server.URL, Token: token})

	sender, receiver := clonePair(t)
	writeFile(t, filepath.Join(sender, "app.txt"), "from editor workflow\n")
	packagePath := buildFrom(t, sender, nil)
	id, err := uploadPackage(clientConfig{Server: server.URL, Token: token}, packagePath)
	if err != nil {
		t.Fatal(err)
	}

	inDirectory(t, receiver, func() {
		listData := runJSONCommand(t, []string{"list", "--json"}, "list")
		var list struct {
			Scope    string               `json:"scope"`
			Limit    int                  `json:"limit"`
			Handoffs []integrationHandoff `json:"handoffs"`
		}
		decodeIntegrationData(t, listData, &list)
		if list.Scope != "repository" || list.Limit != 20 || len(list.Handoffs) != 1 || list.Handoffs[0].ID != id {
			t.Fatalf("unexpected list result: %#v", list)
		}

		inspectData := runJSONCommand(t, []string{"inspect", "--json", id}, "inspect")
		var inspect struct {
			Handoff integrationHandoff `json:"handoff"`
		}
		decodeIntegrationData(t, inspectData, &inspect)
		if inspect.Handoff.ID != id || inspect.Handoff.Author != "Handoff Test" {
			t.Fatalf("unexpected inspect result: %#v", inspect)
		}

		previewData := runJSONCommand(t, []string{"pull", "--dry-run", "--json", id}, "pull")
		var preview struct {
			Status  string `json:"status"`
			DryRun  bool   `json:"dry_run"`
			Applied bool   `json:"applied"`
		}
		decodeIntegrationData(t, previewData, &preview)
		if preview.Status != "previewed" || !preview.DryRun || preview.Applied {
			t.Fatalf("unexpected pull preview: %#v", preview)
		}

		pullData := runJSONCommand(t, []string{"pull", "--json", id}, "pull")
		var pull struct {
			Status   string         `json:"status"`
			Applied  bool           `json:"applied"`
			Recovery recoveryStatus `json:"recovery"`
		}
		decodeIntegrationData(t, pullData, &pull)
		if pull.Status != "applied" || !pull.Applied || pull.Recovery.Active {
			t.Fatalf("unexpected pull result: %#v", pull)
		}
	})
	assertFile(t, filepath.Join(receiver, "app.txt"), "from editor workflow\n")
}

func TestSetupTokenStdinDoesNotPromptOrExposeSecret(t *testing.T) {
	token := strings.Repeat("s", 32)
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
	setTestConfigRoot(t)
	t.Setenv("HANDOFF_TOKEN", strings.Repeat("wrong", 8))

	var output bytes.Buffer
	if err := runSetup(
		[]string{"--server", server.URL, "--token-stdin"},
		strings.NewReader(token+"\n"),
		&output,
	); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "Team token:") || strings.Contains(output.String(), token) {
		t.Fatalf("setup exposed the token or printed an interactive prompt: %q", output.String())
	}
	data, err := os.ReadFile(mustConfigPath(t))
	if err != nil {
		t.Fatal(err)
	}
	var cfg clientConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Token != token || cfg.Server != server.URL {
		t.Fatalf("unexpected saved config: %#v", cfg)
	}
}

func runJSONCommand(t *testing.T, args []string, command string) json.RawMessage {
	t.Helper()
	var stdout, stderr bytes.Buffer
	if err := Run(args, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("%s failed: %v\nstderr: %s", command, err, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("%s wrote stderr: %s", command, stderr.String())
	}
	return decodeIntegrationResponse(t, stdout.Bytes(), command).Data
}

func decodeIntegrationResponse(t *testing.T, data []byte, command string) decodedIntegrationEnvelope {
	t.Helper()
	var response decodedIntegrationEnvelope
	if err := json.Unmarshal(data, &response); err != nil {
		t.Fatalf("decode %s response: %v\n%s", command, err, data)
	}
	if response.SchemaVersion != integrationSchemaVersion || response.Command != command {
		t.Fatalf("unexpected %s envelope: %#v", command, response)
	}
	return response
}

func decodeIntegrationData(t *testing.T, data json.RawMessage, destination any) {
	t.Helper()
	if err := json.Unmarshal(data, destination); err != nil {
		t.Fatalf("decode integration data: %v\n%s", err, data)
	}
}

func configureTestClient(t *testing.T, cfg clientConfig) {
	t.Helper()
	setTestConfigRoot(t)
	if err := writeJSONAtomic(mustConfigPath(t), cfg, 0o600); err != nil {
		t.Fatal(err)
	}
}

func setTestConfigRoot(t *testing.T) {
	t.Helper()
	configRoot := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Setenv("APPDATA", configRoot)
	} else {
		t.Setenv("XDG_CONFIG_HOME", configRoot)
	}
}

func mustConfigPath(t *testing.T) string {
	t.Helper()
	path, err := configPath()
	if err != nil {
		t.Fatal(err)
	}
	return path
}
