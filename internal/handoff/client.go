package handoff

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func runPush(args []string, stdout io.Writer) error {
	fs := flag.NewFlagSet("push", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	message := fs.String("m", "", "handoff message")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	tmpDir, err := os.MkdirTemp("", "handoff-push-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)
	packagePath := filepath.Join(tmpDir, "changes.handoff")
	metadata, err := buildHandoffPackage(packagePath, *message, fs.Args())
	if err != nil {
		return err
	}
	id, err := uploadPackage(cfg, packagePath)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Handoff uploaded successfully\nID: %s\n", id)
	if metadata.Message != "" {
		fmt.Fprintf(stdout, "Message: %s\n", metadata.Message)
	}
	fmt.Fprintf(stdout, "\nShare this command:\nhandoff pull %s\n", id)
	return nil
}

func uploadPackage(cfg clientConfig, path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.Server+"/api/v1/handoffs", file)
	if err != nil {
		return "", err
	}
	req.ContentLength = info.Size()
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	req.Header.Set("Content-Type", "application/vnd.handoff.package")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("upload failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return "", responseError("upload failed", resp)
	}
	var result struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&result); err != nil {
		return "", errors.New("server returned an invalid response")
	}
	if !idPattern.MatchString(result.ID) {
		return "", errors.New("server returned an invalid handoff ID")
	}
	return result.ID, nil
}

func runPull(args []string, stdout io.Writer) error {
	if len(args) != 1 || !idPattern.MatchString(strings.ToLower(args[0])) {
		return errors.New("usage: handoff pull ID")
	}
	id := strings.ToLower(args[0])
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	tmpDir, err := os.MkdirTemp("", "handoff-pull-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)
	packagePath := filepath.Join(tmpDir, id+".handoff")
	if err := downloadPackage(cfg, id, packagePath); err != nil {
		return err
	}
	return applyHandoffPackage(id, packagePath, tmpDir, stdout)
}

func downloadPackage(cfg clientConfig, id, destination string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.Server+"/api/v1/handoffs/"+id, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return responseError("download failed", resp)
	}
	if resp.ContentLength > defaultMaxBytes {
		return errors.New("handoff package exceeds the local 100 MB limit")
	}
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	written, copyErr := io.Copy(output, io.LimitReader(resp.Body, defaultMaxBytes+1))
	closeErr := output.Close()
	if copyErr != nil || closeErr != nil {
		os.Remove(destination)
		return errors.New("download was incomplete")
	}
	if written > defaultMaxBytes {
		os.Remove(destination)
		return errors.New("handoff package exceeds the local 100 MB limit")
	}
	return nil
}

func responseError(prefix string, resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
	detail := strings.TrimSpace(string(body))
	if detail == "" {
		detail = resp.Status
	}
	return fmt.Errorf("%s: %s", prefix, detail)
}

func runContinue(args []string, stdout io.Writer) error {
	if len(args) != 1 || !idPattern.MatchString(strings.ToLower(args[0])) {
		return errors.New("usage: handoff continue ID")
	}
	return continueHandoff(strings.ToLower(args[0]), stdout)
}

func runAbort(args []string, stdout io.Writer) error {
	if len(args) != 1 || !idPattern.MatchString(strings.ToLower(args[0])) {
		return errors.New("usage: handoff abort ID")
	}
	return abortHandoff(strings.ToLower(args[0]), stdout)
}
