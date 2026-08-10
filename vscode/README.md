# Handoff for Visual Studio Code

Handoff lets a team share uncommitted Git changes without temporary commits,
ZIP files, or manually applying patches.

This extension includes the appropriate Handoff binary for its platform. It
does not require a global npm installation.

## Commands

- **Handoff: Configure Server** saves a Handoff server URL and team token.
- **Handoff: Open Inbox** lists handoffs for the open repository and lets you
  inspect or pull one.
- The **Handoff** Activity Bar view keeps the repository inbox one click away;
  use its refresh button to load the latest handoffs, then inspect or pull one.
- **Handoff: Push All Changes** previews every repository change and uploads
  them after confirmation.
- **Handoff: Push Selected Changes** previews changed paths, lets you select
  files, and uploads only that selection.
- **Handoff: Pull Handoff** lets you enter a handoff ID directly.
- **Handoff: Show Recovery Status** reports conflicts and provides Continue or
  Abort actions.

Push actions are also available from the Source Control view. Inbox entries
show complete metadata and changed paths before pull, can copy the Handoff ID,
and refresh automatically after Handoff operations. Active recovery is shown
at the top of the inbox.

The token is retained in VS Code Secret Storage. Handoff also saves its normal
client configuration so the same server works in the terminal.

Sender identity is taken from the sender's Git configuration and is
self-reported until Handoff supports per-user server tokens.

## Requirements

- VS Code 1.95 or newer.
- Git installed and available on your PATH.
- Access to a Handoff server and its shared team token.

## Development override

Set `handoff.binaryPath` to test the extension with a local Handoff binary.
This setting is intended for development; packaged releases use their bundled
binary by default.
