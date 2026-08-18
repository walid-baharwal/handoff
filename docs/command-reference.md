# Handoff command reference

This guide explains what each `handoff` command does, where to run it, and
whether it changes local or server data.

## Before you start

- Run `handoff setup` once for each user account.
- Run `push`, inbox selection, pull application, `status`, `continue`, and
  `abort` inside the Git repository you want to work with. `list --all`,
  `inspect`, `delete`, and an ID-based pull preview can run from anywhere.
- A handoff ID is 12 hexadecimal characters, such as `abcdef123456`.
- Put flags before positional paths or IDs. Use `--` before a path that begins
  with a dash.
- Human-readable output is the default. See the
  [editor integration API](editor-integration-api.md) for the commands that
  support stable JSON output.

## Typical workflow

```bash
# Once per user
handoff setup --server https://handoff.example.com

# Sender
cd project
handoff push -m "invoice changes"

# Receiver, in another clone of the same repository
cd project
handoff list
handoff inspect abcdef123456
handoff pull abcdef123456
```

`push` uploads a package containing local Git changes. `pull` validates and
applies that package as local, uncommitted changes. It does not create a final
commit or push a Git branch.

## `handoff setup`

```bash
handoff setup --server URL [--token TOKEN | --token-stdin]
```

Connects the client to a Handoff server, verifies the team token, and saves the
server URL and token in the current user's configuration directory. HTTPS is
required except for `localhost` and `127.0.0.1`.

By default, Handoff reads `HANDOFF_TOKEN` or prompts for the token without
showing it. Automation should send one line through standard input:

```bash
printf '%s\n' "$HANDOFF_TOKEN" | handoff setup \
  --server https://handoff.example.com --token-stdin
```

`--token TOKEN` also exists, but it can expose the secret in process listings
and shell history and should be avoided.

## `handoff push`

```bash
handoff push [-m MESSAGE] [--to USER] [--team TEAM] [--private] [--dry-run] [--json] [--interactive] \
  [--staged | --worktree] [--exclude PATH] [PATH ...]
```

Collects matching changes, creates a validated Git bundle, and uploads it to
the configured server. With no paths or mode flags, it includes tracked,
untracked, staged, unstaged, and deleted files.

Common examples:

```bash
handoff push -m "all current changes"
handoff push -m "API only" backend/api.go backend/models/
handoff push --exclude generated/ --exclude notes.txt -m "clean copy"
handoff push --interactive -m "choose from a list"
handoff push --staged -m "index only"
handoff push --worktree -m "working tree only"
handoff push --dry-run -m "preview only"
handoff push --to saif@example.com --private -m "review this privately"
handoff push --team backend --private -m "backend team review"
```

Flags:

- `-m MESSAGE` adds the description shown to receivers.
- `--dry-run` builds and validates the package locally but does not upload it
  or require server configuration.
- `--interactive` shows changed paths and asks which ones to include. It cannot
  be combined with `--json`.
- `--staged` sends the versions currently in Git's index.
- `--worktree` selects unstaged and untracked worktree changes. For a partially
  staged file, it sends the complete current worktree version.
- `--exclude PATH` removes a literal file or directory from the selection and
  can be repeated.
- `--json` prints the versioned machine-readable response.
- `--to USER` adds a user ID or email recipient and can be repeated.
- `--team TEAM` targets a server-configured team or channel.
- `--private` restricts visibility to the owner, recipients, team members, and
  administrators. It requires `--to` or `--team`.

`--staged` and `--worktree` cannot be used together. Git LFS and submodule
changes are not supported in version 1.

## `handoff changes`

```bash
handoff changes [--json]
```

Lists the repository's final uncommitted state without changing Git staging.
JSON includes repository identity and rich added, modified, deleted, renamed,
conflicted, binary, staged/worktree, untracked, size, and support information.
Editor extensions should use this command instead of launching Git once per
file.

## `handoff list`

```bash
handoff list [--all] [--sent] [--archived] [--limit N] [--json]
```

Lists the newest handoffs for the current repository without changing files.
The default limit is 20; `N` must be between 1 and 100.

- `--all` lists handoffs from every repository visible to the shared team
  token.
- `--limit N` controls the maximum number returned.
- `--json` prints the versioned machine-readable response.
- `--sent` selects the authenticated user's Outbox.
- `--archived` includes items archived by the authenticated user.

## `handoff inspect`

```bash
handoff inspect [--json] ID
```

Shows sender, project, branch, message, timestamps, package size, and changed
paths without downloading or applying the package. Sender identity comes from
the sender's Git configuration on legacy shared-token servers. Per-user
servers bind identity to the authenticated token.

## `handoff pull`

```bash
handoff pull [--dry-run] [--yes] [--json] [ID]
```

Without an ID, lists the current repository's inbox and asks which handoff to
use. With an ID, loads that handoff directly. Handoff displays its metadata and
asks for confirmation before applying it.

Before changing the repository, Handoff validates the package, base commit,
changed paths, object sizes, local path collisions, and current Git state. It
backs up existing tracked local work, refuses paths that could overwrite local
untracked or ignored data, applies the incoming changes with Git's three-way
merge, restores the local work, and leaves the result uncommitted.

Examples:

```bash
handoff pull
handoff pull abcdef123456
handoff pull --dry-run abcdef123456
handoff pull --yes abcdef123456
handoff pull --dry-run --json abcdef123456
handoff pull --yes --json abcdef123456
```

- `--dry-run` shows metadata and changes nothing.
- `--yes` skips the confirmation prompt; use it only for a trusted, already
  reviewed handoff.
- `--json` requires an explicit ID. Applying in JSON mode also requires
  `--yes`, so automation cannot apply changes accidentally.

If conflicts occur, Handoff keeps recovery information and tells you to use
`status`, `continue`, or `abort`.

## `handoff status`

```bash
handoff status [--json]
```

Reports whether a pull recovery is active, its handoff ID and stage, conflicted
files, and whether it can be continued. It does not modify the repository.

## `handoff continue`

```bash
handoff continue ID
```

Continues an interrupted pull after conflicts have been resolved. Resolve each
file and stage the resolution first:

```bash
git add <resolved-files>
handoff continue abcdef123456
```

The ID must match the active recovery operation. When successful, the combined
changes remain local and uncommitted.

## `handoff abort`

```bash
handoff abort ID
```

Stops the active pull and restores the original `HEAD`, index, and backed-up
local changes. The ID must match the active recovery operation. Use this when
you do not want to finish resolving the handoff.

## `handoff delete`

```bash
handoff delete ID
```

Immediately deletes the package and its metadata from the server. This command
does not prompt and cannot be undone through Handoff. Per-user servers permit
this only for the owner or an administrator.

## Collaboration commands

Per-user servers support:

```bash
handoff whoami [--json]
handoff comment [--json] ID MESSAGE
handoff comments [--json] ID
handoff acknowledge [--json] ID
handoff applied [--json] ID
handoff assign --target USER [--json] ID
handoff read|unread|archive|unarchive [--json] ID
handoff revoke [--json] ID
handoff expire --expires-at RFC3339 [--json] ID
handoff audit [--json] ID
```

Read/archive state is private to each authenticated user. Revoke, assignment,
expiry, and deletion require ownership or an administrator. Revocation keeps
metadata and audit history but prevents future package downloads.

## `handoff serve`

```bash
handoff serve [--address ADDRESS]
```

Starts the self-hosted HTTP server. It requires either `HANDOFF_TOKEN` with at
least 32 characters or `HANDOFF_USERS`. The default listen address is `:8080`; production deployments
should put the service behind an HTTPS reverse proxy.

Server settings:

| Variable | Default | Meaning |
| --- | --- | --- |
| `HANDOFF_TOKEN` | required unless `HANDOFF_USERS` is set | Legacy shared administrator token |
| `HANDOFF_USERS` | empty | JSON array of per-user tokens, IDs, names, emails, roles, and teams |
| `HANDOFF_ADDRESS` | `:8080` | Listen address |
| `HANDOFF_DATA_DIR` | `/data` | Package and metadata storage |
| `HANDOFF_DOWNLOAD_DIR` | `/downloads` | Downloadable CLI binaries |
| `HANDOFF_MAX_BYTES` | `104857600` | Maximum uploaded package bytes |
| `HANDOFF_MAX_STORAGE_BYTES` | `10737418240` | Maximum stored package bytes |
| `HANDOFF_MAX_UPLOADS` | `4` | Maximum concurrent uploads |
| `HANDOFF_RETENTION` | `720h` | Package retention duration |

Use a dedicated, access-controlled `HANDOFF_DATA_DIR`. Handoff normally creates,
expires, and deletes files within that directory.

Example per-user configuration (tokens must contain at least 32 characters):

```json
[
  {"token":"...","id":"saif","name":"Saif","email":"saif@example.com","role":"member","teams":["backend"]},
  {"token":"...","id":"walid","name":"Walid","role":"admin","teams":["backend"]}
]
```

`HANDOFF_TOKEN` remains a legacy administrator/team token for backward
compatibility. Use per-user tokens when private recipients, ownership, roles,
read state, assignment, and audit attribution matter.

## `handoff version`

```bash
handoff version
handoff --version
handoff -v
```

Prints the installed Handoff version and changes nothing.

## `handoff help`

```bash
handoff help
handoff --help
handoff -h
```

Prints the short built-in usage summary. Use this reference for full command
behavior and examples.
