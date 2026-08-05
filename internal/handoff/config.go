package handoff

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/term"
)

type clientConfig struct {
	Server string `json:"server"`
	Token  string `json:"token"`
}

func runSetup(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	server := fs.String("server", "", "Handoff server URL")
	tokenFlag := fs.String("token", "", "team token (prefer the hidden prompt)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *server == "" {
		return errors.New("--server is required")
	}

	normalized, err := normalizeServerURL(*server)
	if err != nil {
		return err
	}
	tokenValue := strings.TrimSpace(*tokenFlag)
	if tokenValue == "" {
		tokenValue = strings.TrimSpace(os.Getenv("HANDOFF_TOKEN"))
	}
	if tokenValue == "" {
		fmt.Fprint(stdout, "Team token: ")
		if file, ok := stdin.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
			secret, readErr := term.ReadPassword(int(file.Fd()))
			fmt.Fprintln(stdout)
			if readErr != nil {
				return readErr
			}
			tokenValue = strings.TrimSpace(string(secret))
		} else {
			line, readErr := bufio.NewReader(stdin).ReadString('\n')
			if readErr != nil && !errors.Is(readErr, io.EOF) {
				return readErr
			}
			tokenValue = strings.TrimSpace(line)
		}
	}
	if len(tokenValue) < 32 {
		return errors.New("team token must contain at least 32 characters")
	}

	cfg := clientConfig{Server: normalized, Token: tokenValue}
	if err := verifyCredentials(cfg); err != nil {
		return err
	}
	path, err := configPath()
	if err != nil {
		return err
	}
	if err := writeJSONAtomic(path, cfg, 0o600); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Configured Handoff server %s\n", normalized)
	return nil
}

func normalizeServerURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimRight(strings.TrimSpace(raw), "/"))
	if err != nil || parsed.Host == "" {
		return "", errors.New("invalid server URL")
	}
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && (parsed.Hostname() == "localhost" || parsed.Hostname() == "127.0.0.1")) {
		return "", errors.New("server URL must use HTTPS (HTTP is allowed only for localhost)")
	}
	if parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("server URL must not contain a path, query, or fragment")
	}
	return parsed.String(), nil
}

func configPath() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "handoff", "config.json"), nil
}

func loadConfig() (clientConfig, error) {
	path, err := configPath()
	if err != nil {
		return clientConfig{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return clientConfig{}, errors.New("not configured; run 'handoff setup --server URL'")
		}
		return clientConfig{}, err
	}
	var cfg clientConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return clientConfig{}, fmt.Errorf("invalid config: %w", err)
	}
	if env := strings.TrimSpace(os.Getenv("HANDOFF_SERVER")); env != "" {
		cfg.Server = strings.TrimRight(env, "/")
	}
	if env := strings.TrimSpace(os.Getenv("HANDOFF_TOKEN")); env != "" {
		cfg.Token = env
	}
	if cfg.Server == "" || cfg.Token == "" {
		return clientConfig{}, errors.New("config is missing server or token")
	}
	normalized, err := normalizeServerURL(cfg.Server)
	if err != nil {
		return clientConfig{}, fmt.Errorf("invalid configured server: %w", err)
	}
	if len(strings.TrimSpace(cfg.Token)) < 32 {
		return clientConfig{}, errors.New("configured team token must contain at least 32 characters")
	}
	cfg.Server = normalized
	cfg.Token = strings.TrimSpace(cfg.Token)
	return cfg, nil
}

func verifyCredentials(cfg clientConfig) error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.Server+"/api/v1/auth", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("cannot reach server: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("server rejected credentials: %s", resp.Status)
	}
	return nil
}

func writeJSONAtomic(path string, value any, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(mode); err != nil {
		tmp.Close()
		return err
	}
	encoder := json.NewEncoder(tmp)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
