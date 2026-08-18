# Changelog

## Unreleased

- Added a native Handoff Source Control provider for every discovered Git repository.
- Added persistent per-repository drafts with include/remove actions, message input, audiences, diffs, file status, safety warnings, and exact previews.
- Rebuilt the Inbox for multiple repositories with safe target matching, search, filters, sorting, badges, background notifications, and deep links.
- Added pull compatibility previews, conflict recovery actions, cancellation, timeouts, diagnostics, onboarding, server profiles, and remote-workspace support.
- Added optional recipients, teams, private Handoffs, assignment, comments, acknowledgement/applied state, outbox, read/archive state, expiry, revoke, and audit history.
- Added a native Activity Bar inbox with refresh, inspect, and pull actions.
- Added selected-file pushes and Source Control view actions.
- Added full handoff inspection, copy-ID actions, automatic refresh, and a
  visible recovery state.
- Fixed VSIX packaging from Windows checkout paths containing spaces.

## 0.2.2 - 2026-08-06

- Hardened package validation, repository recovery, and server storage safety.
- Added explicit confirmation before applying handoffs.
- Clarified that the npm CLI package should be installed globally with `npm install -g`.

## 0.2.1 - 2026-08-05

- Initial Handoff VS Code extension.
