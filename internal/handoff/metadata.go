package handoff

import (
	"crypto/sha256"
	"encoding/hex"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const maxListedFiles = 200

var repositoryIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

type handoffMetadata struct {
	ID             string    `json:"id"`
	Project        string    `json:"project,omitempty"`
	RepositoryID   string    `json:"repository_id,omitempty"`
	Branch         string    `json:"branch,omitempty"`
	Author         string    `json:"author,omitempty"`
	AuthorEmail    string    `json:"author_email,omitempty"`
	Message        string    `json:"message,omitempty"`
	BaseCommit     string    `json:"base_commit"`
	Commit         string    `json:"commit"`
	CreatedAt      time.Time `json:"created_at"`
	StoredAt       time.Time `json:"stored_at"`
	ExpiresAt      time.Time `json:"expires_at"`
	FileCount      int       `json:"file_count"`
	Files          []string  `json:"files,omitempty"`
	FilesTruncated bool      `json:"files_truncated,omitempty"`
	PackageBytes   int64     `json:"package_bytes"`
}

type handoffListResponse struct {
	Handoffs []handoffMetadata `json:"handoffs"`
}

func metadataFromManifest(id string, value manifest, storedAt, expiresAt time.Time, packageBytes int64) handoffMetadata {
	return handoffMetadata{
		ID:             id,
		Project:        value.Project,
		RepositoryID:   value.RepositoryID,
		Branch:         value.Branch,
		Author:         value.Author,
		AuthorEmail:    value.AuthorEmail,
		Message:        value.Message,
		BaseCommit:     value.BaseCommit,
		Commit:         value.Commit,
		CreatedAt:      value.CreatedAt,
		StoredAt:       storedAt,
		ExpiresAt:      expiresAt,
		FileCount:      value.FileCount,
		Files:          append([]string(nil), value.Files...),
		FilesTruncated: value.FilesTruncated,
		PackageBytes:   packageBytes,
	}
}

func repositoryDetails(root, baseCommit string) (repositoryID, project, branch string) {
	remote, _ := gitOutput(root, nil, "config", "--get", "remote.origin.url")
	canonical, project := canonicalRepository(remote)
	if canonical == "" {
		canonical = "git-base:" + baseCommit
		project = filepath.Base(root)
	}
	digest := sha256.Sum256([]byte(canonical))
	repositoryID = hex.EncodeToString(digest[:16])
	branch, _ = gitOutput(root, nil, "symbolic-ref", "--quiet", "--short", "HEAD")
	if branch == "" {
		branch = "detached"
	}
	return repositoryID, project, branch
}

func canonicalRepository(raw string) (canonical, project string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", ""
	}
	if !strings.Contains(raw, "://") && !filepath.IsAbs(raw) {
		if colon := strings.IndexByte(raw, ':'); colon > 0 && !strings.ContainsAny(raw[:colon], `/\\`) {
			host := raw[:colon]
			if at := strings.LastIndexByte(host, '@'); at >= 0 {
				host = host[at+1:]
			}
			path := cleanRepositoryPath(raw[colon+1:])
			return strings.ToLower(host) + "/" + path, repositoryProject(path)
		}
	}
	if parsed, err := url.Parse(raw); err == nil && parsed.Host != "" {
		path := cleanRepositoryPath(parsed.Path)
		return strings.ToLower(parsed.Host) + "/" + path, repositoryProject(path)
	}
	path := cleanRepositoryPath(filepath.ToSlash(filepath.Clean(raw)))
	return "local/" + path, repositoryProject(path)
}

func cleanRepositoryPath(value string) string {
	value = strings.Trim(strings.ReplaceAll(value, "\\", "/"), "/")
	value = strings.TrimSuffix(value, ".git")
	return value
}

func repositoryProject(value string) string {
	value = strings.TrimRight(value, "/")
	if slash := strings.LastIndexByte(value, '/'); slash >= 0 {
		return value[slash+1:]
	}
	return value
}
