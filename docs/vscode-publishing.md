# VS Code extension publishing for repository owners

Handoff releases include five platform-specific VSIX packages. Each package
contains the Go Handoff binary for its target, so VS Code users do not need a
global npm installation.

The Release workflow builds the following targets from the same binaries used
for GitHub Releases and npm packages:

| VS Code target | Included Handoff binary |
| --- | --- |
| `linux-x64` | `handoff-linux-amd64` |
| `linux-arm64` | `handoff-linux-arm64` |
| `darwin-x64` | `handoff-darwin-amd64` |
| `darwin-arm64` | `handoff-darwin-arm64` |
| `win32-x64` | `handoff-windows-amd64.exe` |

## One-time Marketplace setup

1. Sign in to the [Visual Studio Marketplace publisher management page](https://marketplace.visualstudio.com/manage).
2. Create the publisher named `walid-baharwal`, or change `vscode/package.json`
   to the existing publisher name before the first release.
3. Create an Azure DevOps personal access token with the **Marketplace
   (Manage)** scope. Keep this token limited to the Marketplace account that
   owns the publisher.
4. In `walid-baharwal/handoff`, create the repository secret `VSCE_PAT` and
   store that token there.
5. Review the publisher profile and extension listing details before tagging a
   production release.

The workflow deliberately fails if `VSCE_PAT` is absent. This prevents a
version tag from publishing the GitHub and npm releases while silently skipping
the Marketplace package.

## How releases update VS Code

When a `vX.Y.Z` tag points to `main`, the Release workflow:

1. runs the Go, npm, and extension test suites;
2. builds all five Handoff binaries;
3. creates one platform-specific VSIX package for each binary;
4. publishes all VSIX files as the same Marketplace extension version; and
5. attaches all VSIX files to the GitHub Release with the standalone binaries.

The Marketplace selects the matching platform package for compatible VS Code
installations. A single source version is used across every platform.

## Local release-package check

From the repository root on Linux or macOS:

```bash
bash scripts/build-release-binaries.sh 0.0.0-ci dist
npm ci --prefix vscode
node vscode/scripts/package-vsix.mjs --version 0.0.0-ci --binaries dist --output vscode/dist
```

The resulting `.vsix` files are in `vscode/dist/`. Install one manually in VS
Code with **Extensions: Install from VSIX...** to perform an end-to-end smoke
test before the first Marketplace release.

## Normal release checklist

1. Merge the VS Code extension change into `sandbox`, then promote `sandbox`
   to `main` with a merge commit.
2. Confirm CI is green on `main`.
3. Confirm `VSCE_PAT` exists and the publisher name remains correct.
4. Tag a new version on `main` and push the tag.
5. Verify the GitHub Release includes all five `.vsix` files.
6. Verify the Marketplace listing offers the same version for each platform.
