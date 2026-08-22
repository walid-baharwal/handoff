package handoff

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
)

const integrationSchemaVersion = 2

type integrationEnvelope struct {
	SchemaVersion int               `json:"schema_version"`
	Command       string            `json:"command"`
	Data          any               `json:"data,omitempty"`
	Error         *integrationError `json:"error,omitempty"`
}

type integrationError struct {
	Code     string          `json:"code"`
	Message  string          `json:"message"`
	Recovery *recoveryStatus `json:"recovery,omitempty"`
}

type integrationHandoff struct {
	ID             string       `json:"id,omitempty"`
	Project        string       `json:"project,omitempty"`
	RepositoryID   string       `json:"repository_id,omitempty"`
	Branch         string       `json:"branch,omitempty"`
	Author         string       `json:"author,omitempty"`
	AuthorEmail    string       `json:"author_email,omitempty"`
	Message        string       `json:"message,omitempty"`
	BaseCommit     string       `json:"base_commit,omitempty"`
	Commit         string       `json:"commit,omitempty"`
	CreatedAt      string       `json:"created_at,omitempty"`
	StoredAt       string       `json:"stored_at,omitempty"`
	ExpiresAt      string       `json:"expires_at,omitempty"`
	FileCount      int          `json:"file_count"`
	Files          []string     `json:"files"`
	FilesTruncated bool         `json:"files_truncated"`
	Changes        []fileChange `json:"changes"`
	OwnerID        string       `json:"owner_id,omitempty"`
	Team           string       `json:"team,omitempty"`
	Recipients     []string     `json:"recipients"`
	Private        bool         `json:"private,omitempty"`
	AssignedTo     string       `json:"assigned_to,omitempty"`
	Lifecycle      string       `json:"lifecycle,omitempty"`
	AcknowledgedBy []string     `json:"acknowledged_by"`
	AppliedBy      []string     `json:"applied_by"`
	RevokedAt      string       `json:"revoked_at,omitempty"`
	CommentsCount  int          `json:"comments_count,omitempty"`
	ViewerRead     bool         `json:"viewer_read,omitempty"`
	ViewerArchived bool         `json:"viewer_archived,omitempty"`
	PackageBytes   int64        `json:"package_bytes"`
}

type recoveryStatus struct {
	Active          bool     `json:"active"`
	HandoffID       string   `json:"handoff_id,omitempty"`
	Stage           string   `json:"stage,omitempty"`
	ConflictedFiles []string `json:"conflicted_files"`
	CanContinue     bool     `json:"can_continue"`
	CanAbort        bool     `json:"can_abort"`
}

type compatibilityReport struct {
	Repository         repositoryInfo `json:"repository"`
	RepositoryMatch    bool           `json:"repository_match"`
	BranchMatch        bool           `json:"branch_match"`
	BaseAvailable      bool           `json:"base_available"`
	BaseAncestor       bool           `json:"base_ancestor"`
	CommitsFromBase    int            `json:"commits_from_base,omitempty"`
	LocalChanges       []fileChange   `json:"local_changes"`
	PotentialConflicts []string       `json:"potential_conflicts"`
	Warnings           []string       `json:"warnings"`
	Risk               string         `json:"risk"`
}

func inspectCompatibility(value handoffMetadata) (compatibilityReport, error) {
	root, err := repositoryRoot()
	if err != nil {
		return compatibilityReport{}, err
	}
	repository, err := inspectRepository(root)
	if err != nil {
		return compatibilityReport{}, err
	}
	local, err := workingChanges(root)
	if err != nil {
		return compatibilityReport{}, err
	}
	report := compatibilityReport{
		Repository:         repository,
		RepositoryMatch:    value.RepositoryID != "" && value.RepositoryID == repository.RepositoryID,
		BranchMatch:        value.Branch == "" || value.Branch == repository.Branch,
		LocalChanges:       nonNilChanges(local),
		PotentialConflicts: []string{},
		Warnings:           []string{},
		Risk:               "low",
	}
	if value.BaseCommit != "" {
		_, baseErr := gitOutput(root, nil, "cat-file", "-e", value.BaseCommit+"^{commit}")
		report.BaseAvailable = baseErr == nil
		if report.BaseAvailable {
			_, ancestorErr := gitOutput(root, nil, "merge-base", "--is-ancestor", value.BaseCommit, "HEAD")
			report.BaseAncestor = ancestorErr == nil
			if count, countErr := gitOutput(root, nil, "rev-list", "--count", value.BaseCommit+"..HEAD"); countErr == nil {
				fmt.Sscanf(count, "%d", &report.CommitsFromBase)
			}
		}
	}
	if !report.RepositoryMatch {
		report.Warnings = append(report.Warnings, "handoff repository does not match the local repository")
		report.Risk = "blocked"
	}
	if !report.BranchMatch {
		report.Warnings = append(report.Warnings, fmt.Sprintf("sender branch is %s; local branch is %s", orUnknown(value.Branch), repository.Branch))
		report.Risk = "medium"
	}
	if !report.BaseAvailable {
		report.Warnings = append(report.Warnings, "handoff base commit is not available locally; fetch before pulling")
		if report.Risk != "blocked" {
			report.Risk = "high"
		}
	}
	if report.BaseAvailable && !report.BaseAncestor {
		report.Warnings = append(report.Warnings, "handoff base is not an ancestor of the local branch")
		if report.Risk != "blocked" {
			report.Risk = "high"
		}
	} else if report.CommitsFromBase > 0 {
		report.Warnings = append(report.Warnings, fmt.Sprintf("local branch is %d commit(s) ahead of the handoff base", report.CommitsFromBase))
		if report.Risk == "low" {
			report.Risk = "medium"
		}
	}
	incoming := make(map[string]struct{})
	for _, change := range value.Changes {
		incoming[change.Path] = struct{}{}
		if change.OriginalPath != "" {
			incoming[change.OriginalPath] = struct{}{}
		}
	}
	if len(incoming) == 0 {
		for _, path := range value.Files {
			incoming[path] = struct{}{}
		}
	}
	for _, change := range local {
		if _, found := incoming[change.Path]; found {
			report.PotentialConflicts = append(report.PotentialConflicts, change.Path)
		}
		if _, found := incoming[change.OriginalPath]; change.OriginalPath != "" && found {
			report.PotentialConflicts = append(report.PotentialConflicts, change.OriginalPath)
		}
	}
	sort.Strings(report.PotentialConflicts)
	if len(report.PotentialConflicts) > 0 {
		report.Warnings = append(report.Warnings, "incoming paths overlap existing local changes")
		if report.Risk != "blocked" {
			report.Risk = "high"
		}
	} else if len(local) > 0 && report.Risk == "low" {
		report.Warnings = append(report.Warnings, "existing local changes will be preserved")
		report.Risk = "medium"
	}
	return report, nil
}

type codedError struct {
	code string
	err  error
}

func (err *codedError) Error() string { return err.err.Error() }
func (err *codedError) Unwrap() error { return err.err }

type reportedError struct{ err error }

func (err *reportedError) Error() string { return err.err.Error() }
func (err *reportedError) Unwrap() error { return err.err }

func commandError(code string, err error) error {
	if err == nil {
		return nil
	}
	var existing *codedError
	if errors.As(err, &existing) {
		return err
	}
	return &codedError{code: code, err: err}
}

func invalidArguments(message string) error {
	return commandError("invalid_arguments", errors.New(message))
}

func errorCode(err error) string {
	var coded *codedError
	if errors.As(err, &coded) {
		return coded.code
	}
	return "operation_failed"
}

// IsReportedError reports whether Run already wrote a machine-readable error.
func IsReportedError(err error) bool {
	var reported *reportedError
	return errors.As(err, &reported)
}

func writeIntegrationJSON(w io.Writer, command string, data any) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(integrationEnvelope{
		SchemaVersion: integrationSchemaVersion,
		Command:       command,
		Data:          data,
	})
}

func reportIntegrationError(w io.Writer, command string, err error) error {
	response := integrationError{Code: errorCode(err), Message: err.Error()}
	if recovery, statusErr := currentRecoveryStatus(); statusErr == nil && recovery.Active {
		response.Recovery = &recovery
	}
	if encodeErr := writeJSONValue(w, integrationEnvelope{
		SchemaVersion: integrationSchemaVersion,
		Command:       command,
		Error:         &response,
	}); encodeErr != nil {
		return encodeErr
	}
	return &reportedError{err: err}
}

func writeJSONValue(w io.Writer, value any) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func commandWantsJSON(command string, args []string) bool {
	switch command {
	case "push", "pull", "list", "inspect", "status", "changes", "comment", "comments", "audit", "whoami", "read", "unread", "archive", "unarchive", "acknowledge", "applied", "revoke", "assign", "expire":
	default:
		return false
	}
	for _, arg := range args {
		switch arg {
		case "--json", "-json", "--json=true", "-json=true":
			return true
		}
	}
	return false
}

func integrationHandoffFromMetadata(value handoffMetadata) integrationHandoff {
	return integrationHandoff{
		ID:             value.ID,
		Project:        value.Project,
		RepositoryID:   value.RepositoryID,
		Branch:         value.Branch,
		Author:         value.Author,
		AuthorEmail:    value.AuthorEmail,
		Message:        value.Message,
		BaseCommit:     value.BaseCommit,
		Commit:         value.Commit,
		CreatedAt:      integrationTime(value.CreatedAt),
		StoredAt:       integrationTime(value.StoredAt),
		ExpiresAt:      integrationTime(value.ExpiresAt),
		FileCount:      value.FileCount,
		Files:          nonNilStrings(value.Files),
		FilesTruncated: value.FilesTruncated,
		Changes:        nonNilChanges(value.Changes),
		OwnerID:        value.OwnerID,
		Team:           value.Team,
		Recipients:     nonNilStrings(value.Recipients),
		Private:        value.Private,
		AssignedTo:     value.AssignedTo,
		Lifecycle:      value.Lifecycle,
		AcknowledgedBy: nonNilStrings(value.AcknowledgedBy),
		AppliedBy:      nonNilStrings(value.AppliedBy),
		RevokedAt:      integrationTime(value.RevokedAt),
		CommentsCount:  value.CommentsCount,
		ViewerRead:     value.ViewerRead,
		ViewerArchived: value.ViewerArchived,
		PackageBytes:   value.PackageBytes,
	}
}

func integrationHandoffFromManifest(value manifest, id string, packageBytes int64) integrationHandoff {
	return integrationHandoff{
		ID:             id,
		Project:        value.Project,
		RepositoryID:   value.RepositoryID,
		Branch:         value.Branch,
		Author:         value.Author,
		AuthorEmail:    value.AuthorEmail,
		Message:        value.Message,
		BaseCommit:     value.BaseCommit,
		Commit:         value.Commit,
		CreatedAt:      integrationTime(value.CreatedAt),
		FileCount:      value.FileCount,
		Files:          nonNilStrings(value.Files),
		FilesTruncated: value.FilesTruncated,
		Changes:        nonNilChanges(value.Changes),
		Team:           value.Team,
		Recipients:     nonNilStrings(value.Recipients),
		Private:        value.Private,
		AcknowledgedBy: []string{},
		AppliedBy:      []string{},
		PackageBytes:   packageBytes,
	}
}

func integrationTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func currentRecoveryStatus() (recoveryStatus, error) {
	root, err := repositoryRoot()
	if err != nil {
		return recoveryStatus{}, err
	}
	stateFile := statePath(root)
	if _, err := os.Stat(stateFile); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return recoveryStatus{ConflictedFiles: []string{}}, nil
		}
		return recoveryStatus{}, err
	}
	state, err := loadState(stateFile)
	if err != nil {
		return recoveryStatus{}, commandError("recovery_state_invalid", err)
	}
	if err := validateStateObjects(root, state, false); err != nil {
		return recoveryStatus{}, err
	}
	conflicted, err := conflictedPaths(root)
	if err != nil {
		return recoveryStatus{}, err
	}
	canContinue := len(conflicted) == 0 && canContinuePhase(state.Phase)
	if canContinue && state.Phase == phaseIncoming {
		canContinue = validateStateObjects(root, state, true) == nil
	}
	return recoveryStatus{
		Active:          true,
		HandoffID:       state.ID,
		Stage:           integrationRecoveryStage(state.Phase),
		ConflictedFiles: conflicted,
		CanContinue:     canContinue,
		CanAbort:        true,
	}, nil
}

func conflictedPaths(root string) ([]string, error) {
	output, err := gitOutputRaw(root, nil, nil, "diff", "--name-only", "--diff-filter=U", "-z")
	if err != nil {
		return nil, fmt.Errorf("inspect conflicts: %w", err)
	}
	return nonNilStrings(splitNUL(output)), nil
}

func integrationRecoveryStage(phase string) string {
	switch phase {
	case phasePrepare:
		return "preparing"
	case phaseIncoming:
		return "incoming_changes"
	case phaseRestore:
		return "restoring_local_changes"
	case phaseStash:
		return "local_changes"
	case phaseFinalize:
		return "finalizing"
	default:
		return "unknown"
	}
}

func canContinuePhase(phase string) bool {
	return phase == phaseIncoming || phase == phaseStash || phase == phaseFinalize
}

func runStatus(args []string, stdout io.Writer) error {
	fs := newSilentFlagSet("status")
	jsonOutput := fs.Bool("json", false, "print machine-readable JSON")
	if err := fs.Parse(args); err != nil {
		return invalidArguments(err.Error())
	}
	if len(fs.Args()) != 0 {
		return invalidArguments("usage: handoff status [--json]")
	}
	status, err := currentRecoveryStatus()
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeIntegrationJSON(stdout, "status", struct {
			Recovery recoveryStatus `json:"recovery"`
		}{Recovery: status})
	}
	if !status.Active {
		fmt.Fprintln(stdout, "No Handoff recovery is active.")
		return nil
	}
	fmt.Fprintf(stdout, "Active handoff: %s\n", status.HandoffID)
	fmt.Fprintf(stdout, "Stage: %s\n", strings.ReplaceAll(status.Stage, "_", " "))
	if len(status.ConflictedFiles) > 0 {
		fmt.Fprintln(stdout, "Conflicted files:")
		for _, path := range status.ConflictedFiles {
			fmt.Fprintf(stdout, "  %s\n", printable(path))
		}
	}
	if status.CanContinue {
		fmt.Fprintf(stdout, "Continue: handoff continue %s\n", status.HandoffID)
	}
	fmt.Fprintf(stdout, "Abort: handoff abort %s\n", status.HandoffID)
	return nil
}
