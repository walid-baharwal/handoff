package handoff

import (
	"bufio"
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
	pathpkg "path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type handoffState struct {
	ID             string `json:"id"`
	OriginalHead   string `json:"original_head"`
	IncomingRef    string `json:"incoming_ref"`
	IncomingCommit string `json:"incoming_commit"`
	TemporaryHead  string `json:"temporary_head,omitempty"`
	StashHash      string `json:"stash_hash,omitempty"`
	PrivateBackup  bool   `json:"private_backup,omitempty"`
	Phase          string `json:"phase"`
}

const (
	phasePrepare         = "prepare"
	phaseIncoming        = "incoming"
	phaseRestore         = "restore"
	phaseStash           = "stash"
	phaseFinalize        = "finalize"
	maxIncomingPaths     = 10_000
	maxIncomingDiffBytes = 48 << 20
	maxIncomingPathBytes = 4096
)

type pushMode string

const (
	pushModeAll      pushMode = "all"
	pushModeStaged   pushMode = "staged"
	pushModeWorktree pushMode = "worktree"
)

func buildHandoffPackage(packagePath, message string, paths []string) (manifest, error) {
	root, err := repositoryRoot()
	if err != nil {
		return manifest{}, err
	}
	selected, err := selectHandoffPaths(root, paths, nil, pushModeAll)
	if err != nil {
		return manifest{}, err
	}
	return buildHandoffPackageFromPaths(root, packagePath, message, selected, pushModeAll)
}

func buildHandoffPackageFromPaths(root, packagePath, message string, paths []string, mode pushMode) (manifest, error) {
	releaseLock, err := acquireOperationLock(root)
	if err != nil {
		return manifest{}, err
	}
	defer releaseLock()
	if err := ensureNoOperation(root); err != nil {
		return manifest{}, err
	}
	if _, err := os.Stat(statePath(root)); err == nil {
		return manifest{}, commandError("recovery_active", errors.New("a Handoff operation is already active"))
	} else if !errors.Is(err, os.ErrNotExist) {
		return manifest{}, err
	}
	message = strings.TrimSpace(message)
	if len(message) > 500 {
		return manifest{}, invalidArguments("message cannot exceed 500 characters")
	}
	base, err := gitOutput(root, nil, "rev-parse", "HEAD")
	if err != nil {
		return manifest{}, commandError("repository_has_no_commits", errors.New("the repository needs at least one commit before creating a handoff"))
	}
	tmpDir, err := os.MkdirTemp("", "handoff-git-*")
	if err != nil {
		return manifest{}, err
	}
	defer os.RemoveAll(tmpDir)
	indexPath := filepath.Join(tmpDir, "index")
	gitEnv := gitEnvironment("GIT_INDEX_FILE="+indexPath, "GIT_LITERAL_PATHSPECS=1")
	if _, err := gitOutput(root, gitEnv, "read-tree", "HEAD"); err != nil {
		return manifest{}, err
	}
	if mode == pushModeStaged {
		patchArgs := []string{"diff", "--cached", "--binary", "--full-index", "--no-renames", "HEAD", "--"}
		patchArgs = append(patchArgs, paths...)
		patch, err := gitOutputRaw(root, literalPathEnv(), nil, patchArgs...)
		if err != nil {
			return manifest{}, err
		}
		if strings.TrimSpace(patch) == "" {
			return manifest{}, commandError("no_changes", errors.New("there are no staged changes to hand off"))
		}
		if _, err := gitOutputRaw(root, gitEnv, strings.NewReader(patch), "apply", "--cached", "--binary", "--whitespace=nowarn"); err != nil {
			return manifest{}, fmt.Errorf("prepare staged changes: %w", err)
		}
	} else {
		addArgs := []string{"add", "-A", "--"}
		addArgs = append(addArgs, paths...)
		if _, err := gitOutput(root, gitEnv, addArgs...); err != nil {
			return manifest{}, err
		}
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
		return manifest{}, commandError("no_changes", errors.New("there are no changes to hand off"))
	}
	changedOutput, err := gitOutputRaw(root, gitEnv, nil, "diff", "--cached", "--name-only", "-z", "HEAD")
	if err != nil {
		return manifest{}, err
	}
	changed := splitNUL(changedOutput)
	if len(changed) > maxIncomingPaths {
		return manifest{}, fmt.Errorf("handoff changes more than %d paths", maxIncomingPaths)
	}
	for _, path := range changed {
		if len(path) > maxIncomingPathBytes {
			return manifest{}, fmt.Errorf("handoff path exceeds %d bytes", maxIncomingPathBytes)
		}
		if !utf8.ValidString(path) {
			return manifest{}, fmt.Errorf("handoff path %q is not valid UTF-8", path)
		}
	}
	if err := rejectUnsupportedPaths(root, gitEnv, changed); err != nil {
		return manifest{}, err
	}

	name, email := gitIdentity(root)
	repositoryID, project, branch := repositoryDetails(root, base)
	listedFiles := changed
	filesTruncated := false
	if len(listedFiles) > maxListedFiles {
		listedFiles = listedFiles[:maxListedFiles]
		filesTruncated = true
	}
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
	_, objects, err := inspectIncomingChanges(root, nil, base, commit)
	if err != nil {
		return manifest{}, fmt.Errorf("inspect outgoing paths: %w", err)
	}
	if err := enforceExpandedSize(root, nil, objects, defaultMaxBytes); err != nil {
		return manifest{}, err
	}
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
		BaseCommit:     base,
		Commit:         commit,
		Ref:            ref,
		CreatedAt:      time.Now().UTC(),
		Message:        message,
		Author:         name,
		Project:        project,
		RepositoryID:   repositoryID,
		Branch:         branch,
		FileCount:      len(changed),
		Files:          append([]string(nil), listedFiles...),
		FilesTruncated: filesTruncated,
	}
	if err := createPackage(packagePath, bundlePath, metadata); err != nil {
		return manifest{}, err
	}
	return metadata, nil
}

func selectHandoffPaths(root string, paths, excludes []string, mode pushMode) ([]string, error) {
	pathspecs, err := normalizePathspecs(root, paths)
	if err != nil {
		return nil, err
	}
	selected, err := changedPaths(root, mode, pathspecs)
	if err != nil {
		return nil, err
	}
	if len(excludes) > 0 {
		excludePathspecs, err := normalizePathspecs(root, excludes)
		if err != nil {
			return nil, err
		}
		excluded, err := changedPaths(root, mode, excludePathspecs)
		if err != nil {
			return nil, err
		}
		excludedSet := make(map[string]struct{}, len(excluded))
		for _, path := range excluded {
			excludedSet[path] = struct{}{}
		}
		filtered := selected[:0]
		for _, path := range selected {
			if _, found := excludedSet[path]; !found {
				filtered = append(filtered, path)
			}
		}
		selected = filtered
	}
	if len(selected) == 0 {
		switch mode {
		case pushModeStaged:
			return nil, commandError("no_changes", errors.New("there are no staged changes matching the selected paths"))
		case pushModeWorktree:
			return nil, commandError("no_changes", errors.New("there are no worktree changes matching the selected paths"))
		default:
			return nil, commandError("no_changes", errors.New("there are no changes matching the selected paths"))
		}
	}
	return selected, nil
}

func changedPaths(root string, mode pushMode, pathspecs []string) ([]string, error) {
	env := literalPathEnv()
	var diffArgs []string
	switch mode {
	case pushModeStaged:
		diffArgs = []string{"diff", "--cached", "--no-renames", "--name-only", "-z", "HEAD", "--"}
	case pushModeWorktree:
		diffArgs = []string{"diff", "--no-renames", "--name-only", "-z", "--"}
	case pushModeAll:
		diffArgs = []string{"diff", "--no-renames", "--name-only", "-z", "HEAD", "--"}
	default:
		return nil, fmt.Errorf("unsupported push mode %q", mode)
	}
	diffArgs = append(diffArgs, pathspecs...)
	tracked, err := gitOutputRaw(root, env, nil, diffArgs...)
	if err != nil {
		return nil, err
	}
	unique := make(map[string]struct{})
	for _, path := range splitNUL(tracked) {
		unique[path] = struct{}{}
	}
	if mode != pushModeStaged {
		untrackedArgs := []string{"ls-files", "--others", "--exclude-standard", "-z", "--"}
		untrackedArgs = append(untrackedArgs, pathspecs...)
		untracked, err := gitOutputRaw(root, env, nil, untrackedArgs...)
		if err != nil {
			return nil, err
		}
		for _, path := range splitNUL(untracked) {
			unique[path] = struct{}{}
		}
	}
	result := make([]string, 0, len(unique))
	for path := range unique {
		result = append(result, path)
	}
	sort.Strings(result)
	return result, nil
}

func literalPathEnv() []string {
	return gitEnvironment("GIT_LITERAL_PATHSPECS=1")
}

func applyHandoffPackage(id, packagePath, tmpDir string, stdout io.Writer) error {
	root, err := repositoryRoot()
	if err != nil {
		return err
	}
	releaseLock, err := acquireOperationLock(root)
	if err != nil {
		return err
	}
	defer releaseLock()
	if err := ensureNoOperation(root); err != nil {
		return err
	}
	stateFile := statePath(root)
	if _, err := os.Stat(stateFile); err == nil {
		return commandError("recovery_active", errors.New("a Handoff operation is already active; use 'handoff continue ID' or 'handoff abort ID'"))
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
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
	validationRoot := filepath.Join(tmpDir, "validation.git")
	if err := os.Mkdir(validationRoot, 0o700); err != nil {
		return err
	}
	if _, err := gitOutput(validationRoot, nil, "init", "--bare", "."); err != nil {
		return fmt.Errorf("initialize bundle validation: %w", err)
	}
	objectDir, err := gitOutput(root, nil, "rev-parse", "--path-format=absolute", "--git-path", "objects")
	if err != nil {
		return fmt.Errorf("locate repository objects: %w", err)
	}
	if err := os.WriteFile(filepath.Join(validationRoot, "objects", "info", "alternates"), []byte(strconv.Quote(objectDir)+"\n"), 0o600); err != nil {
		return fmt.Errorf("configure bundle validation: %w", err)
	}
	incomingRef := "refs/handoff/incoming/" + id
	localBackupRef := backupRef(id)
	localTemporaryRef := temporaryRef(id)
	defer func() {
		if _, err := os.Stat(stateFile); errors.Is(err, os.ErrNotExist) {
			_, _ = gitOutput(root, nil, "update-ref", "-d", incomingRef)
			_, _ = gitOutput(root, nil, "update-ref", "-d", localBackupRef)
			_, _ = gitOutput(root, nil, "update-ref", "-d", localTemporaryRef)
		}
	}()
	refspec := metadata.Ref + ":" + incomingRef
	if _, err := gitOutput(validationRoot, nil, "fetch", "--quiet", "--no-tags", bundlePath, refspec); err != nil {
		return fmt.Errorf("validate bundle import: %w", err)
	}
	actualCommit, err := gitOutput(validationRoot, nil, "rev-parse", incomingRef)
	if err != nil || actualCommit != metadata.Commit {
		return errors.New("handoff bundle commit does not match its manifest")
	}
	if err := validateCommitParent(validationRoot, nil, metadata.Commit, metadata.BaseCommit); err != nil {
		return err
	}
	incomingPaths, incomingObjects, err := inspectIncomingChanges(validationRoot, nil, metadata.BaseCommit, metadata.Commit)
	if err != nil {
		return fmt.Errorf("inspect incoming paths: %w", err)
	}
	if err := enforceExpandedSize(validationRoot, nil, incomingObjects, defaultMaxBytes); err != nil {
		return err
	}
	if err := validateFileSummary(metadata, incomingPaths); err != nil {
		return err
	}
	localChanged, err := changedWorktreePaths(root)
	if err != nil {
		return fmt.Errorf("inspect local changes: %w", err)
	}
	if err := rejectResetPathCollisions(root, localChanged); err != nil {
		return err
	}
	protectedPaths := append(append([]string(nil), incomingPaths...), localChanged...)
	if err := rejectLocalPathCollisions(root, protectedPaths); err != nil {
		return err
	}
	_, _ = gitOutput(root, nil, "update-ref", "-d", incomingRef)
	_, _ = gitOutput(root, nil, "update-ref", "-d", localBackupRef)
	validatedRefspec := incomingRef + ":" + incomingRef
	if _, err := gitOutput(root, nil, "fetch", "--quiet", "--no-tags", validationRoot, validatedRefspec); err != nil {
		return fmt.Errorf("import validated bundle: %w", err)
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

	dirty, err := trackedWorktreeDirty(root)
	if err != nil {
		return errors.Join(err, removeState(stateFile))
	}
	if dirty {
		backupHash, err := gitOutput(root, nil, "stash", "create", "handoff backup "+id)
		if err != nil {
			return errors.Join(fmt.Errorf("backup local changes: %w", err), removeState(stateFile))
		}
		if backupHash == "" {
			return errors.Join(errors.New("Git did not create the local backup stash"), removeState(stateFile))
		}
		if _, err := gitOutput(root, nil, "update-ref", localBackupRef, backupHash); err != nil {
			return errors.Join(fmt.Errorf("protect local backup: %w", err), removeState(stateFile))
		}
		state.StashHash = backupHash
		state.PrivateBackup = true
		if err := saveState(stateFile, state); err != nil {
			return err
		}
		if _, err := gitOutput(root, nil, "reset", "--hard", originalHead); err != nil {
			return fmt.Errorf("prepare working tree: %w; local backup kept, stop processes using repository files and run 'handoff abort %s'", err, id)
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
		if restoreErr := restoreOriginal(root, state, stateFile); restoreErr != nil {
			return fmt.Errorf("apply incoming changes: %v; restore local state: %w", err, restoreErr)
		}
		return fmt.Errorf("apply incoming changes: %w", err)
	}
	if err := makeTemporaryHead(root, &state, stateFile); err != nil {
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
	releaseLock, err := acquireOperationLock(root)
	if err != nil {
		return err
	}
	defer releaseLock()
	stateFile := statePath(root)
	state, err := loadState(stateFile)
	if err != nil {
		return err
	}
	if state.ID != id {
		return commandError("active_handoff_mismatch", fmt.Errorf("active handoff is %s, not %s", state.ID, id))
	}
	if err := validateStateObjects(root, state, true); err != nil {
		return err
	}
	if hasUnmerged(root) {
		return commandError("conflicts_unresolved", errors.New("conflicts remain; resolve them and run 'git add' on each file before continuing"))
	}
	switch state.Phase {
	case phaseIncoming:
		if err := makeTemporaryHead(root, &state, stateFile); err != nil {
			return err
		}
		if err := applyLocalBackup(root, &state, stateFile); err != nil {
			return err
		}
	case phaseRestore:
		return fmt.Errorf("local backup restore did not complete; run 'handoff abort %s' and retry the pull", id)
	case phaseStash:
		if err := releaseBackup(root, state); err != nil {
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
	releaseLock, err := acquireOperationLock(root)
	if err != nil {
		return err
	}
	defer releaseLock()
	stateFile := statePath(root)
	state, err := loadState(stateFile)
	if err != nil {
		return err
	}
	if state.ID != id {
		return commandError("active_handoff_mismatch", fmt.Errorf("active handoff is %s, not %s", state.ID, id))
	}
	if err := validateStateObjects(root, state, false); err != nil {
		return err
	}
	if err := restoreOriginal(root, state, stateFile); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "Handoff %s aborted. Original local changes were restored.\n", id)
	return nil
}

func makeTemporaryHead(root string, state *handoffState, stateFile string) error {
	tree, err := gitOutput(root, nil, "write-tree")
	if err != nil {
		return fmt.Errorf("write merged tree: %w", err)
	}
	if state.TemporaryHead == "" {
		name, email := gitIdentity(root)
		env := gitEnvironment(
			"GIT_AUTHOR_NAME="+name,
			"GIT_AUTHOR_EMAIL="+email,
			"GIT_COMMITTER_NAME="+name,
			"GIT_COMMITTER_EMAIL="+email,
		)
		commit, err := gitOutputRaw(root, env, strings.NewReader("Temporary Handoff "+state.ID+"\n"), "commit-tree", tree, "-p", state.OriginalHead)
		if err != nil {
			return fmt.Errorf("create temporary merge state: %w", err)
		}
		state.TemporaryHead = strings.TrimSpace(commit)
		if _, err := gitOutput(root, nil, "update-ref", temporaryRef(state.ID), state.TemporaryHead); err != nil {
			return fmt.Errorf("protect temporary merge state: %w", err)
		}
		if err := saveState(stateFile, *state); err != nil {
			return err
		}
	}
	currentHead, err := gitOutput(root, nil, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if currentHead == state.TemporaryHead {
		_, _ = gitOutput(root, nil, "cherry-pick", "--quit")
		return nil
	}
	if currentHead != state.OriginalHead {
		return commandError("recovery_state_invalid", errors.New("repository HEAD changed during Handoff recovery"))
	}
	if _, err := gitOutput(root, nil, "update-ref", "HEAD", state.TemporaryHead, state.OriginalHead); err != nil {
		return fmt.Errorf("activate temporary merge state: %w", err)
	}
	_, _ = gitOutput(root, nil, "cherry-pick", "--quit")
	return nil
}

func applyLocalBackup(root string, state *handoffState, stateFile string) error {
	if state.StashHash != "" {
		state.Phase = phaseRestore
		if err := saveState(stateFile, *state); err != nil {
			return err
		}
		if _, err := gitOutput(root, nil, "stash", "apply", "--index", state.StashHash); err != nil {
			if hasUnmerged(root) {
				state.Phase = phaseStash
				if saveErr := saveState(stateFile, *state); saveErr != nil {
					return fmt.Errorf("save conflict recovery state: %w", saveErr)
				}
				return conflictError(state.ID, "restored local work")
			}
			return fmt.Errorf("restore local backup: %w (backup kept as %s; run 'handoff abort %s')", err, shortID(state.StashHash), state.ID)
		}
		state.Phase = phaseStash
		if err := saveState(stateFile, *state); err != nil {
			return err
		}
		if err := releaseBackup(root, *state); err != nil {
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
	return finishHandoff(root, *state, stateFile)
}

func restoreOriginal(root string, state handoffState, stateFile string) error {
	if state.Phase == phasePrepare && state.StashHash == "" {
		return finishHandoff(root, state, stateFile)
	}
	_, _ = gitOutput(root, nil, "cherry-pick", "--abort")
	if _, err := gitOutput(root, nil, "reset", "--hard", state.OriginalHead); err != nil {
		return fmt.Errorf("restore original HEAD: %w", err)
	}
	if state.StashHash != "" {
		if _, err := gitOutput(root, nil, "stash", "apply", "--index", state.StashHash); err != nil {
			return fmt.Errorf("restore local backup: %w (backup kept as %s)", err, shortID(state.StashHash))
		}
		if err := releaseBackup(root, state); err != nil {
			return err
		}
	}
	return finishHandoff(root, state, stateFile)
}

func finishHandoff(root string, state handoffState, stateFile string) error {
	if err := removeState(stateFile); err != nil {
		return err
	}
	_, _ = gitOutput(root, nil, "update-ref", "-d", state.IncomingRef)
	_, _ = gitOutput(root, nil, "update-ref", "-d", backupRef(state.ID))
	_, _ = gitOutput(root, nil, "update-ref", "-d", temporaryRef(state.ID))
	return nil
}

func backupRef(id string) string {
	return "refs/handoff/backups/" + id
}

func temporaryRef(id string) string {
	return "refs/handoff/temporary/" + id
}

func conflictError(id, stage string) error {
	return commandError("conflict", fmt.Errorf("conflict while applying %s; resolve files, run 'git add', then 'handoff continue %s' (or 'handoff abort %s')", stage, id, id))
}

func repositoryRoot() (string, error) {
	output, err := gitOutput("", nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", commandError("repository_required", errors.New("current directory is not inside a Git repository"))
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
			return handoffState{}, commandError("no_active_recovery", errors.New("no Handoff operation is active"))
		}
		return handoffState{}, err
	}
	var state handoffState
	if err := json.Unmarshal(data, &state); err != nil {
		return handoffState{}, errors.New("Handoff recovery state is corrupted")
	}
	if !idPattern.MatchString(state.ID) || !objectIDPattern.MatchString(state.OriginalHead) || !objectIDPattern.MatchString(state.IncomingCommit) {
		return handoffState{}, errors.New("Handoff recovery state contains invalid identifiers")
	}
	if state.IncomingRef != "refs/handoff/incoming/"+state.ID || (state.StashHash != "" && !objectIDPattern.MatchString(state.StashHash)) || (state.TemporaryHead != "" && !objectIDPattern.MatchString(state.TemporaryHead)) {
		return handoffState{}, errors.New("Handoff recovery state contains invalid references")
	}
	if state.PrivateBackup && state.StashHash == "" {
		return handoffState{}, errors.New("Handoff recovery state contains an invalid backup")
	}
	switch state.Phase {
	case phasePrepare, phaseIncoming, phaseRestore, phaseStash, phaseFinalize:
	default:
		return handoffState{}, errors.New("Handoff recovery state contains an invalid phase")
	}
	if (state.Phase == phaseRestore || state.Phase == phaseStash) && state.StashHash == "" {
		return handoffState{}, errors.New("Handoff recovery state is missing its local backup")
	}
	return state, nil
}

func validateStateObjects(root string, state handoffState, forContinue bool) error {
	for name, object := range map[string]string{
		"original HEAD": state.OriginalHead,
		"local backup":  state.StashHash,
	} {
		if object == "" {
			continue
		}
		if _, err := gitOutput(root, nil, "cat-file", "-e", object+"^{commit}"); err != nil {
			return commandError("recovery_state_invalid", fmt.Errorf("Handoff recovery %s is missing or invalid", name))
		}
	}
	if state.StashHash != "" {
		line, err := gitOutput(root, nil, "rev-list", "--parents", "-n", "1", state.StashHash)
		fields := strings.Fields(line)
		if err != nil || len(fields) < 3 || fields[0] != state.StashHash || fields[1] != state.OriginalHead {
			return commandError("recovery_state_invalid", errors.New("Handoff recovery local backup is not based on the original HEAD"))
		}
	}
	if forContinue && state.Phase == phaseIncoming {
		if state.TemporaryHead == "" {
			if _, err := gitOutput(root, nil, "cat-file", "-e", state.IncomingCommit+"^{commit}"); err != nil {
				return commandError("recovery_state_invalid", errors.New("Handoff recovery incoming commit is missing or invalid"))
			}
			incoming, err := gitOutput(root, nil, "rev-parse", state.IncomingRef)
			if err != nil || incoming != state.IncomingCommit {
				return commandError("recovery_state_invalid", errors.New("Handoff recovery incoming reference is missing or invalid"))
			}
		} else {
			if err := validateCommitParent(root, nil, state.TemporaryHead, state.OriginalHead); err != nil {
				return commandError("recovery_state_invalid", errors.New("Handoff recovery temporary HEAD is missing or invalid"))
			}
		}
	}
	return nil
}

func acquireOperationLock(root string) (func(), error) {
	path, err := gitOutput(root, nil, "rev-parse", "--path-format=absolute", "--git-path", "handoff-operation.lock")
	if err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lockFile(file); err != nil {
		file.Close()
		return nil, commandError("operation_active", errors.New("another Handoff command is already changing this repository"))
	}
	return func() {
		_ = unlockFile(file)
		_ = file.Close()
	}, nil
}

func removeState(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return syncDirectory(filepath.Dir(path))
}

func ensureNoOperation(root string) error {
	if hasUnmerged(root) {
		return commandError("repository_conflict", errors.New("repository has unresolved conflicts"))
	}
	for _, name := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "rebase-merge", "rebase-apply"} {
		path, err := gitOutput(root, nil, "rev-parse", "--path-format=absolute", "--git-path", name)
		if err == nil {
			if _, statErr := os.Stat(path); statErr == nil {
				return commandError("git_operation_active", fmt.Errorf("repository has an active Git operation (%s)", name))
			}
		}
	}
	return nil
}

func hasUnmerged(root string) bool {
	output, err := gitOutput(root, nil, "diff", "--name-only", "--diff-filter=U")
	return err == nil && output != ""
}

func trackedWorktreeDirty(root string) (bool, error) {
	output, err := gitOutputRaw(root, nil, nil, "status", "--porcelain=v1", "-z", "--untracked-files=no")
	return output != "", err
}

func validateCommitParent(root string, env []string, commit, expectedParent string) error {
	line, err := gitOutput(root, env, "rev-list", "--parents", "-n", "1", commit)
	if err != nil {
		return err
	}
	fields := strings.Fields(line)
	if len(fields) != 2 || fields[0] != commit || fields[1] != expectedParent {
		return errors.New("handoff commit must have the manifest base commit as its only parent")
	}
	return nil
}

func inspectIncomingChanges(root string, env []string, base, commit string) ([]string, []string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "git", "diff", "--raw", "--no-abbrev", "--no-renames", "-z", base, commit, "--")
	command.Dir = root
	if env == nil {
		env = gitEnvironment()
	}
	command.Env = append(append([]string(nil), env...), "GIT_LITERAL_PATHSPECS=1", "GIT_TERMINAL_PROMPT=0")
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		return nil, nil, err
	}
	reader := bufio.NewReader(stdout)
	remaining := int64(maxIncomingDiffBytes)
	paths := make([]string, 0)
	objects := make([]string, 0)
	for {
		raw, readErr := readNULField(reader, &remaining, 512)
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			cancel()
			_ = command.Wait()
			return nil, nil, readErr
		}
		path, readErr := readNULField(reader, &remaining, maxIncomingPathBytes)
		if readErr != nil {
			cancel()
			_ = command.Wait()
			return nil, nil, errors.New("invalid or oversized path in raw Git diff")
		}
		if !utf8.ValidString(path) {
			cancel()
			_ = command.Wait()
			return nil, nil, errors.New("incoming Git path is not valid UTF-8")
		}
		fields := strings.Fields(raw)
		if len(fields) != 5 || !strings.HasPrefix(fields[0], ":") {
			cancel()
			_ = command.Wait()
			return nil, nil, errors.New("invalid raw Git diff entry")
		}
		if len(paths) == maxIncomingPaths {
			cancel()
			_ = command.Wait()
			return nil, nil, fmt.Errorf("handoff changes more than %d paths", maxIncomingPaths)
		}
		paths = append(paths, path)
		newMode := fields[1]
		if newMode == "160000" {
			cancel()
			_ = command.Wait()
			return nil, nil, fmt.Errorf("submodule change %q is not supported in version 1", path)
		}
		if newMode != "000000" {
			objects = append(objects, fields[3])
		}
	}
	if err := command.Wait(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = err.Error()
		}
		return nil, nil, errors.New(detail)
	}
	return paths, objects, nil
}

func validateFileSummary(metadata manifest, paths []string) error {
	if metadata.FileCount != len(paths) || metadata.FilesTruncated != (len(paths) > maxListedFiles) || len(metadata.Files) != min(len(paths), maxListedFiles) {
		return errors.New("handoff file summary does not match its Git changes")
	}
	actual := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		actual[path] = struct{}{}
	}
	seen := make(map[string]struct{}, len(metadata.Files))
	for _, path := range metadata.Files {
		if _, exists := actual[path]; !exists {
			return errors.New("handoff file summary does not match its Git changes")
		}
		if _, duplicate := seen[path]; duplicate {
			return errors.New("handoff file summary contains duplicate paths")
		}
		seen[path] = struct{}{}
	}
	return nil
}

func readNULField(reader *bufio.Reader, remaining *int64, maximum int) (string, error) {
	var field []byte
	for {
		fragment, err := reader.ReadSlice(0)
		*remaining -= int64(len(fragment))
		if *remaining < 0 || len(field)+len(fragment) > maximum+1 {
			return "", errors.New("incoming Git diff exceeds its safety limit")
		}
		field = append(field, fragment...)
		if err == nil {
			return string(field[:len(field)-1]), nil
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			if errors.Is(err, io.EOF) && len(field) == 0 {
				return "", io.EOF
			}
			return "", errors.New("unterminated raw Git diff entry")
		}
	}
}

func enforceExpandedSize(root string, env []string, objects []string, maximum int64) error {
	if len(objects) == 0 {
		return nil
	}
	input := strings.NewReader(strings.Join(objects, "\n") + "\n")
	output, err := gitOutputRaw(root, env, input, "cat-file", "--batch-check=%(objecttype) %(objectsize)")
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) != len(objects) {
		return errors.New("Git returned an incomplete incoming-object summary")
	}
	var total int64
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != "blob" {
			return errors.New("handoff contains an invalid incoming object")
		}
		size, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil || size < 0 || size > maximum-total {
			return commandError("package_too_large", fmt.Errorf("incoming files exceed the local %s expanded-size limit", formatBytes(maximum)))
		}
		total += size
	}
	return nil
}

func changedWorktreePaths(root string) ([]string, error) {
	output, err := gitOutputRaw(root, literalPathEnv(), nil, "diff", "--name-only", "--no-renames", "-z", "HEAD", "--")
	if err != nil {
		return nil, err
	}
	return splitNUL(output), nil
}

func rejectResetPathCollisions(root string, paths []string) error {
	for _, path := range paths {
		parts := strings.Split(filepath.FromSlash(path), string(filepath.Separator))
		current := root
		for index, part := range parts {
			current = filepath.Join(current, part)
			info, err := os.Lstat(current)
			if errors.Is(err, os.ErrNotExist) {
				break
			}
			if err != nil {
				return err
			}
			if index < len(parts)-1 && !info.IsDir() {
				return commandError("local_path_collision", fmt.Errorf("local path %q would be overwritten while resetting tracked path %q; move or back up the local path and retry", filepath.ToSlash(strings.Join(parts[:index+1], string(filepath.Separator))), path))
			}
			if index == len(parts)-1 && info.IsDir() {
				return commandError("local_path_collision", fmt.Errorf("a local directory would be overwritten while resetting tracked path %q; move or back up the directory and retry", path))
			}
		}
	}
	return nil
}

func rejectLocalPathCollisions(root string, incoming []string) error {
	ignoreCase := false
	if value, err := gitOutput(root, nil, "config", "--bool", "core.ignoreCase"); err == nil {
		ignoreCase = value == "true"
	}
	output, err := gitOutputRaw(root, nil, nil, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignored=matching")
	if err != nil {
		return err
	}
	localPaths := make([]string, 0)
	for _, entry := range splitNUL(output) {
		if len(entry) < 4 || (entry[:2] != "??" && entry[:2] != "!!") {
			continue
		}
		local := strings.TrimSuffix(entry[3:], "/")
		localPaths = append(localPaths, local)
	}
	if local, path, collided := findPathCollision(localPaths, incoming, ignoreCase); collided {
		return commandError("local_path_collision", fmt.Errorf("local untracked or ignored path %q would be overwritten by incoming path %q; move or back up the local path and retry", local, path))
	}
	return nil
}

func findPathCollision(localPaths, incomingPaths []string, ignoreCase bool) (string, string, bool) {
	key := func(path string) string {
		if ignoreCase {
			return strings.ToLower(path)
		}
		return path
	}
	parent := func(path string) string {
		if index := strings.LastIndexByte(path, '/'); index >= 0 {
			return path[:index]
		}
		return ""
	}
	localByKey := make(map[string]string, len(localPaths))
	incomingByKey := make(map[string]string, len(incomingPaths))
	for _, path := range localPaths {
		localByKey[key(path)] = path
	}
	for _, path := range incomingPaths {
		incomingByKey[key(path)] = path
	}
	for _, local := range localPaths {
		localKey := key(local)
		if incoming, exists := incomingByKey[localKey]; exists {
			return local, incoming, true
		}
		for ancestor := parent(localKey); ancestor != ""; ancestor = parent(ancestor) {
			if incoming, exists := incomingByKey[ancestor]; exists {
				return local, incoming, true
			}
		}
	}
	for _, incoming := range incomingPaths {
		for ancestor := parent(key(incoming)); ancestor != ""; ancestor = parent(ancestor) {
			if local, exists := localByKey[ancestor]; exists {
				return local, incoming, true
			}
		}
	}
	return "", "", false
}

func releaseBackup(root string, state handoffState) error {
	if state.StashHash == "" || state.PrivateBackup {
		return nil
	}
	output, err := gitOutput(root, nil, "stash", "list", "--format=%H %gd")
	if err != nil {
		return err
	}
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == state.StashHash {
			_, err := gitOutput(root, nil, "stash", "drop", fields[1])
			return err
		}
	}
	return nil
}

func normalizePathspecs(root string, paths []string) ([]string, error) {
	if len(paths) == 0 {
		return []string{"."}, nil
	}
	result := make([]string, 0, len(paths))
	prefix, err := gitOutput("", nil, "rev-parse", "--show-prefix")
	if err != nil {
		return nil, err
	}
	for _, path := range paths {
		if !filepath.IsAbs(path) {
			relative := pathpkg.Clean(pathpkg.Join(prefix, filepath.ToSlash(path)))
			if relative == ".." || strings.HasPrefix(relative, "../") {
				return nil, fmt.Errorf("path %q is outside the repository", path)
			}
			result = append(result, relative)
			continue
		}
		absolute := path
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
		baseEntry, _ := gitOutput(root, literalPathEnv(), "ls-tree", "HEAD", "--", path)
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
		env = gitEnvironment()
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

// gitEnvironment prevents caller-controlled GIT_* variables from redirecting
// commands to a different repository, worktree, object store, config, or Git
// implementation. Handoff adds back only variables required by its operations.
func gitEnvironment(overrides ...string) []string {
	env := make([]string, 0, len(os.Environ())+len(overrides))
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(strings.ToUpper(name), "GIT_") {
			continue
		}
		env = append(env, entry)
	}
	return append(env, overrides...)
}
