# Handoff

Handoff transfers uncommitted Git changes between developers through a small self-hosted server. The server does not need repository access, GitHub credentials, or a copy of the codebase. The same binary works as the server and the client.

```text
Developer A                Handoff server                 Developer B
handoff push  ── package ──> stores package ── ID ──────> handoff pull ID
                                                              │
                                                      local Git changes
                                                      (no final commit)
```

## Features

- One command to upload changes and one command to apply them.
- Interactive, staged-only, worktree-only, exclusion, and dry-run push workflows.
- Repository-scoped team inbox with sender, branch, message, and file summary.
- Transfers tracked, untracked, deleted, staged, and unstaged files.
- Sends all changes or only selected paths.
- Uses Git's three-way merge and reports normal Git conflicts.
- Backs up existing receiver changes before applying a handoff.
- Leaves the result local and uncommitted.
- Single binary for Linux, Windows, and macOS.
- Filesystem storage: no database or repository integration.
- 100 MB package limit and 30-day retention by default.
- Receiver safety limits of 100 MB expanded changed-file content and 10,000 changed paths per handoff.

Git LFS files and submodule changes are not supported in version 1.

## Install

### npm

If Node.js 18 or newer is installed, npm can install the official CLI and the
correct native binary for the current platform:

```bash
npm install -g @walid-baharwal/handoff
handoff version
```

This is the same Go application as the standalone download, not a separate
JavaScript implementation. Upgrade or remove it with:

```bash
npm install -g @walid-baharwal/handoff@latest
npm uninstall -g @walid-baharwal/handoff
```

### Standalone binary

Download the binary for your system from the repository's **Releases** page:

| System | Binary |
| --- | --- |
| Linux x86-64 | `handoff-linux-amd64` |
| Linux ARM64 | `handoff-linux-arm64` |
| Windows x86-64 | `handoff-windows-amd64.exe` |
| macOS Intel | `handoff-darwin-amd64` |
| macOS Apple Silicon | `handoff-darwin-arm64` |

### Linux

Install the downloaded binary system-wide (replace the filename with
`handoff-linux-arm64` on ARM64):

```bash
sudo install -m 755 ~/Downloads/handoff-linux-amd64 /usr/local/bin/handoff
handoff version
```

### Windows

Run these commands in PowerShell:

```powershell
New-Item -ItemType Directory -Force "$HOME\bin" | Out-Null
Copy-Item "$HOME\Downloads\handoff-windows-amd64.exe" "$HOME\bin\handoff.exe"
[Environment]::SetEnvironmentVariable("Path", [Environment]::GetEnvironmentVariable("Path", "User") + ";$HOME\bin", "User")
$env:Path += ";$HOME\bin"
handoff version
```

### macOS

For Apple Silicon (`M1`, `M2`, `M3`, or newer):

```bash
sudo mkdir -p /usr/local/bin
sudo install -m 755 ~/Downloads/handoff-darwin-arm64 /usr/local/bin/handoff
handoff version
```

For an Intel Mac, use `handoff-darwin-amd64` instead. If macOS blocks the
unsigned binary, allow this specific download and try again:

```bash
sudo xattr -d com.apple.quarantine /usr/local/bin/handoff
handoff version
```

## Use

Configure the shared server once, then run the other commands inside a Git
repository:

```bash
handoff setup --server https://handoff.example.com
cd project
handoff push -m "backend for invoice task"
```

The sender receives a handoff ID. In another clone of the same repository,
inspect and apply it:

```bash
handoff list
handoff inspect abcdef123456
handoff pull abcdef123456
```

For path selection, previews, staged/worktree modes, inbox filters, conflict
recovery, server operation, JSON output, and every flag, read the
[Handoff command reference](docs/command-reference.md).

## Editor and IDE integrations

Handoff provides a versioned JSON command interface for lightweight editor
extensions. Inbox listing, inspection, push preview/upload, pull preview/apply,
and recovery status support `--json`. Automated setup can pass the team token
through standard input without exposing it in process arguments:

```bash
printf '%s\n' "$HANDOFF_TOKEN" | handoff setup --server https://handoff.example.com --token-stdin
handoff list --json
handoff status --json
```

See the [editor integration API](docs/editor-integration-api.md) for the exact
stdout, stderr, exit-code, response, and error contract.

### Visual Studio Code

The Handoff Visual Studio Code extension provides inbox, push, pull, setup, and
conflict-recovery commands from the Command Palette. It ships as a
platform-specific VSIX with the matching Go binary included, so users do not
need to install the npm package globally.

Install it from the Visual Studio Marketplace after the first extension release,
or use **Extensions: Install from VSIX...** with the matching asset attached to
the GitHub Release. Repository owners can follow the
[VS Code publishing guide](docs/vscode-publishing.md) to configure automated
Marketplace releases.

## Self-host with Docker Compose

Requirements:

- Docker with Compose.
- A public or private HTTPS URL that every developer can reach.
- A reverse proxy such as Coolify, Caddy, Traefik, or Nginx for TLS.

Clone the repository and generate a team token:

```bash
git clone https://github.com/OWNER/handoff.git
cd handoff
printf 'HANDOFF_TOKEN=' > .env
openssl rand -hex 32 >> .env
docker compose up -d --build
```

The service exposes port `8080` inside its Docker network. Connect the reverse proxy for `https://handoff.example.com` to the `handoff` service on that port. Verify it with:

```bash
curl https://handoff.example.com/healthz
```

The Compose volume `handoff_data` keeps uploaded packages across restarts.

### Coolify

1. Create a **Docker Compose** resource from this repository.
2. Add `HANDOFF_TOKEN` with a value generated by `openssl rand -hex 32`.
3. Deploy the stack.
4. Assign your HTTPS domain to the `handoff` service on port `8080`.
5. Open `/healthz`, then test a push and pull between two temporary clones.

## Server configuration

| Variable | Default | Purpose |
| --- | --- | --- |
| `HANDOFF_TOKEN` | required | Shared bearer token, minimum 32 characters |
| `HANDOFF_ADDRESS` | `:8080` | Server listen address |
| `HANDOFF_DATA_DIR` | `/data` | Package storage directory |
| `HANDOFF_DOWNLOAD_DIR` | `/downloads` | Client binary download directory |
| `HANDOFF_MAX_BYTES` | `104857600` | Maximum package size |
| `HANDOFF_MAX_STORAGE_BYTES` | `10737418240` | Maximum total stored package bytes |
| `HANDOFF_MAX_UPLOADS` | `4` | Maximum concurrent package uploads |
| `HANDOFF_RETENTION` | `720h` | Package retention period |

Packages are protected in transit by HTTPS and access-controlled by the shared token. They are not encrypted on disk; anyone with server filesystem access can read them.

Use a dedicated, access-controlled directory for `HANDOFF_DATA_DIR`; do not point it at a repository, home directory, temporary directory shared with other users, or another application's data. Handoff creates, expires, and deletes files within this directory as part of normal operation.

## Project structure

```text
cmd/handoff/       executable entrypoint
internal/handoff/  client, server, Git transfer, packaging, and domain tests
```

## Build and test

With Go 1.24 and Git installed:

```bash
go test ./...
go build -o handoff ./cmd/handoff
```

Or use Docker:

```bash
docker run --rm -v "$PWD:/src" -w /src golang:1.24 go test ./...
docker build -t handoff .
```

## Contributing

Fork the repository and open pull requests against `sandbox`, not `main`.
See [CONTRIBUTING.md](CONTRIBUTING.md) for the complete workflow.

## Releases

CI runs tests on every push and pull request. Pushing a version tag builds all
supported binaries, creates checksums, publishes the matching npm packages,
and publishes a GitHub Release:

```bash
git tag v0.2.0
git push origin v0.2.0
```

Repository maintainers must complete the one-time npm setup before the first
npm-enabled release. See [npm publishing for repository owners](docs/npm-publishing.md).

## License

Handoff is available under the [MIT License](LICENSE).
