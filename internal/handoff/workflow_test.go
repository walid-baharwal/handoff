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
