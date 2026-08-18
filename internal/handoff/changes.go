package handoff

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type fileChange struct {
	Path           string `json:"path"`
	OriginalPath   string `json:"original_path,omitempty"`
	Status         string `json:"status"`
	SizeBytes      int64  `json:"size_bytes,omitempty"`
	Binary         bool   `json:"binary,omitempty"`
	ContentID      string `json:"content_id,omitempty"`
	Staged         bool   `json:"staged,omitempty"`
	Worktree       bool   `json:"worktree,omitempty"`
	Supported      bool   `json:"supported"`
	ExcludedReason string `json:"excluded_reason,omitempty"`
}

type repositoryInfo struct {
	Root         string `json:"root"`
	Project      string `json:"project"`
	RepositoryID string `json:"repository_id"`
	Branch       string `json:"branch"`
	Head         string `json:"head"`
}

func runChanges(args []string, stdout io.Writer) error {
	fs := newSilentFlagSet("changes")
	jsonOutput := fs.Bool("json", false, "print machine-readable JSON")
	if err := fs.Parse(args); err != nil {
		return invalidArguments(err.Error())
	}
	if len(fs.Args()) != 0 {
		return invalidArguments("usage: handoff changes [--json]")
	}
	root, err := repositoryRoot()
	if err != nil {
		return err
	}
	repository, err := inspectRepository(root)
	if err != nil {
		return err
	}
	changes, err := workingChanges(root)
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeIntegrationJSON(stdout, "changes", struct {
			Repository repositoryInfo `json:"repository"`
			Changes    []fileChange   `json:"changes"`
		}{Repository: repository, Changes: nonNilChanges(changes)})
	}
	if len(changes) == 0 {
		fmt.Fprintln(stdout, "No uncommitted changes.")
		return nil
	}
	for _, change := range changes {
		label := strings.ToUpper(change.Status[:1])
		if change.Status == "renamed" {
			fmt.Fprintf(stdout, "%s  %s -> %s\n", label, printable(change.OriginalPath), printable(change.Path))
		} else {
			fmt.Fprintf(stdout, "%s  %s\n", label, printable(change.Path))
		}
	}
	return nil
}

func inspectRepository(root string) (repositoryInfo, error) {
	head, err := gitOutput(root, nil, "rev-parse", "HEAD")
	if err != nil {
		return repositoryInfo{}, commandError("repository_has_no_commits", errors.New("the repository needs at least one commit"))
	}
	repositoryID, project, branch := repositoryDetails(root, head)
	return repositoryInfo{Root: root, Project: project, RepositoryID: repositoryID, Branch: branch, Head: head}, nil
}

func workingChanges(root string) ([]fileChange, error) {
	tmpDir, err := os.MkdirTemp("", "handoff-changes-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmpDir)
	indexPath := filepath.Join(tmpDir, "index")
	env := gitEnvironment("GIT_INDEX_FILE="+indexPath, "GIT_LITERAL_PATHSPECS=1")
	if _, err := gitOutput(root, env, "read-tree", "HEAD"); err != nil {
		return nil, err
	}
	if _, err := gitOutput(root, env, "add", "-A", "--"); err != nil {
		return nil, err
	}
	output, err := gitOutputRaw(root, env, nil, "diff", "--cached", "--name-status", "-z", "-M", "HEAD", "--")
	if err != nil {
		return nil, fmt.Errorf("inspect changes: %w", err)
	}
	untrackedOutput, err := gitOutputRaw(root, literalPathEnv(), nil, "ls-files", "--others", "--exclude-standard", "-z", "--")
	if err != nil {
		return nil, fmt.Errorf("inspect untracked files: %w", err)
	}
	untrackedSet := make(map[string]struct{})
	for _, path := range splitNUL(untrackedOutput) {
		untrackedSet[path] = struct{}{}
	}
	fields := splitNUL(output)
	changes := make([]fileChange, 0, len(fields)/2)
	for index := 0; index < len(fields); {
		code := fields[index]
		index++
		if index >= len(fields) {
			return nil, errors.New("Git returned an invalid change list")
		}
		change := fileChange{Status: changeStatus(code), Supported: true}
		if strings.HasPrefix(code, "R") || strings.HasPrefix(code, "C") {
			if index+1 >= len(fields) {
				return nil, errors.New("Git returned an invalid rename entry")
			}
			change.OriginalPath = fields[index]
			change.Path = fields[index+1]
			index += 2
		} else {
			change.Path = fields[index]
			index++
		}
		if change.Status == "added" {
			if _, found := untrackedSet[change.Path]; found {
				change.Status = "untracked"
				change.Worktree = true
			}
		}
		changes = append(changes, change)
	}
	contentIDs, err := temporaryContentIDs(root, env)
	if err != nil {
		return nil, err
	}
	staged, err := changedPathSet(root, "diff", "--cached", "--name-only", "--no-renames", "-z", "HEAD", "--")
	if err != nil {
		return nil, err
	}
	worktree, err := changedPathSet(root, "diff", "--name-only", "--no-renames", "-z", "--")
	if err != nil {
		return nil, err
	}
	conflicted, err := changedPathSet(root, "diff", "--name-only", "--diff-filter=U", "-z", "--")
	if err != nil {
		return nil, err
	}
	for index := range changes {
		change := &changes[index]
		change.ContentID = contentIDs[change.Path]
		change.Staged = pathInSet(change, staged)
		change.Worktree = change.Worktree || pathInSet(change, worktree)
		if pathInSet(change, conflicted) {
			change.Status = "conflicted"
		}
		populateFileDetails(root, change)
	}
	markUnsupportedChanges(root, changes)
	sort.Slice(changes, func(i, j int) bool { return changes[i].Path < changes[j].Path })
	return changes, nil
}

func temporaryContentIDs(root string, env []string) (map[string]string, error) {
	output, err := gitOutputRaw(root, env, nil, "ls-files", "--stage", "-z")
	if err != nil {
		return nil, fmt.Errorf("inspect change content: %w", err)
	}
	result := make(map[string]string)
	for _, entry := range splitNUL(output) {
		parts := strings.SplitN(entry, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		fields := strings.Fields(parts[0])
		if len(fields) == 3 && fields[2] == "0" {
			result[parts[1]] = fields[1]
		}
	}
	return result, nil
}

func changeStatus(code string) string {
	switch code[0] {
	case 'A':
		return "added"
	case 'D':
		return "deleted"
	case 'R', 'C':
		return "renamed"
	case 'U':
		return "conflicted"
	default:
		return "modified"
	}
}

func changedPathSet(root string, args ...string) (map[string]struct{}, error) {
	output, err := gitOutputRaw(root, literalPathEnv(), nil, args...)
	if err != nil {
		return nil, fmt.Errorf("inspect repository state: %w", err)
	}
	result := make(map[string]struct{})
	for _, path := range splitNUL(output) {
		result[path] = struct{}{}
	}
	return result, nil
}

func pathInSet(change *fileChange, values map[string]struct{}) bool {
	if _, found := values[change.Path]; found {
		return true
	}
	_, found := values[change.OriginalPath]
	return change.OriginalPath != "" && found
}

func populateFileDetails(root string, change *fileChange) {
	path := filepath.Join(root, filepath.FromSlash(change.Path))
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		if change.Status == "deleted" {
			if value, sizeErr := gitOutput(root, literalPathEnv(), "cat-file", "-s", "HEAD:"+change.Path); sizeErr == nil {
				change.SizeBytes, _ = strconv.ParseInt(value, 10, 64)
			}
		}
		return
	}
	change.SizeBytes = info.Size()
	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer file.Close()
	buffer := make([]byte, 8192)
	read, _ := file.Read(buffer)
	change.Binary = bytes.IndexByte(buffer[:read], 0) >= 0
}

func markUnsupportedChanges(root string, changes []fileChange) {
	if len(changes) == 0 {
		return
	}
	var input strings.Builder
	for _, change := range changes {
		input.WriteString(change.Path)
		input.WriteByte(0)
	}
	attributes, err := gitOutputRaw(root, literalPathEnv(), strings.NewReader(input.String()), "check-attr", "-z", "--stdin", "filter")
	if err == nil {
		fields := splitNUL(attributes)
		lfs := make(map[string]struct{})
		for index := 0; index+2 < len(fields); index += 3 {
			if fields[index+1] == "filter" && fields[index+2] == "lfs" {
				lfs[fields[index]] = struct{}{}
			}
		}
		for index := range changes {
			if _, found := lfs[changes[index].Path]; found {
				changes[index].Supported = false
				changes[index].ExcludedReason = "Git LFS files are not supported"
			}
		}
	}
	indexOutput, err := gitOutputRaw(root, literalPathEnv(), nil, "ls-files", "--stage", "-z")
	if err != nil {
		return
	}
	submodules := make(map[string]struct{})
	for _, entry := range splitNUL(indexOutput) {
		parts := strings.SplitN(entry, "\t", 2)
		if len(parts) == 2 && strings.HasPrefix(parts[0], "160000 ") {
			submodules[parts[1]] = struct{}{}
		}
	}
	for index := range changes {
		if _, found := submodules[changes[index].Path]; found {
			changes[index].Supported = false
			changes[index].ExcludedReason = "Git submodules are not supported"
		}
	}
}

func nonNilChanges(values []fileChange) []fileChange {
	if values == nil {
		return []fileChange{}
	}
	return values
}

func selectedChangeSummaries(changes []fileChange, selected []string) []fileChange {
	selectedSet := make(map[string]struct{}, len(selected))
	for _, path := range selected {
		selectedSet[path] = struct{}{}
	}
	result := make([]fileChange, 0, len(changes))
	for _, change := range changes {
		_, pathSelected := selectedSet[change.Path]
		_, originalSelected := selectedSet[change.OriginalPath]
		if pathSelected || (change.OriginalPath != "" && originalSelected) {
			result = append(result, change)
		}
	}
	return result
}
