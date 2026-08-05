# Handoff

Share uncommitted Git changes with a teammate without temporary commits, ZIP
files, or pushing the changes to the main repository.

This package installs the official Handoff Go binary for the current operating
system and exposes it as the `handoff` command. It does not contain a separate
JavaScript implementation.

```bash
npm install --global @walid-baharwal/handoff
handoff setup --server https://handoff.example.com
handoff version
```

See the [Handoff repository](https://github.com/walid-baharwal/handoff) for
usage, server deployment, security details, and standalone binary downloads.
