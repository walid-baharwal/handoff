package main

import (
	"bufio"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/term"
)

const (
	defaultMaxBytes = int64(100 << 20)
)

var version = "dev"

type clientConfig struct {
	Server string `json:"server"`
	Token  string `json:"token"`
}

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "handoff:", err)
		os.Exit(1)
	}
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		printUsage(stdout)
		return nil
	}

	switch args[0] {
	case "setup":
		return runSetup(args[1:], stdin, stdout)
	case "push":
		return runPush(args[1:], stdout)
	case "pull":
		return runPull(args[1:], stdout)
	case "continue":
		return runContinue(args[1:], stdout)
	case "abort":
		return runAbort(args[1:], stdout)
	case "serve":
		return runServe(args[1:], stdout, stderr)
	case "version", "--version", "-v":
		fmt.Fprintln(stdout, version)
		return nil
	case "help", "--help", "-h":
		printUsage(stdout)
		return nil
	default:
		return fmt.Errorf("unknown command %q (run 'handoff help')", args[0])
	}
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, `Handoff transfers uncommitted Git changes through a central server.

Usage:
  handoff setup --server URL
  handoff push [-m MESSAGE] [PATH ...]
  handoff pull ID
  handoff continue ID
  handoff abort ID
  handoff serve
  handoff version`)
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

func runServe(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	address := fs.String("address", envOr("HANDOFF_ADDRESS", ":8080"), "listen address")
	if err := fs.Parse(args); err != nil {
		return err
	}
	tokenValue := strings.TrimSpace(os.Getenv("HANDOFF_TOKEN"))
	if len(tokenValue) < 32 {
		return errors.New("HANDOFF_TOKEN must contain at least 32 characters")
	}
	maxBytes, err := envInt64("HANDOFF_MAX_BYTES", defaultMaxBytes)
	if err != nil || maxBytes < 1 {
		return errors.New("HANDOFF_MAX_BYTES must be a positive integer")
	}
	retention, err := time.ParseDuration(envOr("HANDOFF_RETENTION", "720h"))
	if err != nil || retention <= 0 {
		return errors.New("HANDOFF_RETENTION must be a positive duration")
	}

	service, err := newService(serviceConfig{
		Token:       tokenValue,
		DataDir:     envOr("HANDOFF_DATA_DIR", "/data"),
		DownloadDir: envOr("HANDOFF_DOWNLOAD_DIR", "/downloads"),
		MaxBytes:    maxBytes,
		Retention:   retention,
		Logger:      stderr,
	})
	if err != nil {
		return err
	}
	server := &http.Server{
		Addr:              *address,
		Handler:           service.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       2 * time.Minute,
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       1 * time.Minute,
		MaxHeaderBytes:    1 << 20,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	fmt.Fprintf(stdout, "Handoff %s listening on %s\n", version, *address)
	err = server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func envInt64(name string, fallback int64) (int64, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	return strconv.ParseInt(value, 10, 64)
}

func secureTokenEqual(expected, authorization string) bool {
	const prefix = "Bearer "
	if !strings.HasPrefix(authorization, prefix) {
		return false
	}
	provided := strings.TrimSpace(strings.TrimPrefix(authorization, prefix))
	if len(provided) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(provided)) == 1
}

func platformName() string {
	return runtime.GOOS + "-" + runtime.GOARCH
}
