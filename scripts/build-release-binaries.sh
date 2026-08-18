#!/usr/bin/env sh

set -eu

if [ "$#" -ne 2 ]; then
  echo "usage: build-release-binaries.sh VERSION OUTPUT_DIRECTORY" >&2
  exit 2
fi

version=$1
output_directory=$2

if [ -e "$output_directory" ]; then
  echo "output directory already exists: $output_directory" >&2
  exit 1
fi

mkdir -p "$output_directory"
ldflags="-s -w -X handoff/internal/handoff.Version=$version"

CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="$ldflags" -o "$output_directory/handoff-linux-amd64" ./cmd/handoff
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="$ldflags" -o "$output_directory/handoff-linux-arm64" ./cmd/handoff
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="$ldflags" -o "$output_directory/handoff-windows-amd64.exe" ./cmd/handoff
CGO_ENABLED=0 GOOS=windows GOARCH=arm64 go build -trimpath -ldflags="$ldflags" -o "$output_directory/handoff-windows-arm64.exe" ./cmd/handoff
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags="$ldflags" -o "$output_directory/handoff-darwin-amd64" ./cmd/handoff
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags="$ldflags" -o "$output_directory/handoff-darwin-arm64" ./cmd/handoff

(
  cd "$output_directory"
  sha256sum handoff-* > SHA256SUMS
)
