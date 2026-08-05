# Contributing

1. Fork the repository.
2. Create a branch from `sandbox`.
3. Run `go test ./...` and `go vet ./...`.
4. Open a pull request to `walid-baharwal/handoff:sandbox`.

Changes under `npm/`, the release scripts, or the npm workflows must also pass
`npm test --prefix npm` using Node.js 24.

Linux, Windows, and macOS CI must pass, conversations must be resolved, and
`@walid-baharwal` must approve the pull request before it can be merged.

The `main` branch accepts pull requests only from this repository's `sandbox`
branch. Rebase contributor pull requests into `sandbox`. Merge `sandbox` into
`main` with **Create a merge commit** so the long-lived branches keep shared
history and future promotions do not repeat already-merged changes. Squash
merges are disabled.
