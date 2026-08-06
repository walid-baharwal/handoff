package handoff

import (
	"context"
	"crypto/subtle"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func runServe(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	address := fs.String("address", envOr("HANDOFF_ADDRESS", ":8080"), "listen address")
	if err := fs.Parse(args); err != nil {
		return err
	}
	tokenValue := strings.TrimSpace(os.Getenv("HANDOFF_TOKEN"))
	if len(tokenValue) < 32 {
		return errors.New("HANDOFF_TOKEN must contain at least 32 characters")
	}
	maxBytes, err := envInt64("HANDOFF_MAX_BYTES", defaultMaxBytes)
	if err != nil || maxBytes < 1 {
		return errors.New("HANDOFF_MAX_BYTES must be a positive integer")
	}
	maxStorageBytes, err := envInt64("HANDOFF_MAX_STORAGE_BYTES", defaultMaxStorageBytes)
	if err != nil || maxStorageBytes < 1 {
		return errors.New("HANDOFF_MAX_STORAGE_BYTES must be a positive integer")
	}
	maxUploads, err := envInt64("HANDOFF_MAX_UPLOADS", defaultMaxUploads)
	if err != nil || maxUploads < 1 || maxUploads > 100 {
		return errors.New("HANDOFF_MAX_UPLOADS must be between 1 and 100")
	}
	retention, err := time.ParseDuration(envOr("HANDOFF_RETENTION", "720h"))
	if err != nil || retention <= 0 {
		return errors.New("HANDOFF_RETENTION must be a positive duration")
	}

	service, err := newService(serviceConfig{
		Token:       tokenValue,
		DataDir:     envOr("HANDOFF_DATA_DIR", "/data"),
		DownloadDir: envOr("HANDOFF_DOWNLOAD_DIR", "/downloads"),
		MaxBytes:    maxBytes,
		MaxStorage:  maxStorageBytes,
		MaxUploads:  int(maxUploads),
		Retention:   retention,
		Logger:      stderr,
	})
	if err != nil {
		return err
	}
	server := &http.Server{
		Addr:              *address,
		Handler:           service.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       2 * time.Minute,
		WriteTimeout:      2 * time.Minute,
		IdleTimeout:       1 * time.Minute,
		MaxHeaderBytes:    1 << 20,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
	fmt.Fprintf(stdout, "Handoff %s listening on %s\n", Version, *address)
	err = server.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func envInt64(name string, fallback int64) (int64, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	return strconv.ParseInt(value, 10, 64)
}

func secureTokenEqual(expected, authorization string) bool {
	const prefix = "Bearer "
	if !strings.HasPrefix(authorization, prefix) {
		return false
	}
	provided := strings.TrimSpace(strings.TrimPrefix(authorization, prefix))
	if len(provided) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(provided)) == 1
}

func platformName() string {
	return runtime.GOOS + "-" + runtime.GOARCH
}
