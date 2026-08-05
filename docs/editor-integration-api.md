# Editor integration API

Handoff exposes a versioned, machine-readable CLI contract for editor and IDE
integrations. Human-readable output remains the default; integrations opt in by
passing `--json`.

## Process contract

- A successful command exits with code `0` and writes one JSON object to
  standard output.
- A failed command exits with a nonzero code and writes one JSON error object to
  standard error. It does not prefix the error with `handoff:` or write a
  partial JSON result to standard output.
- `schema_version` is currently `1`. A breaking JSON contract change requires a
  new schema version.
- Timestamps are UTC RFC 3339 strings. Collections such as `files` and
  `conflicted_files` are empty arrays rather than `null`.
- Display text and JSON are intentionally separate contracts. Integrations
  should never parse the default human-readable output.

Every result uses this envelope:

```json
{
  "schema_version": 1,
  "command": "inspect",
  "data": {}
}
```

Every failure uses this envelope:

```json
{
  "schema_version": 1,
  "command": "pull",
  "error": {
    "code": "not_found",
    "message": "inspect failed: handoff not found"
  }
}
```

The message is suitable for display. Branch on `error.code`, not the message.
Current codes include:

| Code | Meaning |
| --- | --- |
| `invalid_arguments` | The command or flags are invalid. |
| `not_configured` | Client setup has not been completed. |
| `invalid_config` | Saved client configuration cannot be used. |
| `repository_required` | The working directory is not inside a Git repository. |
| `repository_has_no_commits` | The repository does not have an initial commit yet. |
| `repository_conflict` | The repository already has unresolved Git conflicts. |
| `git_operation_active` | Another merge, cherry-pick, revert, or rebase is active. |
| `no_changes` | No changes matched the requested push selection. |
| `authentication_failed` | The server rejected the configured token. |
| `server_unavailable` | The server could not be reached. |
| `not_found` | The requested handoff does not exist. |
| `server_conflict` | The server rejected the operation because of its current state. |
| `package_too_large` | The package exceeded the configured limit. |
| `server_error` | The server returned another unsuccessful response. |
| `conflict` | Git conflicts require local recovery. |
| `recovery_active` | A previous Handoff operation must be continued or aborted. |
| `no_active_recovery` | Continue or abort was requested with no active recovery. |
| `active_handoff_mismatch` | The supplied ID does not match active recovery. |
| `conflicts_unresolved` | Recovery still has unstaged conflict resolutions. |
| `recovery_state_invalid` | Local Handoff recovery state is unreadable. |
| `operation_failed` | Another local operation failed. |

Callers must tolerate additional object fields and new error codes in future
compatible releases.

## Commands

### Inbox

```bash
handoff list --json
handoff list --all --limit 50 --json
```

`data.scope` is `repository` or `all`. Repository-scoped responses also contain
`project` and `repository_id`. `data.limit` echoes the requested maximum and
`data.handoffs` contains metadata records ordered the same way as the normal
inbox.

```json
{
  "schema_version": 1,
  "command": "list",
  "data": {
    "scope": "repository",
    "project": "handoff",
    "repository_id": "0123456789abcdef0123456789abcdef",
    "limit": 20,
    "handoffs": []
  }
}
```

### Inspect

```bash
handoff inspect --json abcdef123456
```

The result is in `data.handoff`. Sender name and email originate from the
sender's Git configuration and are self-reported.

### Preview and push

```bash
handoff push --dry-run --json -m "invoice changes" backend/
handoff push --json -m "invoice changes" backend/
```

The result includes `data.status` (`previewed` or `uploaded`), `data.dry_run`,
`data.mode` (`all`, `staged`, or `worktree`), and `data.handoff`. Uploaded
results include the new handoff ID. `--interactive` cannot be combined with
`--json`; the editor should provide its own file picker and pass selected paths
as positional arguments.

### Preview and pull

```bash
handoff pull --dry-run --json abcdef123456
handoff pull --json abcdef123456
```

JSON mode requires an explicit ID and never prompts. The result contains
`data.status`, `data.dry_run`, `data.applied`, `data.handoff`, and, after an
apply, `data.recovery`.

If Git reports conflicts, the process exits nonzero with code `conflict`. The
error includes current recovery state:

```json
{
  "schema_version": 1,
  "command": "pull",
  "error": {
    "code": "conflict",
    "message": "conflict while applying restored local work; resolve files, run 'git add', then 'handoff continue abcdef123456' (or 'handoff abort abcdef123456')",
    "recovery": {
      "active": true,
      "handoff_id": "abcdef123456",
      "stage": "local_changes",
      "conflicted_files": ["app.go"],
      "can_continue": false,
      "can_abort": true
    }
  }
}
```

### Recovery status

```bash
handoff status --json
```

`data.recovery.active` is false when no Handoff operation is active. During
recovery, `stage` is one of `preparing`, `incoming_changes`,
`restoring_local_changes`, `local_changes`, `finalizing`, or `unknown`.
`can_continue` becomes true after all conflict files have been resolved and
staged. `can_abort` indicates that `handoff abort ID` is available.

### Secure setup

An integration should send the token through standard input instead of putting
it in process arguments:

```bash
printf '%s\n' "$HANDOFF_TOKEN" | handoff setup --server https://handoff.example.com --token-stdin
```

`--token-stdin` reads one line without printing a prompt. It takes precedence
over `HANDOFF_TOKEN` and cannot be combined with the legacy `--token` flag.
Do not log the spawned process's standard input.
