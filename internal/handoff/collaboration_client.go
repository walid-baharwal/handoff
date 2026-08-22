package handoff

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

func postHandoffEvent(cfg clientConfig, id, action, target, expiresAt string) (handoffMetadata, error) {
	body, err := json.Marshal(struct {
		Action    string `json:"action"`
		Target    string `json:"target,omitempty"`
		ExpiresAt string `json:"expires_at,omitempty"`
	}{Action: action, Target: target, ExpiresAt: expiresAt})
	if err != nil {
		return handoffMetadata{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.Server+"/api/v1/handoffs/"+id+"/events", bytes.NewReader(body))
	if err != nil {
		return handoffMetadata{}, err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return handoffMetadata{}, commandError("server_unavailable", fmt.Errorf("update handoff failed: %w", err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return handoffMetadata{}, responseError("update handoff failed", resp)
	}
	var record handoffMetadata
	if err := json.NewDecoder(io.LimitReader(resp.Body, 256<<10)).Decode(&record); err != nil {
		return handoffMetadata{}, errors.New("server returned invalid handoff metadata")
	}
	return record, nil
}

func runHandoffEvent(command, action string, args []string, stdout io.Writer) error {
	fs := newSilentFlagSet(command)
	jsonOutput := fs.Bool("json", false, "print machine-readable JSON")
	target := fs.String("target", "", "assignment target")
	expiresAt := fs.String("expires-at", "", "new RFC3339 expiry")
	if err := fs.Parse(args); err != nil {
		return invalidArguments(err.Error())
	}
	if len(fs.Args()) != 1 || !idPattern.MatchString(strings.ToLower(fs.Args()[0])) {
		return invalidArguments(fmt.Sprintf("usage: handoff %s [--json] ID", command))
	}
	if action == "assign" && strings.TrimSpace(*target) == "" {
		return invalidArguments("--target is required")
	}
	if action == "expire" && strings.TrimSpace(*expiresAt) == "" {
		return invalidArguments("--expires-at is required")
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	id := strings.ToLower(fs.Args()[0])
	record, err := postHandoffEvent(cfg, id, action, strings.TrimSpace(*target), strings.TrimSpace(*expiresAt))
	if err != nil {
		return err
	}
	if *jsonOutput {
		return writeIntegrationJSON(stdout, command, struct {
			Handoff integrationHandoff `json:"handoff"`
		}{Handoff: integrationHandoffFromMetadata(record)})
	}
	fmt.Fprintf(stdout, "Handoff %s marked %s.\n", id, action)
	return nil
}

func runComment(args []string, stdout io.Writer) error {
	fs := newSilentFlagSet("comment")
	jsonOutput := fs.Bool("json", false, "print machine-readable JSON")
	if err := fs.Parse(args); err != nil {
		return invalidArguments(err.Error())
	}
	if len(fs.Args()) < 2 || !idPattern.MatchString(strings.ToLower(fs.Args()[0])) {
		return invalidArguments("usage: handoff comment [--json] ID MESSAGE")
	}
	message := strings.TrimSpace(strings.Join(fs.Args()[1:], " "))
	if message == "" || len(message) > 2000 {
		return invalidArguments("comment must contain 1 to 2000 characters")
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	body, _ := json.Marshal(struct {
		Message string `json:"message"`
	}{Message: message})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	id := strings.ToLower(fs.Args()[0])
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.Server+"/api/v1/handoffs/"+id+"/comments", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return commandError("server_unavailable", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return responseError("comment failed", resp)
	}
	var comment handoffComment
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&comment); err != nil {
		return errors.New("server returned an invalid comment")
	}
	if *jsonOutput {
		return writeIntegrationJSON(stdout, "comment", struct {
			Comment handoffComment `json:"comment"`
		}{Comment: comment})
	}
	fmt.Fprintf(stdout, "Comment added to Handoff %s.\n", id)
	return nil
}

func runComments(args []string, stdout io.Writer) error {
	return runCollaborationList("comments", args, stdout)
}

func runAudit(args []string, stdout io.Writer) error {
	return runCollaborationList("audit", args, stdout)
}

func runCollaborationList(command string, args []string, stdout io.Writer) error {
	fs := newSilentFlagSet(command)
	jsonOutput := fs.Bool("json", false, "print machine-readable JSON")
	if err := fs.Parse(args); err != nil {
		return invalidArguments(err.Error())
	}
	if len(fs.Args()) != 1 || !idPattern.MatchString(strings.ToLower(fs.Args()[0])) {
		return invalidArguments(fmt.Sprintf("usage: handoff %s [--json] ID", command))
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	id := strings.ToLower(fs.Args()[0])
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.Server+"/api/v1/handoffs/"+id+"/"+command, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return commandError("server_unavailable", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return responseError(command+" failed", resp)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxCollaborationBytes+1))
	if err != nil || len(data) > maxCollaborationBytes {
		return errors.New("server returned oversized collaboration data")
	}
	if *jsonOutput {
		var value any
		if err := json.Unmarshal(data, &value); err != nil {
			return errors.New("server returned invalid collaboration data")
		}
		return writeIntegrationJSON(stdout, command, value)
	}
	var output map[string][]json.RawMessage
	if err := json.Unmarshal(data, &output); err != nil {
		return errors.New("server returned invalid collaboration data")
	}
	fmt.Fprintf(stdout, "%s for Handoff %s: %d entries\n", strings.Title(command), id, len(output[command]))
	return nil
}

func runWhoami(args []string, stdout io.Writer) error {
	fs := newSilentFlagSet("whoami")
	jsonOutput := fs.Bool("json", false, "print machine-readable JSON")
	if err := fs.Parse(args); err != nil {
		return invalidArguments(err.Error())
	}
	if len(fs.Args()) != 0 {
		return invalidArguments("usage: handoff whoami [--json]")
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, cfg.Server+"/api/v1/me", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return commandError("server_unavailable", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return responseError("identity failed", resp)
	}
	var identity struct {
		ID     string   `json:"id"`
		Name   string   `json:"name"`
		Email  string   `json:"email"`
		Role   string   `json:"role"`
		Teams  []string `json:"teams"`
		Legacy bool     `json:"legacy"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&identity); err != nil {
		return errors.New("server returned an invalid identity")
	}
	if *jsonOutput {
		return writeIntegrationJSON(stdout, "whoami", struct {
			Identity any `json:"identity"`
		}{Identity: identity})
	}
	fmt.Fprintf(stdout, "%s (%s)\n", orUnknown(identity.Name), identity.ID)
	return nil
}
