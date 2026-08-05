# npm publishing for repository owners

Handoff is published to the public npm registry as one launcher package and
five platform packages. npm installs only the native package matching the
user's operating system and CPU. The launcher then runs the same Go binary
that is available from GitHub Releases and the Handoff server.

## Packages

| Package | Purpose |
| --- | --- |
| `@walid-baharwal/handoff` | Public package and global `handoff` command |
| `@walid-baharwal/handoff-linux-x64` | Linux x86-64 binary |
| `@walid-baharwal/handoff-linux-arm64` | Linux ARM64 binary |
| `@walid-baharwal/handoff-darwin-x64` | macOS Intel binary |
| `@walid-baharwal/handoff-darwin-arm64` | macOS Apple Silicon binary |
| `@walid-baharwal/handoff-windows-x64` | Windows x86-64 binary |

All six packages always use the same version. Do not publish or change them
individually.

## How releases update npm

Ordinary pushes and pull requests never publish packages. A `vX.Y.Z` tag in
the upstream `walid-baharwal/handoff` repository starts `release.yml`, which:

1. Runs the Go and npm test suites.
2. Builds all five native binaries with `X.Y.Z` embedded as the version.
3. Creates and inspects all six npm package tarballs.
4. Runs the launcher against the real Linux binary.
5. Publishes the five native packages, then the launcher package.
6. Creates or updates the matching GitHub Release.

The workflow rejects tags whose commit is not already contained in upstream
`main`.

Stable versions such as `v1.2.0` receive npm's `latest` dist-tag. Prereleases
such as `v1.2.0-rc.1` receive `next`, so they do not replace the default stable
installation.

It does not matter whether a release contains changes written by the owner or
merged from a contributor's fork. Only a version tag pushed to the upstream
repository publishes npm packages. Existing global installations do not
self-update; users update with:

```bash
npm install --global @walid-baharwal/handoff@latest
```

## One-time owner setup

The proposed package names were unregistered when this integration was built.
Before merging, confirm that the repository owner controls the
`@walid-baharwal` scope on npm. An npm user automatically controls the matching
user scope; an organization scope must be created on npm first. If that scope
cannot be controlled, change every package name and dependency together before
the first publish.

The npm registry requires packages to exist before a trusted publisher can be
attached. The first release therefore needs a temporary bootstrap token.

1. Sign in to [npmjs.com](https://www.npmjs.com/) as the owner of the
   `@walid-baharwal` scope.
2. Enable two-factor authentication on the npm account.
3. Create a short-lived granular access token that can read and write packages
   in the scope and can bypass 2FA for automation.
4. In GitHub, open **Settings → Secrets and variables → Actions** for the
   upstream repository and create a repository secret named `NPM_TOKEN`.
5. Merge this work through `sandbox` into `main`.
6. Create a new release version. `v0.1.0` already exists, so use the next
   appropriate version, for example:

   ```bash
   git switch main
   git pull --ff-only upstream main
   git tag v0.2.0
   git push upstream v0.2.0
   ```

7. Wait for the **Release** workflow to publish all six public packages and
   the GitHub Release. The publishing script is retry-safe: rerunning the job
   skips any package version that already exists.

## Switch from the bootstrap token to trusted publishing

After the first successful release, configure the same trusted publisher on
each of the six npm packages. On npmjs.com, open each package's
**Settings → Trusted publishing**, choose **GitHub Actions**, and enter:

| Field | Value |
| --- | --- |
| Organization or user | `walid-baharwal` |
| Repository | `handoff` |
| Workflow filename | `release.yml` |
| Environment | Leave empty |
| Allowed action | `npm publish` |

The filename must match exactly. The workflow already runs on a GitHub-hosted
runner with `id-token: write`, Node.js 24, and a compatible npm CLI. npm will
use short-lived OIDC credentials and automatically attach provenance to public
packages from this public repository.

After all six trusted publishers are configured:

1. Delete the `NPM_TOKEN` GitHub Actions secret.
2. Revoke the temporary granular token on npmjs.com.
3. For each package, set publishing access to **Require two-factor
   authentication and disallow tokens**.
4. Keep tag creation restricted to trusted maintainers.

Future version tags now publish without a stored npm credential.

## Normal release checklist

1. Ensure the intended code is on upstream `main` and CI is green.
2. Choose a new SemVer version. npm versions are immutable and cannot be
   reused, even after unpublishing.
3. Create and push the matching `vX.Y.Z` tag.
4. Confirm the Release workflow passes.
5. Confirm all six npm packages and the GitHub Release show the same version.
6. Test a clean installation:

   ```bash
   npm install --global @walid-baharwal/handoff@latest
   handoff version
   ```

## Failed and partial releases

Rerun the failed Release workflow. The publisher checks npm before every
publish and skips versions that are already present, so a failure after two or
three platform packages does not require deleting them.

Never move or recreate a published version tag with different source. If a
published release is broken, fix the code and publish a new patch version. npm
recommends deprecating a bad version instead of unpublishing when consumers may
already depend on it:

```bash
npm deprecate @walid-baharwal/handoff@1.2.0 "Use 1.2.1; this release is broken."
```

## Security references

- [npm trusted publishing](https://docs.npmjs.com/trusted-publishers/)
- [npm provenance](https://docs.npmjs.com/generating-provenance-statements/)
- [Publishing scoped public packages](https://docs.npmjs.com/creating-and-publishing-scoped-public-packages/)
- [npm unpublish policy](https://docs.npmjs.com/policies/unpublish/)
