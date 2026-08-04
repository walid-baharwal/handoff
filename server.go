package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var idPattern = regexp.MustCompile(`^[0-9a-f]{12}$`)

type serviceConfig struct {
	Token       string
	DataDir     string
	DownloadDir string
	MaxBytes    int64
	Retention   time.Duration
	Logger      io.Writer
}

type service struct {
	token       string
	dataDir     string
	downloadDir string
	maxBytes    int64
	retention   time.Duration
	logger      *log.Logger
}

func newService(cfg serviceConfig) (*service, error) {
	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	if cfg.Logger == nil {
		cfg.Logger = io.Discard
	}
	s := &service{
		token:       cfg.Token,
		dataDir:     cfg.DataDir,
		downloadDir: cfg.DownloadDir,
		maxBytes:    cfg.MaxBytes,
		retention:   cfg.Retention,
		logger:      log.New(cfg.Logger, "handoff ", log.LstdFlags|log.LUTC),
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
	mux.Handle("POST /api/v1/handoffs", s.auth(http.HandlerFunc(s.handleUpload)))
	mux.Handle("GET /api/v1/handoffs/{id}", s.auth(http.HandlerFunc(s.handleDownload)))
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
		if !secureTokenEqual(s.token, r.Header.Get("Authorization")) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="handoff"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *service) handleHome(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "Handoff %s\n\nDownload a client from /downloads/ and run:\n  handoff setup --server %s://%s\n", version, forwardedScheme(r), r.Host)
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
	if closeErr != nil || written == 0 {
		http.Error(w, "empty or incomplete upload", http.StatusBadRequest)
		return
	}

	id, finalPath, err := s.availableID()
	if err != nil {
		http.Error(w, "cannot allocate handoff ID", http.StatusInternalServerError)
		return
	}
	if err := os.Rename(tmpName, finalPath); err != nil {
		http.Error(w, "cannot finalize upload", http.StatusInternalServerError)
		return
	}
	s.logger.Printf("uploaded id=%s bytes=%d", id, written)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]string{"id": id})
}

func (s *service) availableID() (string, string, error) {
	for range 20 {
		data := make([]byte, 6)
		if _, err := rand.Read(data); err != nil {
			return "", "", err
		}
		id := hex.EncodeToString(data)
		path := filepath.Join(s.dataDir, id+".handoff")
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			return id, path, nil
		}
	}
	return "", "", errors.New("ID collision limit reached")
}

func (s *service) handleDownload(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !idPattern.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(s.dataDir, id+".handoff")
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "cannot read handoff", http.StatusInternalServerError)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
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
	path := filepath.Join(s.downloadDir, name)
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, path)
}

func (s *service) cleanupLoop() {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for range ticker.C {
		s.cleanupExpired()
	}
}

func (s *service) cleanupExpired() {
	entries, err := os.ReadDir(s.dataDir)
	if err != nil {
		s.logger.Printf("cleanup error=%q", err)
		return
	}
	cutoff := time.Now().Add(-s.retention)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".handoff") {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.ModTime().Before(cutoff) {
			continue
		}
		if err := os.Remove(filepath.Join(s.dataDir, entry.Name())); err == nil {
			s.logger.Printf("expired id=%s", strings.TrimSuffix(entry.Name(), ".handoff"))
		}
	}
}
