package handoff

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var idPattern = regexp.MustCompile(`^[0-9a-f]{12}$`)

const maxServerMetadataBytes = 128 << 10

var homeTemplate = template.Must(template.New("home").Parse(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <meta name="color-scheme" content="dark">
  <title>Handoff · Share work in progress</title>
  <style>
    :root { color-scheme: dark; font-family: Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; }
    * { box-sizing: border-box; }
    body { margin: 0; min-height: 100vh; color: #e8edf5; background: #0b0f14; }
    a { color: inherit; }
    .shell { width: min(920px, calc(100% - 32px)); margin: 0 auto; padding: 56px 0 32px; }
    header { display: flex; align-items: center; justify-content: space-between; gap: 20px; margin-bottom: 56px; }
    .brand { display: flex; align-items: center; gap: 11px; font-weight: 750; letter-spacing: -.02em; }
    .mark { display: grid; place-items: center; width: 34px; height: 34px; border-radius: 10px; color: #07110d; background: #67e8a5; font: 800 18px/1 ui-monospace, monospace; }
    .version { color: #7f8b9d; font: 500 12px/1 ui-monospace, monospace; }
    .status { display: flex; align-items: center; gap: 8px; color: #9aa7b8; font-size: 13px; }
    .status::before { content: ""; width: 8px; height: 8px; border-radius: 50%; background: #67e8a5; box-shadow: 0 0 0 4px rgba(103,232,165,.1); }
    .hero { max-width: 720px; }
    h1 { margin: 0; max-width: 680px; font-size: clamp(40px, 7vw, 68px); line-height: 1.02; letter-spacing: -.055em; }
    .lead { margin: 24px 0 0; max-width: 630px; color: #9aa7b8; font-size: 18px; line-height: 1.65; }
    .panel { margin-top: 48px; padding: 26px; border: 1px solid #222a35; border-radius: 18px; background: #111720; box-shadow: 0 24px 70px rgba(0,0,0,.28); }
    .panel h2 { margin: 0 0 20px; font-size: 15px; letter-spacing: .01em; }
	.step { display: grid; grid-template-columns: 30px 1fr; gap: 14px; padding: 20px 0; border-top: 1px solid #222a35; }
	.step > div { min-width: 0; }
	.step:first-of-type { border-top: 0; padding-top: 0; }
    .step:last-child { padding-bottom: 0; }
    .number { display: grid; place-items: center; width: 26px; height: 26px; border: 1px solid #334052; border-radius: 50%; color: #aeb9c8; font: 600 12px/1 ui-monospace, monospace; }
    .step-title { margin: 2px 0 10px; font-size: 14px; font-weight: 650; }
    code { font-family: ui-monospace, SFMono-Regular, Consolas, "Liberation Mono", monospace; }
    .command { display: block; overflow-x: auto; padding: 13px 15px; border: 1px solid #263142; border-radius: 10px; color: #d9e4f1; background: #0a0e13; font-size: 13px; line-height: 1.55; white-space: nowrap; }
    .prompt { color: #67e8a5; user-select: none; }
    .downloads { display: flex; flex-wrap: wrap; gap: 8px; }
    .download { padding: 9px 12px; border: 1px solid #334052; border-radius: 9px; color: #cbd5e1; text-decoration: none; font-size: 12px; transition: border-color .15s, background .15s; }
    .download:hover { border-color: #67e8a5; background: rgba(103,232,165,.06); }
    footer { display: flex; flex-wrap: wrap; justify-content: space-between; gap: 16px; margin-top: 24px; color: #727f91; font-size: 12px; }
    footer a { text-underline-offset: 3px; }
    @media (max-width: 600px) {
      .shell { padding-top: 28px; }
      header { margin-bottom: 44px; }
      h1 { font-size: 42px; }
		.lead { font-size: 16px; }
		.panel { margin-top: 36px; padding: 20px; }
		.command { white-space: normal; overflow-wrap: anywhere; }
		.status span { display: none; }
	}
  </style>
</head>
<body>
  <main class="shell">
    <header>
      <div class="brand"><span class="mark">H</span><span>Handoff</span><span class="version">{{.Version}}</span></div>
      <div class="status"><span>Server ready</span></div>
    </header>

    <section class="hero">
      <h1>Share work in progress.</h1>
      <p class="lead">Send uncommitted Git changes to a teammate without temporary commits, ZIP files, or touching the main repository.</p>
    </section>

    <section class="panel" aria-labelledby="quick-start">
      <h2 id="quick-start">Quick start</h2>
      <div class="step">
        <span class="number">1</span>
        <div>
          <p class="step-title">Download the CLI for your machine</p>
          <div class="downloads">
            <a class="download" href="/downloads/handoff-linux-amd64">Linux x64</a>
            <a class="download" href="/downloads/handoff-linux-arm64">Linux ARM64</a>
            <a class="download" href="/downloads/handoff-darwin-arm64">macOS Apple Silicon</a>
            <a class="download" href="/downloads/handoff-darwin-amd64">macOS Intel</a>
            <a class="download" href="/downloads/handoff-windows-amd64.exe">Windows x64</a>
            <a class="download" href="/downloads/handoff-windows-arm64.exe">Windows ARM64</a>
          </div>
        </div>
      </div>
      <div class="step">
        <span class="number">2</span>
        <div>
          <p class="step-title">Connect this Handoff server</p>
          <code class="command"><span class="prompt">$</span> handoff setup --server {{.ServerURL}}</code>
        </div>
      </div>
      <div class="step">
        <span class="number">3</span>
        <div>
          <p class="step-title">Push your changes, then find team handoffs</p>
          <code class="command"><span class="prompt">$</span> handoff push -m "invoice changes"<br><span class="prompt">$</span> handoff inbox</code>
        </div>
      </div>
    </section>

    <footer>
      <span>Private by default. Built for developers.</span>
      <span><a href="https://github.com/walid-baharwal/handoff">GitHub</a> · <a href="/downloads/SHA256SUMS">Checksums</a> · <a href="/healthz">Health</a></span>
    </footer>
  </main>
</body>
</html>`))

type homeTemplateData struct {
	Version   string
	ServerURL string
}

type serviceConfig struct {
	Token       string
	Users       []configuredUser
	DataDir     string
	DownloadDir string
	MaxBytes    int64
	MaxStorage  int64
	MaxUploads  int
	Retention   time.Duration
	Logger      io.Writer
}

type service struct {
	token       string
	users       []configuredUser
	dataDir     string
	downloadDir string
	maxBytes    int64
	maxStorage  int64
	retention   time.Duration
	logger      *log.Logger
	uploadSlots chan struct{}
	quotaMu     sync.Mutex
}

type authenticatedUser struct {
	ID     string
	Name   string
	Email  string
	Role   string
	Teams  []string
	Legacy bool
}

type authenticationContextKey struct{}

func requestUser(r *http.Request) authenticatedUser {
	value, _ := r.Context().Value(authenticationContextKey{}).(authenticatedUser)
	return value
}

func (user authenticatedUser) admin() bool { return user.Role == "admin" }

func (user authenticatedUser) inTeam(team string) bool {
	for _, value := range user.Teams {
		if value == team {
			return true
		}
	}
	return false
}

func containsIdentity(values []string, user authenticatedUser) bool {
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == user.ID || (user.Email != "" && value == user.Email) {
			return true
		}
	}
	return false
}

func canView(record handoffMetadata, user authenticatedUser) bool {
	if user.admin() || user.Legacy || record.OwnerID == user.ID || containsIdentity(record.Recipients, user) {
		return true
	}
	if record.Team != "" && user.inTeam(record.Team) {
		return true
	}
	return !record.Private
}

func newService(cfg serviceConfig) (*service, error) {
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	if cfg.Logger == nil {
		cfg.Logger = io.Discard
	}
	if cfg.MaxStorage <= 0 {
		cfg.MaxStorage = defaultMaxStorageBytes
	}
	if cfg.MaxUploads <= 0 {
		cfg.MaxUploads = defaultMaxUploads
	}
	s := &service{
		token:       cfg.Token,
		users:       append([]configuredUser(nil), cfg.Users...),
		dataDir:     cfg.DataDir,
		downloadDir: cfg.DownloadDir,
		maxBytes:    cfg.MaxBytes,
		maxStorage:  cfg.MaxStorage,
		retention:   cfg.Retention,
		logger:      log.New(cfg.Logger, "handoff ", log.LstdFlags|log.LUTC),
		uploadSlots: make(chan struct{}, cfg.MaxUploads),
	}
	s.cleanupExpired()
	go s.cleanupLoop()
	return s, nil
}

func (s *service) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok\n")
	})
	mux.HandleFunc("GET /", s.handleHome)
	mux.HandleFunc("GET /downloads/{name}", s.handleDownloadBinary)
	mux.Handle("GET /api/v1/auth", s.auth(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})))
	mux.Handle("GET /api/v1/me", s.auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := requestUser(r)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(struct {
			ID     string   `json:"id"`
			Name   string   `json:"name"`
			Email  string   `json:"email"`
			Role   string   `json:"role"`
			Teams  []string `json:"teams"`
			Legacy bool     `json:"legacy"`
		}{ID: user.ID, Name: user.Name, Email: user.Email, Role: user.Role, Teams: nonNilStrings(user.Teams), Legacy: user.Legacy})
	})))
	mux.Handle("GET /api/v1/handoffs", s.auth(http.HandlerFunc(s.handleList)))
	mux.Handle("POST /api/v1/handoffs", s.auth(http.HandlerFunc(s.handleUpload)))
	mux.Handle("GET /api/v1/handoffs/{id}/metadata", s.auth(http.HandlerFunc(s.handleMetadata)))
	mux.Handle("GET /api/v1/handoffs/{id}", s.auth(http.HandlerFunc(s.handleDownload)))
	mux.Handle("DELETE /api/v1/handoffs/{id}", s.auth(http.HandlerFunc(s.handleDelete)))
	mux.Handle("GET /api/v1/handoffs/{id}/comments", s.auth(http.HandlerFunc(s.handleComments)))
	mux.Handle("POST /api/v1/handoffs/{id}/comments", s.auth(http.HandlerFunc(s.handleComments)))
	mux.Handle("POST /api/v1/handoffs/{id}/events", s.auth(http.HandlerFunc(s.handleEvent)))
	mux.Handle("GET /api/v1/handoffs/{id}/audit", s.auth(http.HandlerFunc(s.handleAudit)))
	return s.securityHeaders(mux)
}

func (s *service) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func (s *service) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization := r.Header.Get("Authorization")
		if len(s.token) >= 32 && secureTokenEqual(s.token, authorization) {
			user := authenticatedUser{ID: "legacy-team", Name: "Handoff Team", Role: "admin", Legacy: true}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), authenticationContextKey{}, user)))
			return
		}
		var matched *configuredUser
		for index := range s.users {
			if secureTokenEqual(s.users[index].Token, authorization) {
				matched = &s.users[index]
			}
		}
		if matched == nil {
			w.Header().Set("WWW-Authenticate", `Bearer realm="handoff"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		user := authenticatedUser{ID: matched.ID, Name: matched.Name, Email: matched.Email, Role: matched.Role, Teams: matched.Teams}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), authenticationContextKey{}, user)))
	})
}

func (s *service) handleHome(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	w.Header().Set("Cache-Control", "no-cache")
	if err := homeTemplate.Execute(w, homeTemplateData{
		Version:   Version,
		ServerURL: forwardedScheme(r) + "://" + r.Host,
	}); err != nil {
		s.logger.Printf("render homepage error=%q", err)
	}
}

func forwardedScheme(r *http.Request) string {
	if value := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-Proto"), ",")[0]); value != "" {
		return value
	}
	if r.TLS != nil {
		return "https"
	}
	return "http"
}

func (s *service) handleUpload(w http.ResponseWriter, r *http.Request) {
	select {
	case s.uploadSlots <- struct{}{}:
		defer func() { <-s.uploadSlots }()
	default:
		http.Error(w, "too many uploads in progress", http.StatusServiceUnavailable)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.maxBytes)
	tmp, err := os.CreateTemp(s.dataDir, ".upload-*")
	if err != nil {
		http.Error(w, "cannot store upload", http.StatusInternalServerError)
		return
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		http.Error(w, "cannot secure upload", http.StatusInternalServerError)
		return
	}
	written, copyErr := io.Copy(tmp, r.Body)
	syncErr := tmp.Sync()
	closeErr := tmp.Close()
	if copyErr != nil {
		var maxErr *http.MaxBytesError
		if errors.As(copyErr, &maxErr) {
			http.Error(w, "handoff package is too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "cannot read upload", http.StatusBadRequest)
		return
	}
	if syncErr != nil || closeErr != nil {
		http.Error(w, "cannot persist upload", http.StatusInternalServerError)
		return
	}
	if written == 0 {
		http.Error(w, "empty or incomplete upload", http.StatusBadRequest)
		return
	}
	verifyDir, err := os.MkdirTemp("", "handoff-upload-verify-*")
	if err != nil {
		http.Error(w, "cannot validate upload", http.StatusInternalServerError)
		return
	}
	defer os.RemoveAll(verifyDir)
	packageManifest, _, err := extractPackage(tmpName, verifyDir, s.maxBytes)
	if err != nil {
		http.Error(w, "invalid handoff package", http.StatusBadRequest)
		return
	}

	s.quotaMu.Lock()
	defer s.quotaMu.Unlock()
	storedBytes, err := s.storedPackageBytes()
	if err != nil {
		http.Error(w, "cannot inspect storage usage", http.StatusInternalServerError)
		return
	}
	if written > s.maxStorage-storedBytes {
		http.Error(w, "handoff storage quota exceeded", http.StatusInsufficientStorage)
		return
	}
	id, finalPath, final, err := s.reserveID()
	if err != nil {
		http.Error(w, "cannot allocate handoff ID", http.StatusInternalServerError)
		return
	}
	completed := false
	defer func() {
		_ = final.Close()
		if !completed {
			_ = os.Remove(finalPath)
			_ = os.Remove(s.metadataPath(id))
			s.removeCollaborationFiles(id)
			_ = syncDirectory(s.dataDir)
		}
	}()
	source, err := os.Open(tmpName)
	if err != nil {
		http.Error(w, "cannot finalize upload", http.StatusInternalServerError)
		return
	}
	copied, copyErr := io.Copy(final, source)
	sourceErr := source.Close()
	syncErr = final.Sync()
	closeErr = final.Close()
	if copyErr != nil || sourceErr != nil || syncErr != nil || closeErr != nil || copied != written {
		http.Error(w, "cannot finalize upload", http.StatusInternalServerError)
		return
	}
	storedAt := time.Now().UTC()
	record := metadataFromManifest(id, packageManifest, storedAt, storedAt.Add(s.retention), written)
	user := requestUser(r)
	record.OwnerID = user.ID
	if !user.Legacy {
		if user.Name != "" {
			record.Author = user.Name
		}
		if user.Email != "" {
			record.AuthorEmail = user.Email
		}
	}
	if err := writeJSONExclusive(s.metadataPath(id), record, 0o600); err != nil {
		http.Error(w, "cannot index upload", http.StatusInternalServerError)
		return
	}
	if err := s.appendAuditLocked(id, auditEvent{Action: "created", ActorID: user.ID, Actor: actorName(user), CreatedAt: storedAt}); err != nil {
		http.Error(w, "cannot create audit history", http.StatusInternalServerError)
		return
	}
	completed = true
	s.logger.Printf("uploaded id=%s bytes=%d", id, written)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(record)
}

func (s *service) storedPackageBytes() (int64, error) {
	entries, err := os.ReadDir(s.dataDir)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, entry := range entries {
		id := strings.TrimSuffix(entry.Name(), ".handoff")
		if !idPattern.MatchString(id) || entry.Name() != id+".handoff" || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return 0, err
		}
		if info.Mode().IsRegular() {
			total += info.Size()
		}
	}
	return total, nil
}

func (s *service) reserveID() (string, string, *os.File, error) {
	for range 20 {
		data := make([]byte, 6)
		if _, err := rand.Read(data); err != nil {
			return "", "", nil, err
		}
		id := hex.EncodeToString(data)
		finalPath := filepath.Join(s.dataDir, id+".handoff")
		if _, err := os.Lstat(s.metadataPath(id)); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", "", nil, err
		}
		file, err := os.OpenFile(finalPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", "", nil, err
		}
		return id, finalPath, file, nil
	}
	return "", "", nil, errors.New("ID collision limit reached")
}

func (s *service) metadataPath(id string) string {
	return filepath.Join(s.dataDir, id+".json")
}

func (s *service) handleList(w http.ResponseWriter, r *http.Request) {
	repositoryID := strings.TrimSpace(r.URL.Query().Get("repository_id"))
	if repositoryID != "" && !repositoryIDPattern.MatchString(repositoryID) {
		http.Error(w, "invalid repository ID", http.StatusBadRequest)
		return
	}
	limit := 20
	offset := 0
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			http.Error(w, "limit must be between 1 and 100", http.StatusBadRequest)
			return
		}
		limit = parsed
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("offset")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 || parsed > 10000 {
			http.Error(w, "offset must be between 0 and 10000", http.StatusBadRequest)
			return
		}
		offset = parsed
	}
	entries, err := os.ReadDir(s.dataDir)
	if err != nil {
		http.Error(w, "cannot list handoffs", http.StatusInternalServerError)
		return
	}
	now := time.Now()
	user := requestUser(r)
	items := make([]handoffMetadata, 0)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		id := strings.TrimSuffix(name, ".json")
		if !idPattern.MatchString(id) {
			continue
		}
		record, err := s.loadMetadata(id)
		if err != nil || (!record.ExpiresAt.IsZero() && !record.ExpiresAt.After(now)) || !canView(record, user) {
			continue
		}
		if !s.regularDataFileExists(id + ".handoff") {
			continue
		}
		if repositoryID != "" && record.RepositoryID != repositoryID {
			continue
		}
		if owner := strings.TrimSpace(r.URL.Query().Get("owner")); owner == "me" && record.OwnerID != user.ID {
			continue
		}
		var states handoffViewerStates
		_ = readOptionalJSON(s.statesPath(id), &states)
		record.ViewerRead = states[user.ID].Read
		record.ViewerArchived = states[user.ID].Archived
		if record.ViewerArchived && r.URL.Query().Get("archived") != "true" {
			continue
		}
		record.Files = nil
		record.Changes = nil
		items = append(items, record)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].StoredAt.Equal(items[j].StoredAt) {
			return items[i].ID > items[j].ID
		}
		return items[i].StoredAt.After(items[j].StoredAt)
	})
	if offset > len(items) {
		offset = len(items)
	}
	items = items[offset:]
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(handoffListResponse{Handoffs: items, NextOffset: offset + len(items), HasMore: hasMore})
}

func (s *service) handleMetadata(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !idPattern.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	record, err := s.loadMetadata(id)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "cannot read handoff metadata", http.StatusInternalServerError)
		return
	}
	if (!record.ExpiresAt.IsZero() && !record.ExpiresAt.After(time.Now())) || !s.regularDataFileExists(id+".handoff") || !canView(record, requestUser(r)) {
		http.NotFound(w, r)
		return
	}
	var states handoffViewerStates
	_ = readOptionalJSON(s.statesPath(id), &states)
	record.ViewerRead = states[requestUser(r).ID].Read
	record.ViewerArchived = states[requestUser(r).ID].Archived
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, no-store")
	_ = json.NewEncoder(w).Encode(record)
}

func (s *service) regularDataFileExists(name string) bool {
	file, err := s.openDataFile(name)
	if err != nil {
		return false
	}
	defer file.Close()
	info, err := file.Stat()
	return err == nil && info.Mode().IsRegular()
}

func (s *service) loadMetadata(id string) (handoffMetadata, error) {
	file, err := s.openDataFile(id + ".json")
	if err != nil {
		return handoffMetadata{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxServerMetadataBytes+1))
	if err != nil {
		return handoffMetadata{}, err
	}
	if len(data) > maxServerMetadataBytes {
		return handoffMetadata{}, errors.New("handoff metadata exceeds size limit")
	}
	var record handoffMetadata
	if err := json.Unmarshal(data, &record); err != nil {
		return handoffMetadata{}, err
	}
	if record.ID != id {
		return handoffMetadata{}, errors.New("handoff metadata ID mismatch")
	}
	return record, nil
}

func (s *service) handleDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !idPattern.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	s.quotaMu.Lock()
	defer s.quotaMu.Unlock()
	record, err := s.loadMetadata(id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	user := requestUser(r)
	if !user.admin() && record.OwnerID != user.ID {
		http.Error(w, "only the owner or an administrator can delete a handoff", http.StatusForbidden)
		return
	}
	packagePath := filepath.Join(s.dataDir, id+".handoff")
	if err := os.Remove(packagePath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "cannot delete handoff", http.StatusInternalServerError)
		return
	}
	_ = os.Remove(s.metadataPath(id))
	_ = os.Remove(s.commentsPath(id))
	_ = os.Remove(s.auditPath(id))
	_ = os.Remove(s.statesPath(id))
	s.logger.Printf("deleted id=%s", id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *service) handleDownload(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !idPattern.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	record, err := s.loadMetadata(id)
	if err != nil || (!record.ExpiresAt.IsZero() && !record.ExpiresAt.After(time.Now())) || !record.RevokedAt.IsZero() || !canView(record, requestUser(r)) {
		http.NotFound(w, r)
		return
	}
	file, err := s.openDataFile(id + ".handoff")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.Error(w, "cannot inspect handoff", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/vnd.handoff.package")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": id + ".handoff"}))
	w.Header().Set("Content-Length", fmt.Sprint(info.Size()))
	w.Header().Set("Cache-Control", "private, no-store")
	_, _ = io.Copy(w, file)
	s.logger.Printf("downloaded id=%s bytes=%d", id, info.Size())
}

func (s *service) openDataFile(name string) (*os.File, error) {
	root, err := os.OpenRoot(s.dataDir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return root.Open(name)
}

func (s *service) handleDownloadBinary(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if name == "" || filepath.Base(name) != name || strings.HasPrefix(name, ".") {
		http.NotFound(w, r)
		return
	}
	allowed := strings.HasPrefix(name, "handoff-") || name == "SHA256SUMS"
	if !allowed {
		http.NotFound(w, r)
		return
	}
	root, err := os.OpenRoot(s.downloadDir)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer root.Close()
	file, err := root.Open(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	http.ServeContent(w, r, name, info.ModTime(), file)
}

func (s *service) cleanupLoop() {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for range ticker.C {
		s.cleanupExpired()
	}
}

func (s *service) cleanupExpired() {
	s.quotaMu.Lock()
	defer s.quotaMu.Unlock()
	entries, err := os.ReadDir(s.dataDir)
	if err != nil {
		s.logger.Printf("cleanup error=%q", err)
		return
	}
	cutoff := time.Now().Add(-s.retention)
	temporaryCutoff := time.Now().Add(-10 * time.Minute)
	for _, entry := range entries {
		if isServiceTemporary(entry.Name()) && !entry.IsDir() && entry.Type()&os.ModeSymlink == 0 {
			if info, err := entry.Info(); err == nil && info.ModTime().Before(temporaryCutoff) {
				_ = os.Remove(filepath.Join(s.dataDir, entry.Name()))
			}
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".handoff")
		if entry.IsDir() || !idPattern.MatchString(id) || entry.Name() != id+".handoff" {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(temporaryCutoff) {
			if _, err := s.loadMetadata(id); err != nil {
				_ = os.Remove(filepath.Join(s.dataDir, entry.Name()))
				_ = os.Remove(s.metadataPath(id))
				s.removeCollaborationFiles(id)
				continue
			}
		}
		if !info.ModTime().Before(cutoff) {
			continue
		}
		if err := os.Remove(filepath.Join(s.dataDir, entry.Name())); err == nil {
			_ = os.Remove(s.metadataPath(id))
			s.removeCollaborationFiles(id)
			s.logger.Printf("expired id=%s", id)
		}
	}
}

func (s *service) removeCollaborationFiles(id string) {
	_ = os.Remove(s.commentsPath(id))
	_ = os.Remove(s.auditPath(id))
	_ = os.Remove(s.statesPath(id))
}

func isServiceTemporary(name string) bool {
	if strings.HasPrefix(name, ".upload-") || strings.HasPrefix(name, ".tmp-") {
		return true
	}
	id := strings.TrimSuffix(strings.TrimPrefix(name, "."), ".reserve")
	return name == "."+id+".reserve" && idPattern.MatchString(id)
}
