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
	"os"
	"path/filepath"
	"strconv"
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
	if metadata.Author != "" {
		fmt.Fprintf(stdout, "From: %s (self-reported)\n", printable(metadata.Author))
	}
	if metadata.Project != "" {
		fmt.Fprintf(stdout, "Project: %s\n", printable(metadata.Project))
	}
	if metadata.Branch != "" {
		fmt.Fprintf(stdout, "Branch: %s\n", printable(metadata.Branch))
	}
	if metadata.Message != "" {
		fmt.Fprintf(stdout, "Message: %s\n", printable(metadata.Message))
	}
	fmt.Fprintf(stdout, "Files: %d\n\nThis handoff is now visible in the team inbox.\nDirect command:\nhandoff pull %s\n", metadata.FileCount, id)
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

func runPull(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := flag.NewFlagSet("pull", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	dryRun := fs.Bool("dry-run", false, "inspect without applying")
	yes := fs.Bool("yes", false, "skip interactive confirmation")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if len(fs.Args()) > 1 {
		return errors.New("usage: handoff pull [--dry-run] [--yes] [ID]")
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	id := ""
	input := bufio.NewReader(stdin)
	interactive := len(fs.Args()) == 0
	var selected handoffMetadata
	if interactive {
		repositoryID, project, err := currentRepositoryIdentity()
		if err != nil {
			return err
		}
		items, err := listHandoffs(cfg, repositoryID, 20)
		if err != nil {
			return err
		}
		if len(items) == 0 {
			fmt.Fprintf(stdout, "No handoffs are available for %s.\n", printable(project))
			return nil
		}
		printHandoffList(stdout, items, project)
		selected, err = selectHandoff(input, stdout, items)
		if err != nil {
			return err
		}
		id = selected.ID
		selected, err = getHandoffMetadata(cfg, id)
		if err != nil {
			return err
		}
	} else {
		id = strings.ToLower(fs.Args()[0])
		if !idPattern.MatchString(id) {
			return errors.New("usage: handoff pull [--dry-run] [--yes] [ID]")
		}
		selected, _ = getHandoffMetadata(cfg, id)
	}
	if selected.ID != "" {
		fmt.Fprintln(stdout)
		printHandoffMetadata(stdout, selected)
	}
	if *dryRun {
		if selected.ID == "" {
			return errors.New("handoff metadata is unavailable; the server may predate inbox support")
		}
		fmt.Fprintln(stdout, "\nDry run complete; no files were changed.")
		return nil
	}
	if interactive && !*yes {
		confirmed, err := confirmPull(input, stdout)
		if err != nil {
			return err
		}
		if !confirmed {
			fmt.Fprintln(stdout, "Pull cancelled.")
			return nil
		}
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

func selectHandoff(stdin *bufio.Reader, stdout io.Writer, items []handoffMetadata) (handoffMetadata, error) {
	fmt.Fprintf(stdout, "\nSelect a handoff [1-%d] or enter its ID: ", len(items))
	line, err := readInputLine(stdin)
	if err != nil {
		return handoffMetadata{}, err
	}
	if number, parseErr := strconv.Atoi(line); parseErr == nil {
		if number < 1 || number > len(items) {
			return handoffMetadata{}, errors.New("selection is outside the displayed range")
		}
		return items[number-1], nil
	}
	id := strings.ToLower(line)
	if !idPattern.MatchString(id) {
		return handoffMetadata{}, errors.New("enter a displayed number or a valid handoff ID")
	}
	for _, item := range items {
		if item.ID == id {
			return item, nil
		}
	}
	return handoffMetadata{}, errors.New("handoff ID is not in the displayed inbox")
}

func confirmPull(stdin *bufio.Reader, stdout io.Writer) (bool, error) {
	fmt.Fprint(stdout, "\nApply this handoff? [y/N]: ")
	line, err := readInputLine(stdin)
	if err != nil {
		return false, err
	}
	switch strings.ToLower(line) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

func readInputLine(stdin *bufio.Reader) (string, error) {
	line, err := stdin.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return "", errors.New("no selection was entered")
	}
	return line, nil
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
