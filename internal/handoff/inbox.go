package handoff

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"
	"unicode"
)

func runList(args []string, stdout io.Writer) error {
	fs := newSilentFlagSet("list")
	all := fs.Bool("all", false, "list handoffs from every repository")
	sent := fs.Bool("sent", false, "list handoffs created by the authenticated user")
	archived := fs.Bool("archived", false, "include handoffs archived by the authenticated user")
	jsonOutput := fs.Bool("json", false, "print machine-readable JSON")
	limit := fs.Int("limit", 20, "maximum number of handoffs")
	offset := fs.Int("offset", 0, "number of newer handoffs to skip")
	if err := fs.Parse(args); err != nil {
		return invalidArguments(err.Error())
	}
	if len(fs.Args()) != 0 || *limit < 1 || *limit > 100 || *offset < 0 || *offset > 10000 {
		return invalidArguments("usage: handoff list [--all] [--sent] [--archived] [--json] [--limit N] [--offset N]")
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	repositoryID, project := "", "all projects"
	if !*all {
		repositoryID, project, err = currentRepositoryIdentity()
		if err != nil {
			return err
		}
	}
	page, err := listHandoffPage(cfg, repositoryID, *limit, *offset, *sent, *archived)
	if err != nil {
		return err
	}
	items := page.Handoffs
	if *jsonOutput {
		handoffs := make([]integrationHandoff, 0, len(items))
		for _, item := range items {
			handoffs = append(handoffs, integrationHandoffFromMetadata(item))
		}
		scope := "repository"
		if *all {
			scope = "all"
		}
		responseProject := project
		if *all {
			responseProject = ""
		}
		return writeIntegrationJSON(stdout, "list", struct {
			Scope        string               `json:"scope"`
			Project      string               `json:"project,omitempty"`
			RepositoryID string               `json:"repository_id,omitempty"`
			Limit        int                  `json:"limit"`
			Offset       int                  `json:"offset"`
			NextOffset   int                  `json:"next_offset"`
			HasMore      bool                 `json:"has_more"`
			Handoffs     []integrationHandoff `json:"handoffs"`
		}{
			Scope:        scope,
			Project:      responseProject,
			RepositoryID: repositoryID,
			Limit:        *limit,
			Offset:       *offset,
			NextOffset:   page.NextOffset,
			HasMore:      page.HasMore,
			Handoffs:     handoffs,
		})
	}
	if len(items) == 0 {
		fmt.Fprintf(stdout, "No handoffs are available for %s.\n", printable(project))
		return nil
	}
	printHandoffList(stdout, items, project)
	return nil
}

func runInspect(args []string, stdout io.Writer) error {
	fs := newSilentFlagSet("inspect")
	jsonOutput := fs.Bool("json", false, "print machine-readable JSON")
	if err := fs.Parse(args); err != nil {
		return invalidArguments(err.Error())
	}
	if len(fs.Args()) != 1 || !idPattern.MatchString(strings.ToLower(fs.Args()[0])) {
		return invalidArguments("usage: handoff inspect [--json] ID")
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	record, err := getHandoffMetadata(cfg, strings.ToLower(fs.Args()[0]))
	if err != nil {
		return err
	}
	if updated, updateErr := postHandoffEvent(cfg, record.ID, "read", "", ""); updateErr == nil {
		record = updated
	}
	if *jsonOutput {
		return writeIntegrationJSON(stdout, "inspect", struct {
			Handoff integrationHandoff `json:"handoff"`
		}{Handoff: integrationHandoffFromMetadata(record)})
	}
	printHandoffMetadata(stdout, record)
	return nil
}

func runDelete(args []string, stdout io.Writer) error {
	if len(args) != 1 || !idPattern.MatchString(strings.ToLower(args[0])) {
		return errors.New("usage: handoff delete ID")
	}
	id := strings.ToLower(args[0])
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, cfg.Server+"/api/v1/handoffs/"+id, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return commandError("server_unavailable", fmt.Errorf("delete failed: %w", err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return responseError("delete failed", resp)
	}
	fmt.Fprintf(stdout, "Handoff %s deleted.\n", id)
	return nil
}

func currentRepositoryIdentity() (repositoryID, project string, err error) {
	root, err := repositoryRoot()
	if err != nil {
		return "", "", err
	}
	base, err := gitOutput(root, nil, "rev-parse", "HEAD")
	if err != nil {
		return "", "", commandError("repository_has_no_commits", errors.New("the repository needs at least one commit"))
	}
	repositoryID, project, _ = repositoryDetails(root, base)
	return repositoryID, project, nil
}

func listHandoffs(cfg clientConfig, repositoryID string, limit int) ([]handoffMetadata, error) {
	return listHandoffsWithOptions(cfg, repositoryID, limit, false, false)
}

func listHandoffsWithOptions(cfg clientConfig, repositoryID string, limit int, sent, archived bool) ([]handoffMetadata, error) {
	page, err := listHandoffPage(cfg, repositoryID, limit, 0, sent, archived)
	return page.Handoffs, err
}

func listHandoffPage(cfg clientConfig, repositoryID string, limit, offset int, sent, archived bool) (handoffListResponse, error) {
	endpoint, err := url.Parse(cfg.Server + "/api/v1/handoffs")
	if err != nil {
		return handoffListResponse{}, err
	}
	query := endpoint.Query()
	if repositoryID != "" {
		query.Set("repository_id", repositoryID)
	}
	query.Set("limit", strconv.Itoa(limit))
	query.Set("offset", strconv.Itoa(offset))
	if sent {
		query.Set("owner", "me")
	}
	if archived {
		query.Set("archived", "true")
	}
	endpoint.RawQuery = query.Encode()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := authenticatedRead(ctx, cfg, endpoint.String())
	if err != nil {
		return handoffListResponse{}, commandError("server_unavailable", fmt.Errorf("list failed: %w", err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return handoffListResponse{}, responseError("list failed", resp)
	}
	var result handoffListResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&result); err != nil {
		return handoffListResponse{}, errors.New("server returned an invalid handoff list")
	}
	for _, record := range result.Handoffs {
		if !idPattern.MatchString(record.ID) {
			return handoffListResponse{}, errors.New("server returned an invalid handoff list")
		}
	}
	return result, nil
}

func getHandoffMetadata(cfg clientConfig, id string) (handoffMetadata, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resp, err := authenticatedRead(ctx, cfg, cfg.Server+"/api/v1/handoffs/"+id+"/metadata")
	if err != nil {
		return handoffMetadata{}, commandError("server_unavailable", fmt.Errorf("inspect failed: %w", err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return handoffMetadata{}, responseError("inspect failed", resp)
	}
	var record handoffMetadata
	if err := json.NewDecoder(io.LimitReader(resp.Body, 256<<10)).Decode(&record); err != nil || record.ID != id {
		return handoffMetadata{}, errors.New("server returned invalid handoff metadata")
	}
	return record, nil
}

func printHandoffList(stdout io.Writer, items []handoffMetadata, project string) {
	fmt.Fprintf(stdout, "RECENT HANDOFFS FOR %s\n\n", printable(project))
	table := tabwriter.NewWriter(stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(table, "#\tID\tFROM\tBRANCH\tMESSAGE\tAGE\tFILES")
	for index, item := range items {
		fmt.Fprintf(table, "%d\t%s\t%s\t%s\t%s\t%s\t%d\n",
			index+1,
			item.ID,
			shorten(printable(orUnknown(item.Author)), 20),
			shorten(printable(orUnknown(item.Branch)), 24),
			shorten(printable(orUnknown(item.Message)), 38),
			formatAge(item.StoredAt),
			item.FileCount,
		)
	}
	_ = table.Flush()
}

func printHandoffMetadata(stdout io.Writer, item handoffMetadata) {
	fmt.Fprintf(stdout, "Handoff: %s\n", item.ID)
	fmt.Fprintf(stdout, "From: %s (self-reported)\n", printable(orUnknown(item.Author)))
	if item.AuthorEmail != "" {
		fmt.Fprintf(stdout, "Email: %s\n", printable(item.AuthorEmail))
	}
	fmt.Fprintf(stdout, "Project: %s\n", printable(orUnknown(item.Project)))
	fmt.Fprintf(stdout, "Branch: %s\n", printable(orUnknown(item.Branch)))
	fmt.Fprintf(stdout, "Message: %s\n", printable(orUnknown(item.Message)))
	if item.Team != "" {
		fmt.Fprintf(stdout, "Team: %s\n", printable(item.Team))
	}
	if len(item.Recipients) > 0 {
		fmt.Fprintf(stdout, "Recipients: %s\n", printable(strings.Join(item.Recipients, ", ")))
	}
	if item.AssignedTo != "" {
		fmt.Fprintf(stdout, "Assigned to: %s\n", printable(item.AssignedTo))
	}
	if item.Lifecycle != "" {
		fmt.Fprintf(stdout, "Status: %s\n", printable(item.Lifecycle))
	}
	if !item.CreatedAt.IsZero() {
		fmt.Fprintf(stdout, "Created: %s (%s)\n", item.CreatedAt.Local().Format(time.RFC1123), formatAge(item.CreatedAt))
	}
	if !item.ExpiresAt.IsZero() {
		fmt.Fprintf(stdout, "Expires: %s\n", item.ExpiresAt.Local().Format(time.RFC1123))
	}
	fmt.Fprintf(stdout, "Files: %d\n", item.FileCount)
	if item.PackageBytes > 0 {
		fmt.Fprintf(stdout, "Package: %s\n", formatBytes(item.PackageBytes))
	}
	if item.BaseCommit != "" {
		fmt.Fprintf(stdout, "Base: %s\n", shortID(item.BaseCommit))
	}
	if len(item.Files) > 0 {
		fmt.Fprintln(stdout, "Changed paths:")
		for _, path := range item.Files {
			fmt.Fprintf(stdout, "  %s\n", printable(path))
		}
		if item.FilesTruncated {
			fmt.Fprintf(stdout, "  ... and %d more\n", item.FileCount-len(item.Files))
		}
	}
}

func printable(value string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, value)
}

func shorten(value string, max int) string {
	runes := []rune(value)
	if len(runes) <= max {
		return value
	}
	if max <= 1 {
		return string(runes[:max])
	}
	return string(runes[:max-1]) + "…"
}

func orUnknown(value string) string {
	if strings.TrimSpace(value) == "" {
		return "unknown"
	}
	return value
}

func formatAge(value time.Time) string {
	if value.IsZero() {
		return "unknown"
	}
	duration := time.Since(value)
	if duration < 0 {
		duration = 0
	}
	switch {
	case duration < time.Minute:
		return "now"
	case duration < time.Hour:
		return fmt.Sprintf("%dm", int(duration.Minutes()))
	case duration < 24*time.Hour:
		return fmt.Sprintf("%dh", int(duration.Hours()))
	default:
		return fmt.Sprintf("%dd", int(duration.Hours()/24))
	}
}

func formatBytes(value int64) string {
	const (
		kib = int64(1 << 10)
		mib = int64(1 << 20)
	)
	switch {
	case value >= mib:
		return fmt.Sprintf("%.1f MiB", float64(value)/float64(mib))
	case value >= kib:
		return fmt.Sprintf("%.1f KiB", float64(value)/float64(kib))
	default:
		return fmt.Sprintf("%d B", value)
	}
}
