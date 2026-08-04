package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

const packageVersion = 1

var objectIDPattern = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

type manifest struct {
	Version      int       `json:"version"`
	BaseCommit   string    `json:"base_commit"`
	Commit       string    `json:"commit"`
	Ref          string    `json:"ref"`
	CreatedAt    time.Time `json:"created_at"`
	Message      string    `json:"message,omitempty"`
	Author       string    `json:"author,omitempty"`
	BundleSHA256 string    `json:"bundle_sha256"`
}

func createPackage(path, bundlePath string, metadata manifest) error {
	bundle, err := os.Open(bundlePath)
	if err != nil {
		return err
	}
	defer bundle.Close()
	info, err := bundle.Stat()
	if err != nil {
		return err
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, bundle); err != nil {
		return err
	}
	metadata.Version = packageVersion
	metadata.BundleSHA256 = hex.EncodeToString(hash.Sum(nil))
	if _, err := bundle.Seek(0, io.SeekStart); err != nil {
		return err
	}
	manifestBytes, err := json.Marshal(metadata)
	if err != nil {
		return err
	}

	output, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	failed := true
	defer func() {
		output.Close()
		if failed {
			os.Remove(path)
		}
	}()
	gzipWriter := gzip.NewWriter(output)
	tarWriter := tar.NewWriter(gzipWriter)
	if err := writeTarEntry(tarWriter, "manifest.json", int64(len(manifestBytes)), time.Now(), func(w io.Writer) error {
		_, err := w.Write(manifestBytes)
		return err
	}); err != nil {
		return err
	}
	if err := writeTarEntry(tarWriter, "changes.bundle", info.Size(), info.ModTime(), func(w io.Writer) error {
		_, err := io.Copy(w, bundle)
		return err
	}); err != nil {
		return err
	}
	if err := tarWriter.Close(); err != nil {
		return err
	}
	if err := gzipWriter.Close(); err != nil {
		return err
	}
	if err := output.Sync(); err != nil {
		return err
	}
	if err := output.Close(); err != nil {
		return err
	}
	failed = false
	return nil
}

func writeTarEntry(writer *tar.Writer, name string, size int64, modified time.Time, write func(io.Writer) error) error {
	if err := writer.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: size, ModTime: modified.UTC()}); err != nil {
		return err
	}
	return write(writer)
}

func extractPackage(packagePath, destination string, maxBytes int64) (manifest, string, error) {
	input, err := os.Open(packagePath)
	if err != nil {
		return manifest{}, "", err
	}
	defer input.Close()
	gzipReader, err := gzip.NewReader(input)
	if err != nil {
		return manifest{}, "", errors.New("invalid handoff package")
	}
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)
	var metadata manifest
	var haveManifest, haveBundle bool
	var bundleHash string
	bundlePath := filepath.Join(destination, "changes.bundle")
	for {
		header, nextErr := tarReader.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return manifest{}, "", errors.New("invalid handoff archive")
		}
		if header.Typeflag != tar.TypeReg || header.Size < 0 || header.Size > maxBytes {
			return manifest{}, "", errors.New("invalid handoff archive entry")
		}
		switch header.Name {
		case "manifest.json":
			if haveManifest || header.Size > 64<<10 {
				return manifest{}, "", errors.New("invalid handoff manifest")
			}
			limited := io.LimitReader(tarReader, header.Size)
			if err := json.NewDecoder(limited).Decode(&metadata); err != nil {
				return manifest{}, "", errors.New("invalid handoff manifest")
			}
			haveManifest = true
		case "changes.bundle":
			if haveBundle {
				return manifest{}, "", errors.New("duplicate bundle")
			}
			output, err := os.OpenFile(bundlePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
			if err != nil {
				return manifest{}, "", err
			}
			hash := sha256.New()
			written, copyErr := io.Copy(io.MultiWriter(output, hash), io.LimitReader(tarReader, header.Size))
			closeErr := output.Close()
			if copyErr != nil || closeErr != nil || written != header.Size {
				return manifest{}, "", errors.New("incomplete handoff bundle")
			}
			bundleHash = hex.EncodeToString(hash.Sum(nil))
			haveBundle = true
		default:
			return manifest{}, "", fmt.Errorf("unexpected archive entry %q", header.Name)
		}
	}
	if !haveManifest || !haveBundle {
		return manifest{}, "", errors.New("handoff package is incomplete")
	}
	if err := validateManifest(metadata); err != nil {
		return manifest{}, "", err
	}
	if metadata.BundleSHA256 == "" {
		return manifest{}, "", errors.New("handoff manifest is missing bundle checksum")
	}
	if metadata.BundleSHA256 != bundleHash {
		return manifest{}, "", errors.New("handoff bundle checksum mismatch")
	}
	return metadata, bundlePath, nil
}

func validateManifest(value manifest) error {
	if value.Version != packageVersion {
		return fmt.Errorf("unsupported handoff package version %d", value.Version)
	}
	if !objectIDPattern.MatchString(value.BaseCommit) || !objectIDPattern.MatchString(value.Commit) {
		return errors.New("invalid Git object ID in handoff manifest")
	}
	if value.Ref == "" || len(value.Ref) > 200 || !regexp.MustCompile(`^refs/handoff/outgoing/[0-9a-f]+$`).MatchString(value.Ref) {
		return errors.New("invalid Git ref in handoff manifest")
	}
	if len(value.Message) > 500 || len(value.Author) > 200 {
		return errors.New("handoff metadata is too long")
	}
	return nil
}
