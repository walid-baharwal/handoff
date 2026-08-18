package handoff

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type stringListFlag []string

func (values *stringListFlag) String() string {
	return strings.Join(*values, ",")
}

func (values *stringListFlag) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return errors.New("path cannot be empty")
	}
	*values = append(*values, value)
	return nil
}

func runPush(args []string, stdin io.Reader, stdout io.Writer) error {
	fs := newSilentFlagSet("push")
	message := fs.String("m", "", "handoff message")
	dryRun := fs.Bool("dry-run", false, "preview without uploading")
	jsonOutput := fs.Bool("json", false, "print machine-readable JSON")
	interactive := fs.Bool("interactive", false, "select changed paths interactively")
	staged := fs.Bool("staged", false, "include staged changes only")
	worktree := fs.Bool("worktree", false, "include worktree changes only")
	team := fs.String("team", "", "share with a team or channel")
	private := fs.Bool("private", false, "restrict visibility to recipients or the selected team")
	var excludes stringListFlag
	var recipients stringListFlag
	fs.Var(&excludes, "exclude", "exclude a file or directory (repeatable)")
	fs.Var(&recipients, "to", "share with a user ID or email (repeatable)")
	if err := fs.Parse(args); err != nil {
		return invalidArguments(err.Error())
	}
	if *staged && *worktree {
		return invalidArguments("--staged and --worktree cannot be used together")
	}
	if *interactive && *jsonOutput {
		return invalidArguments("--interactive and --json cannot be used together")
	}
	if *private && len(recipients) == 0 && strings.TrimSpace(*team) == "" {
		return invalidArguments("--private requires --to or --team")
	}
	if len(recipients) > 50 {
		return invalidArguments("a handoff cannot have more than 50 recipients")
	}
	for index := range recipients {
		recipients[index] = strings.TrimSpace(strings.ToLower(recipients[index]))
		if len(recipients[index]) > 254 {
			return invalidArguments("recipient identifiers cannot exceed 254 characters")
		}
	}
	mode := pushModeAll
	if *staged {
		mode = pushModeStaged
	} else if *worktree {
		mode = pushModeWorktree
	}
	var cfg clientConfig
	var err error
	if !*dryRun {
		cfg, err = loadConfig()
		if err != nil {
			return err
		}
	}
	root, err := repositoryRoot()
	if err != nil {
		return err
	}
	selectedPaths, err := selectHandoffPaths(root, fs.Args(), excludes, mode)
	if err != nil {
		return err
	}
	if *interactive {
		var cancelled bool
		selectedPaths, cancelled, err = choosePushPaths(bufio.NewReader(stdin), stdout, selectedPaths)
		if err != nil {
			return err
		}
		if cancelled {
			fmt.Fprintln(stdout, "Push cancelled.")
			return nil
		}
	}
	tmpDir, err := os.MkdirTemp("", "handoff-push-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmpDir)
	packagePath := filepath.Join(tmpDir, "changes.handoff")
	metadata, err := buildHandoffPackageFromPathsWithSharing(root, packagePath, *message, selectedPaths, mode, sharingOptions{
		Team:       strings.TrimSpace(strings.ToLower(*team)),
		Recipients: recipients,
		Private:    *private,
	})
	if err != nil {
		return err
	}
	packageInfo, err := os.Stat(packagePath)
	if err != nil {
		return err
	}
	if *dryRun {
		if *jsonOutput {
			return writeIntegrationJSON(stdout, "push", struct {
				Status  string             `json:"status"`
				DryRun  bool               `json:"dry_run"`
				Mode    pushMode           `json:"mode"`
				Handoff integrationHandoff `json:"handoff"`
			}{
				Status:  "previewed",
				DryRun:  true,
				Mode:    mode,
				Handoff: integrationHandoffFromManifest(metadata, "", packageInfo.Size()),
			})
		}
		printPushPreview(stdout, metadata, mode, packageInfo.Size())
		return nil
	}
	id, err := uploadPackage(cfg, packagePath)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeIntegrationJSON(stdout, "push", struct {
			Status  string             `json:"status"`
			DryRun  bool               `json:"dry_run"`
			Mode    pushMode           `json:"mode"`
			Handoff integrationHandoff `json:"handoff"`
		}{
			Status:  "uploaded",
			DryRun:  false,
			Mode:    mode,
			Handoff: integrationHandoffFromManifest(metadata, id, packageInfo.Size()),
		})
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
	fmt.Fprintf(stdout, "Selection: %s\n", pushModeDescription(mode))
	if metadata.Message != "" {
		fmt.Fprintf(stdout, "Message: %s\n", printable(metadata.Message))
	}
	fmt.Fprintf(stdout, "Files: %d\n\nThis handoff is now visible in the team inbox.\nDirect command:\nhandoff pull %s\n", metadata.FileCount, id)
	return nil
}

func choosePushPaths(stdin *bufio.Reader, stdout io.Writer, paths []string) ([]string, bool, error) {
	fmt.Fprintln(stdout, "CHANGED PATHS")
	for index, path := range paths {
		fmt.Fprintf(stdout, "%3d  %s\n", index+1, printable(path))
	}
	fmt.Fprint(stdout, "\nSelect paths (for example 1,3-5 or all; q to cancel): ")
	line, err := readInputLine(stdin)
	if err != nil {
		return nil, false, err
	}
	if strings.EqualFold(line, "q") || strings.EqualFold(line, "quit") {
		return nil, true, nil
	}
	indices, err := parsePushSelection(line, len(paths))
	if err != nil {
		return nil, false, err
	}
	selected := make([]string, 0, len(indices))
	for index, path := range paths {
		if _, found := indices[index+1]; found {
			selected = append(selected, path)
		}
	}
	return selected, false, nil
}

func parsePushSelection(value string, maximum int) (map[int]struct{}, error) {
	selected := make(map[int]struct{})
	if strings.EqualFold(strings.TrimSpace(value), "all") {
		for index := 1; index <= maximum; index++ {
			selected[index] = struct{}{}
		}
		return selected, nil
	}
	parts := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t'
	})
	for _, part := range parts {
		bounds := strings.Split(part, "-")
		if len(bounds) > 2 {
			return nil, fmt.Errorf("invalid selection %q", part)
		}
		start, err := strconv.Atoi(bounds[0])
		if err != nil {
			return nil, fmt.Errorf("invalid selection %q", part)
		}
		end := start
		if len(bounds) == 2 {
			end, err = strconv.Atoi(bounds[1])
			if err != nil || end < start {
				return nil, fmt.Errorf("invalid selection %q", part)
			}
		}
		if start < 1 || end > maximum {
			return nil, fmt.Errorf("selection %q is outside the displayed range", part)
		}
		for index := start; index <= end; index++ {
			selected[index] = struct{}{}
		}
	}
	if len(selected) == 0 {
		return nil, errors.New("select at least one changed path")
	}
	return selected, nil
}

func printPushPreview(stdout io.Writer, metadata manifest, mode pushMode, packageBytes int64) {
	message := metadata.Message
	if message == "" {
		message = "Handoff changes (default)"
	}
	fmt.Fprintln(stdout, "HANDOFF PUSH PREVIEW")
	fmt.Fprintf(stdout, "From: %s (self-reported)\n", printable(orUnknown(metadata.Author)))
	fmt.Fprintf(stdout, "Project: %s\n", printable(orUnknown(metadata.Project)))
	fmt.Fprintf(stdout, "Branch: %s\n", printable(orUnknown(metadata.Branch)))
	fmt.Fprintf(stdout, "Selection: %s\n", pushModeDescription(mode))
	fmt.Fprintf(stdout, "Message: %s\n", printable(message))
	fmt.Fprintf(stdout, "Files: %d\n", metadata.FileCount)
	fmt.Fprintf(stdout, "Package: %s\n", formatBytes(packageBytes))
	fmt.Fprintln(stdout, "Changed paths:")
	for _, path := range metadata.Files {
		fmt.Fprintf(stdout, "  %s\n", printable(path))
	}
	if metadata.FilesTruncated {
		fmt.Fprintf(stdout, "  ... and %d more\n", metadata.FileCount-len(metadata.Files))
	}
	fmt.Fprintln(stdout, "\nDry run complete; no files were uploaded.")
}

func pushModeDescription(mode pushMode) string {
	switch mode {
	case pushModeStaged:
		return "staged changes"
	case pushModeWorktree:
		return "worktree changes"
	default:
		return "all changes"
	}
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
		return "", commandError("server_unavailable", fmt.Errorf("upload failed: %w", err))
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
	fs := newSilentFlagSet("pull")
	dryRun := fs.Bool("dry-run", false, "inspect without applying")
	yes := fs.Bool("yes", false, "skip interactive confirmation")
	jsonOutput := fs.Bool("json", false, "print machine-readable JSON")
	if err := fs.Parse(args); err != nil {
		return invalidArguments(err.Error())
	}
	if len(fs.Args()) > 1 {
		return invalidArguments("usage: handoff pull [--dry-run] [--yes] [--json] [ID]")
	}
	if *jsonOutput && len(fs.Args()) == 0 {
		return invalidArguments("--json requires a handoff ID")
	}
	if len(fs.Args()) == 1 && !idPattern.MatchString(strings.ToLower(fs.Args()[0])) {
		return invalidArguments("usage: handoff pull [--dry-run] [--yes] [--json] [ID]")
	}
	if *jsonOutput && !*dryRun && !*yes {
		return invalidArguments("--yes is required when applying a handoff with --json")
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
		selected, err = getHandoffMetadata(cfg, id)
		if err != nil {
			return err
		}
	}
	if selected.ID != "" && !*jsonOutput {
		fmt.Fprintln(stdout)
		printHandoffMetadata(stdout, selected)
	}
	_, _ = postHandoffEvent(cfg, id, "read", "", "")
	if *dryRun {
		if selected.ID == "" {
			return errors.New("handoff metadata is unavailable; the server may predate inbox support")
		}
		if *jsonOutput {
			compatibility, err := inspectCompatibility(selected)
			if err != nil {
				return err
			}
			return writeIntegrationJSON(stdout, "pull", struct {
				Status        string              `json:"status"`
				DryRun        bool                `json:"dry_run"`
				Applied       bool                `json:"applied"`
				Handoff       integrationHandoff  `json:"handoff"`
				Compatibility compatibilityReport `json:"compatibility"`
			}{
				Status:        "previewed",
				DryRun:        true,
				Applied:       false,
				Handoff:       integrationHandoffFromMetadata(selected),
				Compatibility: compatibility,
			})
		}
		fmt.Fprintln(stdout, "\nDry run complete; no files were changed.")
		return nil
	}
	if !*yes {
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
	applyOutput := stdout
	if *jsonOutput {
		applyOutput = io.Discard
	}
	if err := applyHandoffPackage(id, packagePath, tmpDir, applyOutput); err != nil {
		return err
	}
	_, _ = postHandoffEvent(cfg, id, "applied", "", "")
	if *jsonOutput {
		return writeIntegrationJSON(stdout, "pull", struct {
			Status   string             `json:"status"`
			DryRun   bool               `json:"dry_run"`
			Applied  bool               `json:"applied"`
			Handoff  integrationHandoff `json:"handoff"`
			Recovery recoveryStatus     `json:"recovery"`
		}{
			Status:   "applied",
			DryRun:   false,
			Applied:  true,
			Handoff:  integrationHandoffFromMetadata(selected),
			Recovery: recoveryStatus{ConflictedFiles: []string{}},
		})
	}
	return nil
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
		return commandError("server_unavailable", fmt.Errorf("download failed: %w", err))
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
	code := "server_error"
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		code = "authentication_failed"
	case http.StatusForbidden:
		code = "access_denied"
	case http.StatusNotFound:
		code = "not_found"
	case http.StatusConflict:
		code = "server_conflict"
	case http.StatusRequestEntityTooLarge:
		code = "package_too_large"
	}
	return commandError(code, fmt.Errorf("%s: %s", prefix, detail))
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
