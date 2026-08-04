package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"time"
)

type handoffState struct {
	ID             string `json:"id"`
	OriginalHead   string `json:"original_head"`
	IncomingRef    string `json:"incoming_ref"`
	IncomingCommit string `json:"incoming_commit"`
	StashHash      string `json:"stash_hash,omitempty"`
	Phase          string `json:"phase"`
}

const (
	phasePrepare  = "prepare"
	phaseIncoming = "incoming"
	phaseStash    = "stash"
	phaseFinalize = "finalize"
)

func buildHandoffPackage(packagePath, message string, paths []string) (manifest, error) {
	root, err := repositoryRoot()
	if err != nil {
		return manifest{}, err
	}
	if err := ensureNoOperation(root); err != nil {
		return manifest{}, err
	}
	if _, err := os.Stat(statePath(root)); !errors.Is(err, os.ErrNotExist) {
		return manifest{}, errors.New("a Handoff operation is already active")
	}
	message = strings.TrimSpace(message)
	if len(message) > 500 {
		return manifest{}, errors.New("message cannot exceed 500 characters")
	}
	base, err := gitOutput(root, nil, "rev-parse", "HEAD")
	if err != nil {
		return manifest{}, errors.New("the repository needs at least one commit before creating a handoff")
	}
	tmpDir, err := os.MkdirTemp("", "handoff-git-*")
	if err != nil {
		return manifest{}, err
	}
	defer os.RemoveAll(tmpDir)
	indexPath := filepath.Join(tmpDir, "index")
	gitEnv := append(os.Environ(), "GIT_INDEX_FILE="+indexPath)
	if _, err := gitOutput(root, gitEnv, "read-tree", "HEAD"); err != nil {
		return manifest{}, err
	}
	pathspecs, err := normalizePathspecs(root, paths)
	if err != nil {
		return manifest{}, err
	}
	addArgs := []string{"add", "-A", "--"}
	addArgs = append(addArgs, pathspecs...)
	if _, err := gitOutput(root, gitEnv, addArgs...); err != nil {
		return manifest{}, err
	}
	tree, err := gitOutput(root, gitEnv, "write-tree")
	if err != nil {
		return manifest{}, err
	}
	baseTree, err := gitOutput(root, nil, "rev-parse", "HEAD^{tree}")
	if err != nil {
		return manifest{}, err
	}
	if tree == baseTree {
		return manifest{}, errors.New("there are no changes to hand off")
	}
	changedOutput, err := gitOutputRaw(root, gitEnv, nil, "diff", "--cached", "--name-only", "-z", "HEAD")
	if err != nil {
		return manifest{}, err
	}
	changed := splitNUL(changedOutput)
	if err := rejectUnsupportedPaths(root, gitEnv, changed); err != nil {
		return manifest{}, err
	}

	name, email := gitIdentity(root)
	commitMessage := message
	if commitMessage == "" {
		commitMessage = "Handoff changes"
	}
	identityEnv := append(gitEnv,
		"GIT_AUTHOR_NAME="+name,
		"GIT_AUTHOR_EMAIL="+email,
		"GIT_COMMITTER_NAME="+name,
		"GIT_COMMITTER_EMAIL="+email,
	)
	commit, err := gitOutputRaw(root, identityEnv, strings.NewReader(commitMessage+"\n"), "commit-tree", tree, "-p", base)
	if err != nil {
		return manifest{}, err
	}
	commit = strings.TrimSpace(commit)
	random, err := randomHex(8)
	if err != nil {
		return manifest{}, err
	}
	ref := "refs/handoff/outgoing/" + random
	if _, err := gitOutput(root, nil, "update-ref", ref, commit); err != nil {
		return manifest{}, err
	}
	defer gitOutput(root, nil, "update-ref", "-d", ref)
	bundlePath := filepath.Join(tmpDir, "changes.bundle")
	if _, err := gitOutput(root, nil, "bundle", "create", bundlePath, ref, "^"+base); err != nil {
		return manifest{}, err
	}
	metadata := manifest{
		BaseCommit: base,
		Commit:     commit,
		Ref:        ref,
		CreatedAt:  time.Now().UTC(),
		Message:    message,
		Author:     name,
	}
	if err := createPackage(packagePath, bundlePath, metadata); err != nil {
		return manifest{}, err
	}
	return metadata, nil
}

func applyHandoffPackage(id, packagePath, tmpDir string, stdout io.Writer) error {
	root, err := repositoryRoot()
	if err != nil {
		return err
	}
	if err := ensureNoOperation(root); err != nil {
		return err
	}
	stateFile := statePath(root)
	if _, err := os.Stat(stateFile); !errors.Is(err, os.ErrNotExist) {
		return errors.New("a Handoff operation is already active; use 'handoff continue ID' or 'handoff abort ID'")
	}
	metadata, bundlePath, err := extractPackage(packagePath, tmpDir, defaultMaxBytes)
	if err != nil {
		return err
	}
	if _, err := gitOutput(root, nil, "cat-file", "-e", metadata.BaseCommit+"^{commit}"); err != nil {
		return fmt.Errorf("base commit %s is missing; run git fetch and try again", shortID(metadata.BaseCommit))
	}
	if _, err := gitOutput(root, nil, "bundle", "verify", bundlePath); err != nil {
		return fmt.Errorf("bundle verification failed: %w", err)
	}
	incomingRef := "refs/handoff/incoming/" + id
	defer func() {
		if _, err := os.Stat(stateFile); errors.Is(err, os.ErrNotExist) {
			_, _ = gitOutput(root, nil, "update-ref", "-d", incomingRef)
		}
	}()
	_, _ = gitOutput(root, nil, "update-ref", "-d", incomingRef)
	refspec := metadata.Ref + ":" + incomingRef
	if _, err := gitOutput(root, nil, "fetch", "--quiet", "--no-tags", bundlePath, refspec); err != nil {
		return fmt.Errorf("import bundle: %w", err)
	}
	actualCommit, err := gitOutput(root, nil, "rev-parse", incomingRef)
	if err != nil || actualCommit != metadata.Commit {
		return errors.New("handoff bundle commit does not match its manifest")
	}
	originalHead, err := gitOutput(root, nil, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	state := handoffState{
		ID:             id,
		OriginalHead:   originalHead,
		IncomingRef:    incomingRef,
		IncomingCommit: metadata.Commit,
		Phase:          phasePrepare,
	}
	if err := saveState(stateFile, state); err != nil {
		return err
	}

	dirty, err := worktreeDirty(root)
	if err != nil {
		removeState(stateFile)
		return err
	}
	if dirty {
		before, _ := gitOutput(root, nil, "rev-parse", "-q", "--verify", "refs/stash")
		if _, err := gitOutput(root, nil, "stash", "push", "--include-untracked", "--message", "handoff backup "+id); err != nil {
			removeState(stateFile)
			return fmt.Errorf("backup local changes: %w", err)
		}
		after, err := gitOutput(root, nil, "rev-parse", "refs/stash")
		if err != nil || after == before {
			removeState(stateFile)
			return errors.New("Git did not create the local backup stash")
		}
		state.StashHash = after
		if err := saveState(stateFile, state); err != nil {
			_, _ = gitOutput(root, nil, "stash", "apply", "--index", after)
			return err
		}
	}

	state.Phase = phaseIncoming
	if err := saveState(stateFile, state); err != nil {
		return err
	}
	if _, err := gitOutput(root, nil, "cherry-pick", "--no-commit", metadata.Commit); err != nil {
		if hasUnmerged(root) {
			return conflictError(id, "incoming handoff")
		}
		_ = restoreOriginal(root, state, stateFile)
		return fmt.Errorf("apply incoming changes: %w", err)
	}
	if err := makeTemporaryHead(root, state); err != nil {
		return err
	}
	if err := applyLocalBackup(root, &state, stateFile); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Handoff %s applied successfully. Changes are local and uncommitted.\n", id)
	return nil
}

func continueHandoff(id string, stdout io.Writer) error {
	root, err := repositoryRoot()
	if err != nil {
		return err
	}
	stateFile := statePath(root)
	state, err := loadState(stateFile)
	if err != nil {
		return err
	}
	if state.ID != id {
		return fmt.Errorf("active handoff is %s, not %s", state.ID, id)
	}
	if hasUnmerged(root) {
		return errors.New("conflicts remain; resolve them and run 'git add' on each file before continuing")
	}
	switch state.Phase {
	case phaseIncoming:
		if err := makeTemporaryHead(root, state); err != nil {
			return err
		}
		if err := applyLocalBackup(root, &state, stateFile); err != nil {
			return err
		}
	case phaseStash:
		if err := dropStash(root, state.StashHash); err != nil {
			return err
		}
		if err := finalizeApplied(root, &state, stateFile); err != nil {
			return err
		}
	case phaseFinalize:
		if err := finalizeApplied(root, &state, stateFile); err != nil {
			return err
		}
	default:
		return errors.New("handoff did not reach a conflict; abort it and retry")
	}
	fmt.Fprintf(stdout, "Handoff %s completed. Changes are local and uncommitted.\n", id)
	return nil
}

func abortHandoff(id string, stdout io.Writer) error {
	root, err := repositoryRoot()
	if err != nil {
		return err
	}
	stateFile := statePath(root)
	state, err := loadState(stateFile)
	if err != nil {
		return err
	}
	if state.ID != id {
		return fmt.Errorf("active handoff is %s, not %s", state.ID, id)
	}
	if err := restoreOriginal(root, state, stateFile); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Handoff %s aborted. Original local changes were restored.\n", id)
	return nil
}

func makeTemporaryHead(root string, state handoffState) error {
	tree, err := gitOutput(root, nil, "write-tree")
	if err != nil {
		return fmt.Errorf("write merged tree: %w", err)
	}
	name, email := gitIdentity(root)
	env := append(os.Environ(),
		"GIT_AUTHOR_NAME="+name,
		"GIT_AUTHOR_EMAIL="+email,
		"GIT_COMMITTER_NAME="+name,
		"GIT_COMMITTER_EMAIL="+email,
	)
	commit, err := gitOutputRaw(root, env, strings.NewReader("Temporary Handoff "+state.ID+"\n"), "commit-tree", tree, "-p", state.OriginalHead)
	if err != nil {
		return fmt.Errorf("create temporary merge state: %w", err)
	}
	commit = strings.TrimSpace(commit)
	if _, err := gitOutput(root, nil, "update-ref", "HEAD", commit, state.OriginalHead); err != nil {
		return fmt.Errorf("activate temporary merge state: %w", err)
	}
	_, _ = gitOutput(root, nil, "cherry-pick", "--quit")
	return nil
}

func applyLocalBackup(root string, state *handoffState, stateFile string) error {
	if state.StashHash != "" {
		state.Phase = phaseStash
		if err := saveState(stateFile, *state); err != nil {
			return err
		}
		if _, err := gitOutput(root, nil, "stash", "apply", "--index", state.StashHash); err != nil {
			if hasUnmerged(root) {
				return conflictError(state.ID, "restored local work")
			}
			return fmt.Errorf("restore local backup: %w (backup kept as %s)", err, shortID(state.StashHash))
		}
		if err := dropStash(root, state.StashHash); err != nil {
			return err
		}
	}
	return finalizeApplied(root, state, stateFile)
}

func finalizeApplied(root string, state *handoffState, stateFile string) error {
	state.Phase = phaseFinalize
	if err := saveState(stateFile, *state); err != nil {
		return err
	}
	if _, err := gitOutput(root, nil, "reset", "--mixed", state.OriginalHead); err != nil {
		return fmt.Errorf("restore original HEAD: %w", err)
	}
	finishHandoff(root, *state, stateFile)
	return nil
}

func restoreOriginal(root string, state handoffState, stateFile string) error {
	_, _ = gitOutput(root, nil, "cherry-pick", "--abort")
	if _, err := gitOutput(root, nil, "reset", "--hard", state.OriginalHead); err != nil {
		return fmt.Errorf("restore original HEAD: %w", err)
	}
	if state.StashHash != "" {
		if _, err := gitOutput(root, nil, "stash", "apply", "--index", state.StashHash); err != nil {
			return fmt.Errorf("restore local backup: %w (backup kept as %s)", err, shortID(state.StashHash))
		}
		if err := dropStash(root, state.StashHash); err != nil {
			return err
		}
	}
	finishHandoff(root, state, stateFile)
	return nil
}

func finishHandoff(root string, state handoffState, stateFile string) {
	_, _ = gitOutput(root, nil, "update-ref", "-d", state.IncomingRef)
	removeState(stateFile)
}

func conflictError(id, stage string) error {
	return fmt.Errorf("conflict while applying %s; resolve files, run 'git add', then 'handoff continue %s' (or 'handoff abort %s')", stage, id, id)
}

func repositoryRoot() (string, error) {
	output, err := gitOutput("", nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", errors.New("current directory is not inside a Git repository")
	}
	return output, nil
}

func statePath(root string) string {
	path, err := gitOutput(root, nil, "rev-parse", "--path-format=absolute", "--git-path", "handoff-state.json")
	if err == nil && filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(root, ".git", "handoff-state.json")
}

func saveState(path string, state handoffState) error {
	return writeJSONAtomic(path, state, 0o600)
}

func loadState(path string) (handoffState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return handoffState{}, errors.New("no Handoff operation is active")
		}
		return handoffState{}, err
	}
	var state handoffState
	if err := json.Unmarshal(data, &state); err != nil {
		return handoffState{}, errors.New("Handoff recovery state is corrupted")
	}
	return state, nil
}

func removeState(path string) {
	_ = os.Remove(path)
}

func ensureNoOperation(root string) error {
	if hasUnmerged(root) {
		return errors.New("repository has unresolved conflicts")
	}
	for _, name := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "rebase-merge", "rebase-apply"} {
		path, err := gitOutput(root, nil, "rev-parse", "--path-format=absolute", "--git-path", name)
		if err == nil {
			if _, statErr := os.Stat(path); statErr == nil {
				return fmt.Errorf("repository has an active Git operation (%s)", name)
			}
		}
	}
	return nil
}

func hasUnmerged(root string) bool {
	output, err := gitOutput(root, nil, "diff", "--name-only", "--diff-filter=U")
	return err == nil && output != ""
}

func worktreeDirty(root string) (bool, error) {
	output, err := gitOutputRaw(root, nil, nil, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	return output != "", err
}

func dropStash(root, hash string) error {
	if hash == "" {
		return nil
	}
	output, err := gitOutput(root, nil, "stash", "list", "--format=%H %gd")
	if err != nil {
		return err
	}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == hash {
			_, err := gitOutput(root, nil, "stash", "drop", fields[1])
			return err
		}
	}
	return fmt.Errorf("backup stash %s was not found; it was not deleted", shortID(hash))
}

func normalizePathspecs(root string, paths []string) ([]string, error) {
	if len(paths) == 0 {
		return []string{"."}, nil
	}
	result := make([]string, 0, len(paths))
	workingDir, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	for _, path := range paths {
		absolute := path
		if !filepath.IsAbs(path) {
			absolute = filepath.Join(workingDir, path)
		}
		relative, err := filepath.Rel(root, filepath.Clean(absolute))
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("path %q is outside the repository", path)
		}
		result = append(result, filepath.ToSlash(relative))
	}
	return result, nil
}

func rejectUnsupportedPaths(root string, env []string, paths []string) error {
	for _, path := range paths {
		attribute, _ := gitOutput(root, env, "check-attr", "filter", "--", path)
		if strings.HasSuffix(attribute, ": filter: lfs") {
			return fmt.Errorf("Git LFS change %q is not supported in version 1", path)
		}
		indexEntry, _ := gitOutput(root, env, "ls-files", "-s", "--", path)
		baseEntry, _ := gitOutput(root, nil, "ls-tree", "HEAD", "--", path)
		if strings.HasPrefix(indexEntry, "160000 ") || strings.HasPrefix(baseEntry, "160000 ") {
			return fmt.Errorf("submodule change %q is not supported in version 1", path)
		}
	}
	return nil
}

func gitIdentity(root string) (string, string) {
	name, _ := gitOutput(root, nil, "config", "--get", "user.name")
	email, _ := gitOutput(root, nil, "config", "--get", "user.email")
	if name == "" {
		if current, err := user.Current(); err == nil && current.Username != "" {
			name = current.Username
		} else {
			name = "Handoff User"
		}
	}
	if email == "" {
		email = "handoff@localhost"
	}
	return name, email
}

func randomHex(bytesCount int) (string, error) {
	data := make([]byte, bytesCount)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return hex.EncodeToString(data), nil
}

func shortID(value string) string {
	if len(value) > 12 {
		return value[:12]
	}
	return value
}

func splitNUL(value string) []string {
	parts := strings.Split(value, "\x00")
	result := parts[:0]
	for _, part := range parts {
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}

func gitOutput(dir string, env []string, args ...string) (string, error) {
	output, err := gitOutputRaw(dir, env, nil, args...)
	return strings.TrimSpace(output), err
}

func gitOutputRaw(dir string, env []string, input io.Reader, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		command.Dir = dir
	}
	if env == nil {
		env = os.Environ()
	}
	command.Env = append(env, "GIT_TERMINAL_PROMPT=0")
	command.Stdin = input
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		if detail == "" {
			detail = err.Error()
		}
		return "", errors.New(detail)
	}
	return stdout.String(), nil
}
