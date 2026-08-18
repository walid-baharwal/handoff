"use strict";

const path = require("node:path");
const vscode = require("vscode");
const { HandoffCommandError, resolveBinary, runHandoff } = require("./lib/cli");
const {
  handoffDescription,
  handoffFromCommand,
  handoffInspection,
  handoffLabel,
  handoffTooltip
} = require("./lib/inbox");
const { groupHandoffs, matchingRepositories, repositoryKey } = require("./lib/repositories");
const {
  buildPushArguments,
  changeFingerprint,
  excludedByPattern,
  groupChanges,
  selectedPaths,
  sensitiveFileReason,
  validateMessage
} = require("./lib/workflow");

const draftsKey = "handoff.drafts.v2";
const inboxStateKey = "handoff.inboxState.v1";
const lastRepositoryKey = "handoff.lastRepository.v1";
const profileStateKey = "handoff.profiles.v1";
const profileBindingsKey = "handoff.profileBindings.v1";
const statusIcons = Object.freeze({
  added: "diff-added",
  modified: "diff-modified",
  deleted: "diff-removed",
  renamed: "diff-renamed",
  conflicted: "warning",
  untracked: "question"
});

let activeExtension;

function formatBytes(value) {
  if (!value) return "0 B";
  if (value >= 1 << 20) return `${(value / (1 << 20)).toFixed(1)} MiB`;
  if (value >= 1 << 10) return `${(value / (1 << 10)).toFixed(1)} KiB`;
  return `${value} B`;
}

function relativeTime(value, now = Date.now()) {
  const timestamp = Date.parse(value || "");
  if (!Number.isFinite(timestamp)) return "unknown time";
  const seconds = Math.max(0, Math.floor((now - timestamp) / 1000));
  if (seconds < 60) return "now";
  if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`;
  if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ago`;
  return `${Math.floor(seconds / 86400)}d ago`;
}

function repositoryLabel(repository) {
  return repository.info?.project || path.basename(repository.rootUri.fsPath);
}

function normalizeResources(values) {
  return values.flatMap((value) => Array.isArray(value) ? value : [value]).filter((value) => value?.change);
}

class DraftStore {
  constructor(context) {
    this.context = context;
    this.values = context.workspaceState.get(draftsKey, {});
  }

  get(root) {
    return this.values[root.toLowerCase()] || { included: [], message: "", recipients: [], team: "", private: false };
  }

  async set(root, value) {
    this.values[root.toLowerCase()] = value;
    await this.context.workspaceState.update(draftsKey, this.values);
  }

  async remove(root) {
    delete this.values[root.toLowerCase()];
    await this.context.workspaceState.update(draftsKey, this.values);
  }
}

class BaseContentProvider {
  constructor(manager) {
    this.manager = manager;
  }

  async provideTextDocumentContent(uri) {
    const query = JSON.parse(uri.query || "{}");
    const repository = this.manager.byRoot(query.root);
    if (!repository) return "The Git repository is no longer open.";
    try {
      const value = await repository.git.show(query.ref || "HEAD", query.path);
      return Buffer.from(value).toString("utf8");
    } catch {
      return "";
    }
  }
}

class EmptyContentProvider {
  provideTextDocumentContent() { return ""; }
}

class HandoffRepository {
  constructor(extension, git) {
    this.extension = extension;
    this.git = git;
    this.rootUri = git.rootUri;
    this.changes = [];
    this.info = undefined;
    this.refreshTimer = undefined;
    this.refreshing = undefined;
    const draft = extension.drafts.get(this.rootUri.fsPath);
    this.included = new Set(draft.included || []);
    this.fingerprints = draft.fingerprints || {};
    this.stale = new Set();
    this.recipients = draft.recipients || [];
    this.team = draft.team || "";
    this.private = Boolean(draft.private);
    this.sourceControl = vscode.scm.createSourceControl("handoff", `Handoff: ${path.basename(this.rootUri.fsPath)}`, this.rootUri);
    this.sourceControl.inputBox.placeholder = "Message for this Handoff (Ctrl/Cmd+Enter to create)";
    this.sourceControl.inputBox.value = draft.message || "";
    this.sourceControl.acceptInputCommand = { command: "handoff.create", title: "Create Handoff" };
    this.updateSharingStatus();
    this.includedGroup = this.sourceControl.createResourceGroup("handoffIncluded", "Included in Handoff");
    this.changesGroup = this.sourceControl.createResourceGroup("handoffChanges", "Changes");
    this.excludedGroup = this.sourceControl.createResourceGroup("handoffExcluded", "Excluded or unsupported");
    this.excludedGroup.hideWhenEmpty = true;
    this.lastMessage = this.sourceControl.inputBox.value;
    this.inputPoll = setInterval(() => {
      if (this.lastMessage === this.sourceControl.inputBox.value) return;
      this.lastMessage = this.sourceControl.inputBox.value;
      this.persist().catch(() => undefined);
    }, 1000);
    this.gitDisposable = git.state.onDidChange(() => this.scheduleRefresh());
  }

  dispose() {
    clearTimeout(this.refreshTimer);
    clearInterval(this.inputPoll);
    this.gitDisposable.dispose();
    this.sourceControl.dispose();
  }

  scheduleRefresh() {
    clearTimeout(this.refreshTimer);
    this.refreshTimer = setTimeout(() => this.refresh().catch((error) => this.extension.presentError(error)), 350);
  }

  async persist() {
    await this.extension.drafts.set(this.rootUri.fsPath, {
      included: [...this.included],
      fingerprints: this.fingerprints,
      message: this.sourceControl.inputBox.value,
      recipients: this.recipients,
      team: this.team,
      private: this.private
    });
  }

  async refresh() {
    if (this.refreshing) return this.refreshing;
    this.refreshing = this.extension.execute(["changes"], { cwd: this.rootUri.fsPath, timeoutMs: 30000 })
      .then(async (response) => {
        this.info = response.data.repository;
        const patterns = vscode.workspace.getConfiguration("handoff", this.rootUri).get("exclude", []);
        this.changes = response.data.changes.map((change) => {
          const pattern = excludedByPattern(change.path, patterns);
          return pattern && change.supported !== false
            ? { ...change, supported: false, excluded_reason: `Excluded by Handoff pattern ${pattern}` }
            : change;
        });
        const available = new Set(this.changes.filter((change) => change.supported !== false).map((change) => change.path));
        let draftChanged = false;
        let removed = false;
        const newlyStale = [];
        const byPath = new Map(this.changes.map((change) => [change.path, change]));
        for (const included of [...this.included]) {
          if (!available.has(included)) {
            this.included.delete(included);
            delete this.fingerprints[included];
            this.stale.delete(included);
            draftChanged = true;
            removed = true;
            continue;
          }
          const fingerprint = changeFingerprint(byPath.get(included));
          if (!this.fingerprints[included]) {
            this.fingerprints[included] = fingerprint;
            draftChanged = true;
          } else if (this.fingerprints[included] !== fingerprint && !this.stale.has(included)) {
            this.stale.add(included);
            newlyStale.push(included);
          } else if (this.fingerprints[included] === fingerprint) {
            this.stale.delete(included);
          }
        }
        if (draftChanged) {
          await this.persist();
          if (removed) vscode.window.showWarningMessage(`${repositoryLabel(this)}: the Handoff draft was updated because selected files disappeared or became unsupported.`);
        }
        if (newlyStale.length) vscode.window.showWarningMessage(`${repositoryLabel(this)}: ${newlyStale.length} included file${newlyStale.length === 1 ? " has" : "s have"} changed since selection. Review the draft before upload.`);
        this.render();
        this.extension.inbox?.refreshTree();
      })
      .finally(() => { this.refreshing = undefined; });
    return this.refreshing;
  }

  render() {
    const groups = groupChanges(this.changes, this.included);
    this.includedGroup.resourceStates = groups.included.map((change) => this.resource(change, "handoffIncluded"));
    this.changesGroup.resourceStates = groups.available.map((change) => this.resource(change, "handoffChanges"));
    this.excludedGroup.resourceStates = groups.excluded.map((change) => this.resource(change, "handoffExcluded"));
    this.sourceControl.count = this.included.size;
  }

  resource(change, group) {
    const uri = vscode.Uri.file(path.join(this.rootUri.fsPath, ...change.path.split("/")));
    const status = change.status || "modified";
    const draftStale = this.stale.has(change.path);
    const detail = [status, draftStale ? "changed after inclusion" : "", change.binary ? "binary" : "", change.size_bytes ? formatBytes(change.size_bytes) : "", change.excluded_reason || ""]
      .filter(Boolean).join(" · ");
    return {
      resourceUri: uri,
      change,
      repository: this,
      contextValue: change.supported === false ? "handoffExcluded" : "handoffChange",
      command: { command: "handoff.openChange", title: "Open Change", arguments: [this, change] },
      decorations: {
        strikeThrough: status === "deleted",
        faded: group === "handoffExcluded",
        tooltip: change.original_path ? `${detail}\nRenamed from ${change.original_path}` : detail,
        iconPath: new vscode.ThemeIcon(draftStale ? "warning" : statusIcons[status] || "diff-modified")
      }
    };
  }

  async include(resources) {
    const candidates = normalizeResources(resources).filter((resource) => resource.repository === this && resource.change.supported !== false);
    const sensitive = candidates.map(({ change }) => ({ change, reason: sensitiveFileReason(change.path) })).filter(({ reason }) => reason);
    const largeLimit = vscode.workspace.getConfiguration("handoff", this.rootUri).get("largeFileWarningBytes", 10 * 1024 * 1024);
    const large = candidates.filter(({ change }) => change.size_bytes > largeLimit);
    if (sensitive.length || large.length) {
      const lines = [
        ...sensitive.map(({ change, reason }) => `${change.path} (${reason})`),
        ...large.map(({ change }) => `${change.path} (${formatBytes(change.size_bytes)})`)
      ];
      const confirmation = await vscode.window.showWarningMessage(
        "Some selected files may contain sensitive data or are unusually large.",
        { modal: true, detail: lines.join("\n") },
        "Include Files"
      );
      if (confirmation !== "Include Files") return;
    }
    for (const { change } of candidates) {
      this.included.add(change.path);
      this.fingerprints[change.path] = changeFingerprint(change);
      this.stale.delete(change.path);
    }
    this.render();
    await this.persist();
  }

  async exclude(resources) {
    for (const { change } of normalizeResources(resources)) {
      this.included.delete(change.path);
      delete this.fingerprints[change.path];
      this.stale.delete(change.path);
    }
    this.render();
    await this.persist();
  }

  async includeAll() {
    await this.include(this.changes.filter((change) => change.supported !== false).map((change) => ({ change, repository: this })));
  }

  async excludeAll() {
    this.included.clear();
    this.fingerprints = {};
    this.stale.clear();
    this.render();
    await this.persist();
  }

  async discardDraft() {
    if (this.included.size || this.sourceControl.inputBox.value.trim()) {
      const action = await vscode.window.showWarningMessage(
        `Discard the Handoff draft for ${repositoryLabel(this)}?`,
        { modal: true },
        "Discard Draft"
      );
      if (action !== "Discard Draft") return;
    }
    this.included.clear();
    this.fingerprints = {};
    this.stale.clear();
    this.sourceControl.inputBox.value = "";
    this.recipients = [];
    this.team = "";
    this.private = false;
    this.updateSharingStatus();
    await this.extension.drafts.remove(this.rootUri.fsPath);
    this.render();
  }

  updateSharingStatus() {
    const audience = [this.team ? `team:${this.team}` : "", ...this.recipients].filter(Boolean).join(", ") || "team inbox";
    this.sourceControl.statusBarCommands = [{
      command: "handoff.setAudience",
      title: `$(people) ${audience}${this.private ? " · private" : ""}`,
      tooltip: "Choose recipients, team/channel, and visibility for this draft",
      arguments: [this]
    }];
  }

  async setAudience() {
    const audience = await vscode.window.showInputBox({
      prompt: "Recipients (user IDs or emails, comma-separated). Leave empty for the team inbox.",
      value: this.recipients.join(", "),
      ignoreFocusOut: true
    });
    if (audience === undefined) return;
    const team = await vscode.window.showInputBox({ prompt: "Optional team or channel", value: this.team, ignoreFocusOut: true });
    if (team === undefined) return;
    const visibility = await vscode.window.showQuickPick([
      { label: "Visible to server members", value: false },
      { label: "Private to recipients/team", value: true }
    ], { placeHolder: "Choose Handoff visibility" });
    if (!visibility) return;
    this.recipients = [...new Set(audience.split(",").map((value) => value.trim().toLowerCase()).filter(Boolean))];
    this.team = team.trim().toLowerCase();
    this.private = visibility.value && Boolean(this.team || this.recipients.length);
    this.updateSharingStatus();
    await this.persist();
  }
}

class RepositoryManager {
  constructor(extension) {
    this.extension = extension;
    this.models = new Map();
    this.disposables = [];
  }

  async activate() {
    const gitExtension = vscode.extensions.getExtension("vscode.git");
    if (!gitExtension) throw new HandoffCommandError("VS Code's built-in Git extension is required.", { code: "git_extension_missing" });
    const exports = gitExtension.isActive ? gitExtension.exports : await gitExtension.activate();
    this.api = exports.getAPI(1);
    for (const repository of this.api.repositories) await this.add(repository);
    this.disposables.push(this.api.onDidOpenRepository((repository) => this.add(repository).catch((error) => this.extension.presentError(error))));
    this.disposables.push(this.api.onDidCloseRepository((repository) => this.remove(repository)));
    await this.discoverNestedRepositories();
  }

  dispose() {
    for (const disposable of this.disposables) disposable.dispose();
    for (const model of this.models.values()) model.dispose();
    this.models.clear();
  }

  list() {
    return [...this.models.values()];
  }

  byRoot(root) {
    return this.models.get(String(root || "").toLowerCase());
  }

  fromValue(value) {
    if (value instanceof HandoffRepository) return value;
    if (value?.repository instanceof HandoffRepository) return value.repository;
    const root = value?.rootUri?.fsPath || value?.rootUri?.path;
    return this.byRoot(root) || this.byRoot(this.extension.context.workspaceState.get(lastRepositoryKey, "")) || this.list()[0];
  }

  async add(git) {
    const key = repositoryKey(git);
    if (this.models.has(key)) return this.models.get(key);
    const model = new HandoffRepository(this.extension, git);
    this.models.set(key, model);
    await vscode.commands.executeCommand("setContext", "handoff.hasRepositories", true);
    try {
      await model.refresh();
    } catch (error) {
      this.extension.log(`Repository refresh failed (${git.rootUri.fsPath}): ${error.message}`);
      if (/unknown command.*changes/i.test(error.message)) {
        error = new HandoffCommandError("The configured Handoff binary is too old for this extension. Update the binary or clear handoff.binaryPath.", { code: "incompatible_binary" });
      }
      this.extension.presentError(error);
    }
    this.extension.inbox?.refreshTree();
    return model;
  }

  remove(git) {
    const key = repositoryKey(git);
    this.models.get(key)?.dispose();
    this.models.delete(key);
    vscode.commands.executeCommand("setContext", "handoff.hasRepositories", this.models.size > 0);
    this.extension.inbox?.refreshTree();
  }

  async discoverNestedRepositories() {
    const folders = vscode.workspace.workspaceFolders || [];
    const maximumDepth = vscode.workspace.getConfiguration("handoff").get("repositorySearchDepth", 4);
    const ignored = new Set([".git", "node_modules", "vendor", "dist", "build", ".cache", ".next"]);
    const queue = folders.map((folder) => ({ uri: folder.uri, depth: 0 }));
    let inspected = 0;
    while (queue.length && inspected < 2000) {
      const { uri, depth } = queue.shift();
      inspected++;
      let entries;
      try { entries = await vscode.workspace.fs.readDirectory(uri); } catch { continue; }
      if (entries.some(([name]) => name === ".git")) {
        const opened = await this.api.openRepository(uri);
        if (opened) await this.add(opened);
        continue;
      }
      if (depth >= maximumDepth) continue;
      for (const [name, type] of entries) {
        if (type === vscode.FileType.Directory && !ignored.has(name)) queue.push({ uri: vscode.Uri.joinPath(uri, name), depth: depth + 1 });
      }
    }
  }

  async choose(placeHolder = "Select a Git repository") {
    const repositories = this.list();
    if (repositories.length === 0) throw new HandoffCommandError("Open a Git repository before using Handoff.", { code: "repository_required" });
    if (repositories.length === 1) {
      await this.remember(repositories[0]);
      return repositories[0];
    }
    const previous = this.extension.context.workspaceState.get(lastRepositoryKey, "").toLowerCase();
    const ordered = [...repositories].sort((left, right) => Number(repositoryKey(right) === previous) - Number(repositoryKey(left) === previous));
    const selected = await vscode.window.showQuickPick(ordered.map((repository) => ({
      label: repositoryLabel(repository),
      description: repository.info?.branch || "",
      detail: repository.rootUri.fsPath,
      repository
    })), { placeHolder });
    if (selected?.repository) await this.remember(selected.repository);
    return selected?.repository;
  }

  async remember(repository) {
    await this.extension.context.workspaceState.update(lastRepositoryKey, repositoryKey(repository));
  }
}

class InboxProvider {
  constructor(extension) {
    this.extension = extension;
    this.state = "idle";
    this.handoffs = [];
    this.recoveries = new Map();
    this.error = undefined;
    this.query = "";
    this.filter = "all";
    this.sort = "newest";
    this.nextOffset = 0;
    this.hasMore = false;
    this.knownIDs = undefined;
    const saved = extension.context.workspaceState.get(inboxStateKey, {});
    this.read = new Set(saved.read || []);
    this.archived = new Set(saved.archived || []);
    this.changeEmitter = new vscode.EventEmitter();
    this.onDidChangeTreeData = this.changeEmitter.event;
  }

  dispose() { this.changeEmitter.dispose(); }
  refreshTree() { this.changeEmitter.fire(undefined); this.updateBadge(); }
  getTreeItem(item) { return item; }

  getChildren(element) {
    if (element?.kind === "repository") return element.handoffs.map((handoff) => this.handoffItem(handoff, element.repository));
    if (element?.kind === "unmatched") return element.handoffs.map((handoff) => this.handoffItem(handoff));
    if (element) return [];
    if (this.state === "loading") return [this.stateItem("Loading Handoffs…", "", "sync~spin")];
    if (this.state === "error") {
      const configure = ["not_configured", "invalid_config"].includes(this.error?.code);
      return [this.stateItem(configure ? "Configure Handoff" : "Inbox unavailable", this.error?.message || "Refresh to try again.", configure ? "gear" : "error", configure ? "handoff.configure" : "handoff.refreshInbox")];
    }
    const visible = this.filteredHandoffs();
    const { groups, unmatched } = groupHandoffs(visible, this.extension.repositories.list().filter((repository) => repository.info));
    const items = [];
    for (const repository of this.extension.repositories.list()) {
      const recovery = this.recoveries.get(repositoryKey(repository));
      if (recovery?.active) items.push(this.recoveryItem(repository, recovery));
    }
    for (const group of groups) items.push(this.repositoryItem(group.repository, group.handoffs));
    if (unmatched.length && this.filter !== "compatible") items.push(this.unmatchedItem(unmatched));
    if (this.hasMore) items.push(this.stateItem("Load more Handoffs", `${this.nextOffset} loaded`, "more", "handoff.loadMoreInbox"));
    if (items.length === 0) items.push(this.stateItem("Inbox is empty", this.query ? "No Handoffs match the current search and filters." : "No Handoffs are available.", "inbox"));
    return items;
  }

  filteredHandoffs() {
    const archived = (handoff) => this.archived.has(handoff.id) || handoff.viewer_archived;
    let result = this.handoffs.filter((handoff) => this.filter === "archived" ? archived(handoff) : !archived(handoff));
    if (this.query) {
      const query = this.query.toLowerCase();
      result = result.filter((handoff) => [handoff.id, handoff.message, handoff.author, handoff.author_email, handoff.branch, handoff.project]
        .some((value) => String(value || "").toLowerCase().includes(query)));
    }
    const repositories = this.extension.repositories.list();
    if (this.filter === "compatible") result = result.filter((handoff) => matchingRepositories(handoff, repositories).length);
    if (this.filter === "unmatched") result = result.filter((handoff) => !matchingRepositories(handoff, repositories).length);
    if (this.filter === "unread") result = result.filter((handoff) => !this.read.has(handoff.id) && !handoff.viewer_read);
    if (this.filter === "sent") result = result.filter((handoff) => handoff.owner_id && handoff.owner_id === this.identity?.id);
    if (this.filter === "assigned") result = result.filter((handoff) => handoff.assigned_to && [this.identity?.id, this.identity?.email].includes(handoff.assigned_to));
    if (this.filter.startsWith("team:")) result = result.filter((handoff) => handoff.team === this.filter.slice(5));
    if (this.filter.startsWith("repository:")) result = result.filter((handoff) => handoff.repository_id === this.filter.slice(11));
    if (this.filter.startsWith("author:")) result = result.filter((handoff) => [handoff.author, handoff.author_email, handoff.owner_id].some((value) => String(value || "").toLowerCase() === this.filter.slice(7)));
    if (this.filter.startsWith("branch:")) result = result.filter((handoff) => handoff.branch === this.filter.slice(7));
    if (this.filter.startsWith("age:")) {
      const days = Number(this.filter.slice(4));
      const cutoff = Date.now() - days * 86400000;
      result = result.filter((handoff) => Date.parse(handoff.stored_at || handoff.created_at || "") >= cutoff);
    }
    const direction = this.sort === "oldest" ? 1 : -1;
    return [...result].sort((left, right) => {
      if (this.sort === "sender") return String(left.author || "").localeCompare(String(right.author || ""));
      if (this.sort === "expiry") return String(left.expires_at || "").localeCompare(String(right.expires_at || ""));
      return direction * String(left.stored_at || left.created_at || "").localeCompare(String(right.stored_at || right.created_at || ""));
    });
  }

  stateItem(label, description, icon, command) {
    const item = new vscode.TreeItem(label);
    item.description = description;
    item.iconPath = new vscode.ThemeIcon(icon);
    item.contextValue = "handoff.inboxState";
    if (command) item.command = { command, title: label };
    return item;
  }

  repositoryItem(repository, handoffs) {
    /** @type {any} */
    const item = new vscode.TreeItem(repositoryLabel(repository), vscode.TreeItemCollapsibleState.Expanded);
    item.kind = "repository";
    item.repository = repository;
    item.handoffs = handoffs;
    item.description = `${handoffs.length} · ${repository.info.branch}`;
    item.tooltip = repository.rootUri.fsPath;
    item.accessibilityInformation = { label: `${repositoryLabel(repository)}, ${handoffs.length} Handoffs, branch ${repository.info.branch}` };
    item.iconPath = new vscode.ThemeIcon("repo");
    item.contextValue = "handoff.repositoryGroup";
    return item;
  }

  unmatchedItem(handoffs) {
    /** @type {any} */
    const item = new vscode.TreeItem("No matching local repository", vscode.TreeItemCollapsibleState.Collapsed);
    item.kind = "unmatched";
    item.handoffs = handoffs;
    item.description = String(handoffs.length);
    item.iconPath = new vscode.ThemeIcon("warning");
    item.accessibilityInformation = { label: `${handoffs.length} Handoffs have no matching local repository` };
    item.contextValue = "handoff.unmatchedGroup";
    return item;
  }

  handoffItem(handoff, repository) {
    const unread = !this.read.has(handoff.id) && !handoff.viewer_read;
    const expired = handoff.expires_at && Date.parse(handoff.expires_at) <= Date.now();
    /** @type {any} */
    const item = new vscode.TreeItem(handoffLabel(handoff));
    item.id = `handoff:${repository?.rootUri.fsPath || "unmatched"}:${handoff.id}`;
    item.handoff = handoff;
    item.repository = repository;
    item.description = `${unread ? "● " : ""}${handoffDescription(handoff)} · ${relativeTime(handoff.stored_at || handoff.created_at)}`;
    item.tooltip = `${handoffTooltip(handoff)}${handoff.expires_at ? `\nExpires: ${handoff.expires_at}` : ""}`;
    item.iconPath = new vscode.ThemeIcon(expired ? "clock" : "git-pull-request");
    const owned = handoff.owner_id && handoff.owner_id === this.identity?.id;
    const archived = this.archived.has(handoff.id) || handoff.viewer_archived;
    item.contextValue = `handoff.inboxItem.${archived ? "archived" : "active"}${owned ? ".owned" : ""}.${unread ? "unread" : "read"}`;
    item.accessibilityInformation = { label: `${unread ? "Unread" : "Read"} Handoff, ${handoffLabel(handoff)}, from ${handoff.author || "unknown sender"}, ${handoff.file_count || 0} files` };
    item.command = { command: "handoff.inspectInboxItem", title: "Inspect Handoff", arguments: [item] };
    return item;
  }

  recoveryItem(repository, recovery) {
    /** @type {any} */
    const item = new vscode.TreeItem(`Recovery required: ${repositoryLabel(repository)}`);
    item.id = `handoff:recovery:${repository.rootUri.fsPath}`;
    item.repository = repository;
    item.recovery = recovery;
    item.description = `${recovery.handoff_id} · ${String(recovery.stage).replaceAll("_", " ")}`;
    item.tooltip = recovery.conflicted_files?.length ? `Conflicted files:\n${recovery.conflicted_files.join("\n")}` : "Open recovery actions.";
    item.iconPath = new vscode.ThemeIcon("warning");
    item.contextValue = "handoff.recoveryItem";
    item.accessibilityInformation = { label: `Recovery required in ${repositoryLabel(repository)} for Handoff ${recovery.handoff_id}` };
    item.command = { command: "handoff.showRecovery", title: "Show Recovery", arguments: [repository] };
    return item;
  }

  async saveState() {
    await this.extension.context.workspaceState.update(inboxStateKey, { read: [...this.read], archived: [...this.archived] });
    this.updateBadge();
  }

  commandRepository(handoff) {
    return matchingRepositories(handoff || {}, this.extension.repositories.list())[0] || this.extension.repositories.list()[0];
  }

  async markRead(handoff, read = true) {
    const id = handoff?.id;
    if (!id) return;
    if (read) this.read.add(id); else this.read.delete(id);
    await this.saveState();
    const repository = this.commandRepository(handoff);
    if (repository) this.extension.execute([read ? "read" : "unread", id], { cwd: repository.rootUri.fsPath }).catch(() => undefined);
    this.refreshTree();
  }

  async setArchived(handoff, archived) {
    const id = handoff?.id;
    if (!id) return;
    if (archived) this.archived.add(id); else this.archived.delete(id);
    await this.saveState();
    const repository = this.commandRepository(handoff);
    if (repository) this.extension.execute([archived ? "archive" : "unarchive", id], { cwd: repository.rootUri.fsPath }).catch(() => undefined);
    this.refreshTree();
  }

  updateBadge() {
    if (!this.extension.inboxView) return;
    const repositories = this.extension.repositories?.list() || [];
    const unread = this.handoffs.filter((handoff) => !this.read.has(handoff.id) && !handoff.viewer_read && !this.archived.has(handoff.id) && !handoff.viewer_archived && matchingRepositories(handoff, repositories).length).length;
    this.extension.inboxView.badge = unread ? { value: unread, tooltip: `${unread} unread compatible Handoff${unread === 1 ? "" : "s"}` } : undefined;
  }

  async refresh(notify = false) {
    this.state = "loading";
    this.refreshTree();
    const repositories = this.extension.repositories.list();
    const cwd = repositories[0]?.rootUri.fsPath || vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
    if (!cwd) {
      this.state = "ready";
      this.handoffs = [];
      this.refreshTree();
      return;
    }
    try {
      const [inbox, identity, ...statuses] = await Promise.all([
        this.extension.execute(["list", "--all", "--archived", "--limit", "100"], { cwd, timeoutMs: 30000 }),
        this.extension.execute(["whoami"], { cwd, timeoutMs: 15000 }).catch(() => undefined),
        ...repositories.map((repository) => this.extension.execute(["status"], { cwd: repository.rootUri.fsPath, timeoutMs: 15000 }).catch(() => undefined))
      ]);
      const nextHandoffs = inbox.data.handoffs || [];
      const nextIDs = new Set(nextHandoffs.map((handoff) => handoff.id));
      if (this.knownIDs && vscode.workspace.getConfiguration("handoff").get("desktopNotifications", true)) {
        const repositoriesNow = this.extension.repositories.list();
        const fresh = nextHandoffs.filter((handoff) => !this.knownIDs.has(handoff.id) && matchingRepositories(handoff, repositoriesNow).length);
        if (fresh.length) {
          const action = await vscode.window.showInformationMessage(`${fresh.length} new compatible Handoff${fresh.length === 1 ? "" : "s"} received.`, "Open Inbox");
          if (action === "Open Inbox") vscode.commands.executeCommand("handoff.openInbox");
        }
      }
      this.handoffs = nextHandoffs;
      this.knownIDs = nextIDs;
      this.nextOffset = inbox.data.next_offset || this.handoffs.length;
      this.hasMore = Boolean(inbox.data.has_more);
      this.identity = identity?.data?.identity;
      this.recoveries.clear();
      statuses.forEach((status, index) => {
        if (status?.data?.recovery) this.recoveries.set(repositoryKey(repositories[index]), status.data.recovery);
      });
      this.error = undefined;
      this.state = "ready";
      if (notify) vscode.window.showInformationMessage(`Handoff inbox refreshed: ${this.handoffs.length} available.`);
    } catch (error) {
      this.error = error;
      this.state = "error";
      throw error;
    } finally {
      this.refreshTree();
    }
  }

  async loadMore() {
    if (!this.hasMore) return;
    const repository = this.extension.repositories.list()[0];
    const cwd = repository?.rootUri.fsPath || vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
    if (!cwd) return;
    const page = await this.extension.execute(["list", "--all", "--archived", "--limit", "100", "--offset", String(this.nextOffset)], { cwd, timeoutMs: 30000 });
    const seen = new Set(this.handoffs.map((handoff) => handoff.id));
    this.handoffs.push(...(page.data.handoffs || []).filter((handoff) => !seen.has(handoff.id)));
    this.nextOffset = page.data.next_offset || this.handoffs.length;
    this.hasMore = Boolean(page.data.has_more);
    this.refreshTree();
  }
}

class HandoffExtension {
  constructor(context) {
    this.context = context;
    this.drafts = new DraftStore(context);
    this.output = vscode.window.createOutputChannel("Handoff", { log: true });
    this.repositories = new RepositoryManager(this);
    this.lastError = undefined;
  }

  async activate() {
    this.context.subscriptions.push(this.output, this.repositories);
    this.inbox = new InboxProvider(this);
    this.context.subscriptions.push(this.inbox);
    this.inboxView = vscode.window.createTreeView("handoff.inbox", { treeDataProvider: this.inbox, showCollapseAll: true });
    this.context.subscriptions.push(this.inboxView);
    this.context.subscriptions.push(vscode.workspace.registerTextDocumentContentProvider("handoff-base", new BaseContentProvider(this.repositories)));
    this.context.subscriptions.push(vscode.workspace.registerTextDocumentContentProvider("handoff-empty", new EmptyContentProvider()));
    this.context.subscriptions.push(vscode.window.registerUriHandler({ handleUri: (uri) => this.handleUri(uri) }));
    this.registerCommands();
    await this.repositories.activate();
    const firstRepository = this.repositories.list()[0];
    if (firstRepository) {
      const version = await this.execute(["version"], { cwd: firstRepository.rootUri.fsPath, json: false, timeoutMs: 15000 });
      this.log(`Handoff binary version: ${version || "unknown"}`);
    }
    this.context.subscriptions.push(this.inboxView.onDidChangeVisibility(({ visible }) => {
      if (visible && this.inbox.state === "idle") this.inbox.refresh().catch((error) => this.presentError(error));
    }));
    if (this.inboxView.visible) this.inbox.refresh().catch((error) => this.presentError(error));
    const pollSeconds = vscode.workspace.getConfiguration("handoff").get("pollIntervalSeconds", 60);
    if (pollSeconds >= 30) {
      const poll = setInterval(() => this.inbox.refresh().catch(() => undefined), pollSeconds * 1000);
      this.context.subscriptions.push({ dispose: () => clearInterval(poll) });
    }
    await this.offerOnboarding();
  }

  log(message) {
    this.output.info(String(message).replace(/Bearer\s+\S+/gi, "Bearer [redacted]").replace(/[A-Za-z0-9_-]{32,}/g, "[redacted]"));
  }

  getBinary() {
    const configuredPath = vscode.workspace.getConfiguration("handoff").get("binaryPath", "");
    return resolveBinary({ extensionPath: this.context.extensionPath, configuredPath });
  }

  async profileEnvironment(cwd) {
    const folder = vscode.workspace.getWorkspaceFolder(vscode.Uri.file(cwd || ""));
    const repositories = this.repositories.list().filter((repository) => {
      const relative = path.relative(repository.rootUri.fsPath, cwd || "");
      return relative === "" || (!relative.startsWith("..") && !path.isAbsolute(relative));
    }).sort((left, right) => right.rootUri.fsPath.length - left.rootUri.fsPath.length);
    const bindings = this.context.workspaceState.get(profileBindingsKey, {});
    const boundProfileID = repositories[0] ? bindings[repositoryKey(repositories[0])] : "";
    const profileID = boundProfileID || vscode.workspace.getConfiguration("handoff", folder?.uri).get("profile", "");
    if (!profileID) return process.env;
    const profiles = this.context.globalState.get(profileStateKey, []);
    const profile = profiles.find((value) => value.id === profileID);
    const token = profile ? await this.context.secrets.get(`handoff.profile.${profile.id}`) : undefined;
    return profile && token ? { ...process.env, HANDOFF_SERVER: profile.server, HANDOFF_TOKEN: token } : process.env;
  }

  async execute(args, options = {}) {
    const started = Date.now();
    this.log(`Running handoff ${args[0]} in ${options.cwd || "workspace"}`);
    try {
      const result = await runHandoff(this.getBinary(), args, {
        cwd: options.cwd,
        input: options.input,
        json: options.json,
        signal: options.signal,
        timeoutMs: options.timeoutMs,
        env: await this.profileEnvironment(options.cwd)
      });
      this.log(`Completed handoff ${args[0]} in ${Date.now() - started}ms`);
      return result;
    } catch (error) {
      this.lastError = error;
      this.log(`Failed handoff ${args[0]} (${error.code || "error"}): ${error.message}`);
      throw error;
    }
  }

  async progress(title, operation, cancellable = true) {
    return vscode.window.withProgress({ location: vscode.ProgressLocation.Notification, title, cancellable }, (_progress, token) => {
      const controller = new AbortController();
      token.onCancellationRequested(() => controller.abort());
      return operation(controller.signal);
    });
  }

  register(command, callback) {
    this.context.subscriptions.push(vscode.commands.registerCommand(command, (...args) => Promise.resolve(callback(...args)).catch((error) => this.presentError(error))));
  }

  registerCommands() {
    this.register("handoff.configure", () => this.configure());
    this.register("handoff.testConnection", () => this.testConnection());
    this.register("handoff.selectProfile", () => this.selectProfile());
    this.register("handoff.refreshRepositories", () => this.repositories.discoverNestedRepositories());
    this.register("handoff.refreshChanges", async (value) => (this.repositories.fromValue(value) || await this.repositories.choose())?.refresh());
    this.register("handoff.include", (...resources) => this.resourceRepository(resources)?.include(resources));
    this.register("handoff.exclude", (...resources) => this.resourceRepository(resources)?.exclude(resources));
    this.register("handoff.includeAll", async (value) => (this.repositories.fromValue(value) || await this.repositories.choose())?.includeAll());
    this.register("handoff.excludeAll", async (value) => (this.repositories.fromValue(value) || await this.repositories.choose())?.excludeAll());
    this.register("handoff.discardDraft", async (value) => (this.repositories.fromValue(value) || await this.repositories.choose())?.discardDraft());
    this.register("handoff.setAudience", async (value) => (this.repositories.fromValue(value) || await this.repositories.choose())?.setAudience());
    this.register("handoff.create", (value) => this.createHandoff(this.repositories.fromValue(value)));
    this.register("handoff.openChange", (repository, change) => this.openChange(repository, change));
    this.register("handoff.openInbox", () => vscode.commands.executeCommand("workbench.view.extension.handoff"));
    this.register("handoff.refreshInbox", () => this.inbox.refresh(true));
    this.register("handoff.refreshRepositoryInbox", (item) => this.refreshRepositoryInbox(item?.repository || item));
    this.register("handoff.loadMoreInbox", () => this.inbox.loadMore());
    this.register("handoff.searchInbox", () => this.searchInbox());
    this.register("handoff.filterInbox", () => this.filterInbox());
    this.register("handoff.sortInbox", () => this.sortInbox());
    this.register("handoff.inspectInboxItem", (item) => this.inspectInboxItem(item));
    this.register("handoff.pullInboxItem", (item) => this.pullInboxItem(item));
    this.register("handoff.previewPull", (item) => this.previewPull(item));
    this.register("handoff.copyHandoffID", (item) => this.copyHandoffID(item));
    this.register("handoff.copyHandoffDetails", (item) => this.copyHandoffDetails(item));
    this.register("handoff.copyHandoffLink", (item) => this.copyHandoffLink(item));
    this.register("handoff.markRead", (item) => this.inbox.markRead(handoffFromCommand(item), true));
    this.register("handoff.markUnread", (item) => this.inbox.markRead(handoffFromCommand(item), false));
    this.register("handoff.archive", (item) => this.inbox.setArchived(handoffFromCommand(item), true));
    this.register("handoff.unarchive", (item) => this.inbox.setArchived(handoffFromCommand(item), false));
    this.register("handoff.delete", (item) => this.deleteHandoff(item));
    this.register("handoff.comment", (item) => this.commentOnHandoff(item));
    this.register("handoff.showComments", (item) => this.showComments(item));
    this.register("handoff.showAudit", (item) => this.showAudit(item));
    this.register("handoff.acknowledge", (item) => this.lifecycleEvent(item, "acknowledge"));
    this.register("handoff.markApplied", (item) => this.lifecycleEvent(item, "applied"));
    this.register("handoff.revoke", (item) => this.lifecycleEvent(item, "revoke", true));
    this.register("handoff.assign", (item) => this.assignHandoff(item));
    this.register("handoff.setExpiry", (item) => this.setHandoffExpiry(item));
    this.register("handoff.pullHandoff", () => this.pullByID());
    this.register("handoff.showRecovery", (repository) => this.showRecovery(repository));
    this.register("handoff.continueRecovery", (repository) => this.continueRecovery(repository));
    this.register("handoff.abortRecovery", (repository) => this.abortRecovery(repository));
    this.register("handoff.openConflicts", (repository) => this.openConflicts(repository));
    this.register("handoff.copyDiagnostics", () => this.copyDiagnostics());
    this.register("handoff.showOutput", () => this.output.show());
    this.register("handoff.pushChanges", async () => {
      const repository = await this.repositories.choose();
      if (repository) await repository.includeAll();
    });
    this.register("handoff.pushSelectedChanges", async () => {
      const repository = await this.repositories.choose();
      if (repository) await vscode.commands.executeCommand("workbench.view.scm");
    });
  }

  resourceRepository(resources) {
    return normalizeResources(resources)[0]?.repository;
  }

  async createHandoff(repository) {
    repository ||= await this.repositories.choose("Select the repository for this Handoff");
    if (!repository) return;
    await repository.refresh();
    const messageError = validateMessage(repository.sourceControl.inputBox.value);
    if (messageError) {
      vscode.window.showWarningMessage(messageError);
      return;
    }
    const included = repository.changes.filter((change) => repository.included.has(change.path) && change.supported !== false);
    if (!included.length) {
      vscode.window.showInformationMessage("Include at least one changed file before creating the Handoff.");
      return;
    }
    const stale = included.filter((change) => repository.stale.has(change.path));
    if (stale.length) {
      const review = await vscode.window.showWarningMessage(
        `${stale.length} included file${stale.length === 1 ? " has" : "s have"} changed since selection.`,
        { modal: true, detail: stale.map((change) => change.path).join("\n") },
        "Use Current Versions"
      );
      if (review !== "Use Current Versions") return;
      for (const change of stale) {
        repository.fingerprints[change.path] = changeFingerprint(change);
        repository.stale.delete(change.path);
      }
      await repository.persist();
      repository.render();
    }
    const paths = selectedPaths(included);
    const message = repository.sourceControl.inputBox.value.trim();
    const sharing = { recipients: repository.recipients, team: repository.team, private: repository.private };
    const preview = await this.progress("Preparing Handoff preview…", (signal) => this.execute(buildPushArguments(message, { dryRun: true, paths, ...sharing }), {
      cwd: repository.rootUri.fsPath,
      signal,
      timeoutMs: 120000
    }));
    const summary = preview.data.handoff;
    const action = await vscode.window.showWarningMessage(
      `Create Handoff from ${repositoryLabel(repository)} with ${summary.file_count} file${summary.file_count === 1 ? "" : "s"}?`,
      { modal: true, detail: `${message}\n\n${summary.files.join("\n")}` },
      "Create Handoff"
    );
    if (action !== "Create Handoff") return;
    const result = await this.progress("Uploading Handoff…", (signal) => this.execute(buildPushArguments(message, { paths, ...sharing }), {
      cwd: repository.rootUri.fsPath,
      signal,
      timeoutMs: 180000
    }), false);
    const handoff = result.data.handoff;
    await vscode.env.clipboard.writeText(handoff.id);
    repository.included.clear();
    repository.fingerprints = {};
    repository.stale.clear();
    repository.sourceControl.inputBox.value = "";
    repository.recipients = [];
    repository.team = "";
    repository.private = false;
    repository.updateSharingStatus();
    await repository.persist();
    await repository.refresh();
    this.inbox.refresh().catch(() => undefined);
    const selected = await vscode.window.showInformationMessage(
      `Handoff ${handoff.id} created from ${repositoryLabel(repository)}: ${handoff.file_count} file${handoff.file_count === 1 ? "" : "s"}. ID copied.`,
      "Open Inbox"
    );
    if (selected === "Open Inbox") await vscode.commands.executeCommand("handoff.openInbox");
  }

  async openChange(repository, change) {
    const current = vscode.Uri.file(path.join(repository.rootUri.fsPath, ...change.path.split("/")));
    if (change.binary || change.status === "untracked") {
      await vscode.commands.executeCommand("vscode.open", current);
      return;
    }
    const originalPath = change.original_path || change.path;
    const left = vscode.Uri.from({
      scheme: "handoff-base",
      path: `/${originalPath}`,
      query: JSON.stringify({ root: repository.rootUri.fsPath, path: originalPath, ref: "HEAD" })
    });
    const right = change.status === "deleted"
      ? vscode.Uri.from({ scheme: "handoff-empty", path: `/deleted/${path.basename(change.path)}` })
      : current;
    await vscode.commands.executeCommand("vscode.diff", left, right, `${change.path} (${change.status})`);
  }

  async compatibleRepository(handoff, preferred) {
    const matches = matchingRepositories(handoff, this.repositories.list());
    if (preferred && matches.includes(preferred)) return preferred;
    if (matches.length === 1) return matches[0];
    if (matches.length === 0) {
      const action = await vscode.window.showWarningMessage(
        `No open Git repository matches ${handoff.project || handoff.repository_id}.`,
        "Open Folder"
      );
      if (action === "Open Folder") await vscode.commands.executeCommand("vscode.openFolder");
      return undefined;
    }
    /** @type {any} */
    const selected = await vscode.window.showQuickPick(matches.map((repository) => ({
      label: repositoryLabel(repository),
      description: repository.info.branch,
      detail: repository.rootUri.fsPath,
      repository
    })), { placeHolder: "Select the matching clone that should receive this Handoff" });
    return selected?.repository;
  }

  async inspectInboxItem(item) {
    const handoff = handoffFromCommand(item);
    if (!handoff?.id) return;
    await this.inbox.markRead(handoff, true);
    const repository = await this.compatibleRepository(handoff, item?.repository);
    const cwd = repository?.rootUri.fsPath || this.repositories.list()[0]?.rootUri.fsPath;
    if (!cwd) return;
    const inspected = await this.execute(["inspect", handoff.id], { cwd, timeoutMs: 30000 });
    const full = inspected.data.handoff;
    const actions = repository ? ["Pull", "Copy ID", "Copy Details"] : ["Copy ID", "Copy Details"];
    const action = await vscode.window.showInformationMessage(
      `${full.author || "Unknown sender"}: ${full.message || "Handoff changes"}`,
      { modal: true, detail: handoffInspection(full) },
      ...actions
    );
    if (action === "Pull") await this.pull(full, repository);
    if (action === "Copy ID") await this.copyHandoffID(full);
    if (action === "Copy Details") await this.copyHandoffDetails(full);
  }

  async pullInboxItem(item) {
    const handoff = handoffFromCommand(item);
    if (!handoff?.id) return;
    const repository = await this.compatibleRepository(handoff, item?.repository);
    if (repository) await this.pull(handoff, repository);
  }

  async previewPull(item) {
    const handoff = handoffFromCommand(item);
    if (!handoff?.id) return;
    const repository = await this.compatibleRepository(handoff, item?.repository);
    if (!repository) return;
    const preview = await this.progress("Inspecting Handoff safety…", (signal) => this.execute(["pull", "--dry-run", handoff.id], { cwd: repository.rootUri.fsPath, signal, timeoutMs: 60000 }));
    const compatibility = preview.data.compatibility;
    const localChanges = compatibility.local_changes?.length ? `\n\nExisting local changes:\n${compatibility.local_changes.join("\n")}` : "";
    const conflicts = compatibility.potential_conflicts?.length ? `\n\nLikely conflicts:\n${compatibility.potential_conflicts.join("\n")}` : "";
    await vscode.window.showInformationMessage(
      `Pull preview: ${compatibility.risk} risk`,
      { modal: true, detail: `${handoffInspection(preview.data.handoff)}\n\nCompatibility:\n${(compatibility.warnings?.length ? compatibility.warnings : ["No warnings detected."]).join("\n")}${localChanges}${conflicts}` },
      "OK"
    );
  }

  async pull(handoff, repository) {
    await repository.refresh();
    const preview = await this.progress("Inspecting Handoff…", (signal) => this.execute(["pull", "--dry-run", handoff.id], {
      cwd: repository.rootUri.fsPath,
      signal,
      timeoutMs: 60000
    }));
    const full = preview.data.handoff;
    const compatibility = preview.data.compatibility || {};
    const warnings = [...(compatibility.warnings || [])];
    if (compatibility.potential_conflicts?.length) warnings.push(`Likely conflicts: ${compatibility.potential_conflicts.join(", ")}`);
    if (compatibility.risk === "blocked") {
      throw new HandoffCommandError("This Handoff does not belong to the selected repository.", { code: "repository_mismatch" });
    }
    const confirmLowRisk = vscode.workspace.getConfiguration("handoff", repository.rootUri).get("confirmLowRiskPull", true);
    if (warnings.length || confirmLowRisk) {
      const action = await vscode.window.showWarningMessage(
        `Pull Handoff ${full.id} into ${repositoryLabel(repository)}?`,
        { modal: true, detail: [handoffInspection(full), warnings.length ? `\nWarnings:\n${warnings.join("\n")}` : ""].join("\n") },
        "Pull Handoff"
      );
      if (action !== "Pull Handoff") return;
    }
    try {
      await this.progress("Applying Handoff…", (signal) => this.execute(["pull", "--yes", full.id], {
        cwd: repository.rootUri.fsPath,
        signal,
        timeoutMs: 180000
      }), false);
      const preserved = compatibility.local_changes?.length || 0;
      vscode.window.showInformationMessage(`Handoff ${full.id} applied as local, uncommitted changes in ${repositoryLabel(repository)}.${preserved ? ` Preserved ${preserved} existing local change${preserved === 1 ? "" : "s"}.` : ""}`);
    } finally {
      await repository.refresh().catch(() => undefined);
      await this.inbox.refresh().catch(() => undefined);
    }
  }

  async pullByID() {
    const id = await vscode.window.showInputBox({
      prompt: "Handoff ID to pull",
      placeHolder: "abcdef123456",
      ignoreFocusOut: true,
      validateInput: (value) => /^[0-9a-f]{12}$/i.test(value) ? undefined : "Enter a 12-character Handoff ID."
    });
    if (!id) return;
    const repository = await this.repositories.choose("Select the repository that should receive this Handoff");
    if (!repository) return;
    const inspected = await this.execute(["inspect", id], { cwd: repository.rootUri.fsPath });
    const compatible = await this.compatibleRepository(inspected.data.handoff, repository);
    if (compatible) await this.pull(inspected.data.handoff, compatible);
  }

  async showRecovery(repository) {
    repository = this.repositories.fromValue(repository) || await this.repositories.choose();
    if (!repository) return;
    const result = await this.execute(["status"], { cwd: repository.rootUri.fsPath });
    const recovery = result.data.recovery;
    if (!recovery.active) {
      vscode.window.showInformationMessage(`No Handoff recovery is active in ${repositoryLabel(repository)}.`);
      return;
    }
    const actions = [];
    if (recovery.conflicted_files?.length) actions.push("Open Conflicts");
    if (recovery.can_continue) actions.push("Continue Recovery");
    if (recovery.can_abort) actions.push("Abort Recovery");
    const selected = await vscode.window.showWarningMessage(
      `Handoff ${recovery.handoff_id}: ${String(recovery.stage).replaceAll("_", " ")}`,
      { modal: true, detail: recovery.conflicted_files?.length ? `Resolve and stage these files before continuing:\n${recovery.conflicted_files.join("\n")}\n\nAbort restores the original local state.` : "Continue completes the apply. Abort restores the original local state." },
      ...actions
    );
    if (selected === "Open Conflicts") await this.openConflicts(repository, recovery);
    if (selected === "Continue Recovery") await this.continueRecovery(repository, recovery);
    if (selected === "Abort Recovery") await this.abortRecovery(repository, recovery);
  }

  async recovery(repository) {
    const result = await this.execute(["status"], { cwd: repository.rootUri.fsPath });
    return result.data.recovery;
  }

  async continueRecovery(repository, known) {
    repository = this.repositories.fromValue(repository) || await this.repositories.choose();
    if (!repository) return;
    const recovery = known?.handoff_id ? known : await this.recovery(repository);
    if (!recovery.active) return vscode.window.showInformationMessage("No Handoff recovery is active.");
    await this.progress("Continuing Handoff recovery…", () => this.execute(["continue", recovery.handoff_id], { cwd: repository.rootUri.fsPath, json: false, timeoutMs: 120000 }), false);
    vscode.window.showInformationMessage(`Handoff ${recovery.handoff_id} recovery completed.`);
    await repository.refresh();
    await this.inbox.refresh();
  }

  async abortRecovery(repository, known) {
    repository = this.repositories.fromValue(repository) || await this.repositories.choose();
    if (!repository) return;
    const recovery = known?.handoff_id ? known : await this.recovery(repository);
    if (!recovery.active) return vscode.window.showInformationMessage("No Handoff recovery is active.");
    const confirmation = await vscode.window.showWarningMessage(
      `Abort Handoff ${recovery.handoff_id} and restore the exact original local state?`,
      { modal: true },
      "Abort Recovery"
    );
    if (confirmation !== "Abort Recovery") return;
    await this.progress("Aborting Handoff recovery…", () => this.execute(["abort", recovery.handoff_id], { cwd: repository.rootUri.fsPath, json: false, timeoutMs: 120000 }), false);
    vscode.window.showInformationMessage(`Handoff ${recovery.handoff_id} was aborted; original local changes were restored.`);
    await repository.refresh();
    await this.inbox.refresh();
  }

  async openConflicts(repository, known) {
    repository = this.repositories.fromValue(repository) || await this.repositories.choose();
    if (!repository) return;
    const recovery = known?.conflicted_files ? known : await this.recovery(repository);
    for (const file of recovery.conflicted_files || []) {
      await vscode.window.showTextDocument(vscode.Uri.file(path.join(repository.rootUri.fsPath, ...file.split("/"))), { preview: false });
    }
  }

  async searchInbox() {
    const value = await vscode.window.showInputBox({ prompt: "Search Handoffs by message, author, ID, branch, or project", value: this.inbox.query });
    if (value === undefined) return;
    this.inbox.query = value.trim();
    this.inbox.refreshTree();
  }

  async refreshRepositoryInbox(value) {
    const repository = this.repositories.fromValue(value);
    if (!repository) return;
    await repository.refresh();
    const status = await this.execute(["status"], { cwd: repository.rootUri.fsPath, timeoutMs: 15000 }).catch(() => undefined);
    if (status?.data?.recovery) this.inbox.recoveries.set(repositoryKey(repository), status.data.recovery);
    this.inbox.refreshTree();
  }

  async filterInbox() {
    const options = [
      ["All Handoffs", "all"], ["Compatible", "compatible"], ["No matching repository", "unmatched"], ["Unread", "unread"], ["Archived", "archived"], ["Sent / Outbox", "sent"], ["Assigned to me", "assigned"],
      ["Repository…", "choose:repository"], ["Author…", "choose:author"], ["Branch…", "choose:branch"], ["Age…", "choose:age"],
      ...(this.inbox.identity?.teams || []).map((team) => [`Team: ${team}`, `team:${team}`])
    ];
    const selected = await vscode.window.showQuickPick(options.map(([label, value]) => ({ label, value, picked: value === this.inbox.filter })), { placeHolder: "Filter Handoff Inbox" });
    if (!selected) return;
    if (!selected.value.startsWith("choose:")) this.inbox.filter = selected.value;
    else {
      const kind = selected.value.slice(7);
      let values;
      if (kind === "repository") values = [...new Set(this.inbox.handoffs.map((handoff) => handoff.repository_id).filter(Boolean))];
      if (kind === "author") values = [...new Set(this.inbox.handoffs.flatMap((handoff) => [handoff.author_email || handoff.author || handoff.owner_id]).filter(Boolean))];
      if (kind === "branch") values = [...new Set(this.inbox.handoffs.map((handoff) => handoff.branch).filter(Boolean))];
      if (kind === "age") values = ["1", "7", "30", "90"];
      const value = await vscode.window.showQuickPick((values || []).map((entry) => ({
        label: kind === "age" ? `Last ${entry} day${entry === "1" ? "" : "s"}` : entry,
        value: `${kind}:${kind === "author" ? entry.toLowerCase() : entry}`
      })), { placeHolder: `Filter by ${kind}` });
      if (!value) return;
      this.inbox.filter = value.value;
    }
    this.inbox.refreshTree();
  }

  async sortInbox() {
    const options = [["Newest", "newest"], ["Oldest", "oldest"], ["Sender", "sender"], ["Expiry", "expiry"]];
    const selected = await vscode.window.showQuickPick(options.map(([label, value]) => ({ label, value })), { placeHolder: "Sort Handoff Inbox" });
    if (!selected) return;
    this.inbox.sort = selected.value;
    this.inbox.refreshTree();
  }

  async copyHandoffID(value) {
    const handoff = handoffFromCommand(value);
    const id = typeof handoff === "string" ? handoff : handoff?.id;
    if (!id) return;
    await vscode.env.clipboard.writeText(id);
    vscode.window.showInformationMessage(`Copied Handoff ID: ${id}`);
  }

  async copyHandoffDetails(value) {
    const handoff = handoffFromCommand(value);
    if (!handoff?.id) return;
    await vscode.env.clipboard.writeText(handoffInspection(handoff));
    vscode.window.showInformationMessage(`Copied details for Handoff ${handoff.id}.`);
  }

  async copyHandoffLink(value) {
    const handoff = handoffFromCommand(value);
    if (!handoff?.id) return;
    const link = `vscode://${this.context.extension.id}/handoff/${handoff.id}`;
    await vscode.env.clipboard.writeText(link);
    vscode.window.showInformationMessage(`Copied VS Code link for Handoff ${handoff.id}.`);
  }

  async handleUri(uri) {
    const match = /^\/handoff\/([0-9a-f]{12})$/i.exec(uri.path);
    if (!match) return;
    await vscode.commands.executeCommand("handoff.openInbox");
    if (this.inbox.state === "idle") await this.inbox.refresh();
    const handoff = this.inbox.handoffs.find((value) => value.id === match[1].toLowerCase()) || { id: match[1].toLowerCase() };
    await this.inspectInboxItem(handoff);
  }

  async deleteHandoff(value) {
    const handoff = handoffFromCommand(value);
    if (!handoff?.id) return;
    const repository = await this.compatibleRepository(handoff, value?.repository) || this.repositories.list()[0];
    if (!repository) return;
    const confirmation = await vscode.window.showWarningMessage(`Delete Handoff ${handoff.id} from the server?`, { modal: true }, "Delete Handoff");
    if (confirmation !== "Delete Handoff") return;
    await this.execute(["delete", handoff.id], { cwd: repository.rootUri.fsPath, json: false, timeoutMs: 30000 });
    await this.inbox.refresh();
  }

  async collaborationRepository(handoff, preferred) {
    return await this.compatibleRepository(handoff, preferred) || this.repositories.list()[0];
  }

  async lifecycleEvent(value, command, destructive = false) {
    const handoff = handoffFromCommand(value);
    if (!handoff?.id) return;
    const repository = await this.collaborationRepository(handoff, value?.repository);
    if (!repository) return;
    if (destructive) {
      const action = await vscode.window.showWarningMessage(`Revoke Handoff ${handoff.id}? Recipients will no longer be able to download it.`, { modal: true }, "Revoke Handoff");
      if (action !== "Revoke Handoff") return;
    }
    await this.execute([command, handoff.id], { cwd: repository.rootUri.fsPath, timeoutMs: 30000 });
    vscode.window.showInformationMessage(`Handoff ${handoff.id} updated: ${command}.`);
    await this.inbox.refresh();
  }

  async assignHandoff(value) {
    const handoff = handoffFromCommand(value);
    if (!handoff?.id) return;
    const target = await vscode.window.showInputBox({ prompt: "Assign this Handoff to a user ID or email", value: handoff.assigned_to || "", ignoreFocusOut: true });
    if (target === undefined) return;
    const repository = await this.collaborationRepository(handoff, value?.repository);
    if (!repository) return;
    await this.execute(["assign", "--target", target.trim(), handoff.id], { cwd: repository.rootUri.fsPath, timeoutMs: 30000 });
    await this.inbox.refresh();
  }

  async setHandoffExpiry(value) {
    const handoff = handoffFromCommand(value);
    if (!handoff?.id) return;
    const expiresAt = await vscode.window.showInputBox({
      prompt: "New Handoff expiry in RFC 3339 format",
      value: handoff.expires_at || new Date(Date.now() + 7 * 86400000).toISOString(),
      ignoreFocusOut: true,
      validateInput: (input) => Number.isFinite(Date.parse(input)) && Date.parse(input) > Date.now() ? undefined : "Enter a future date/time, for example 2026-08-20T12:00:00Z."
    });
    if (!expiresAt) return;
    const repository = await this.collaborationRepository(handoff, value?.repository);
    if (!repository) return;
    await this.execute(["expire", "--expires-at", new Date(expiresAt).toISOString(), handoff.id], { cwd: repository.rootUri.fsPath, timeoutMs: 30000 });
    vscode.window.showInformationMessage(`Handoff ${handoff.id} expiry updated.`);
    await this.inbox.refresh();
  }

  async commentOnHandoff(value) {
    const handoff = handoffFromCommand(value);
    if (!handoff?.id) return;
    const message = await vscode.window.showInputBox({ prompt: `Comment on Handoff ${handoff.id}`, ignoreFocusOut: true, validateInput: (text) => text.trim() ? undefined : "Enter a comment." });
    if (!message) return;
    const repository = await this.collaborationRepository(handoff, value?.repository);
    if (!repository) return;
    await this.execute(["comment", handoff.id, message.trim()], { cwd: repository.rootUri.fsPath, timeoutMs: 30000 });
    vscode.window.showInformationMessage(`Comment added to Handoff ${handoff.id}.`);
    await this.inbox.refresh();
  }

  async showComments(value) {
    const handoff = handoffFromCommand(value);
    if (!handoff?.id) return;
    const repository = await this.collaborationRepository(handoff, value?.repository);
    if (!repository) return;
    const result = await this.execute(["comments", handoff.id], { cwd: repository.rootUri.fsPath, timeoutMs: 30000 });
    const comments = result.data.comments || [];
    if (!comments.length) return vscode.window.showInformationMessage(`Handoff ${handoff.id} has no comments.`);
    /** @type {any} */
    const selected = await vscode.window.showQuickPick(comments.map((comment) => ({
      label: comment.author || comment.author_id,
      description: relativeTime(comment.created_at),
      detail: comment.message,
      comment
    })), { placeHolder: `Comments on ${handoff.message || handoff.id}` });
    if (selected) await vscode.env.clipboard.writeText(selected.comment.message);
  }

  async showAudit(value) {
    const handoff = handoffFromCommand(value);
    if (!handoff?.id) return;
    const repository = await this.collaborationRepository(handoff, value?.repository);
    if (!repository) return;
    const result = await this.execute(["audit", handoff.id], { cwd: repository.rootUri.fsPath, timeoutMs: 30000 });
    const events = result.data.events || [];
    if (!events.length) return vscode.window.showInformationMessage(`Handoff ${handoff.id} has no audit events.`);
    await vscode.window.showQuickPick(events.map((event) => ({
      label: event.action,
      description: `${event.actor || event.actor_id} · ${relativeTime(event.created_at)}`,
      detail: event.detail
    })), { placeHolder: `Audit history for ${handoff.id}` });
  }

  async configure() {
    const repository = this.repositories.list().length ? await this.repositories.choose("Select the repository for this server profile") : undefined;
    const name = await vscode.window.showInputBox({ prompt: "Profile name", placeHolder: "My team", ignoreFocusOut: true });
    if (!name) return;
    const server = await vscode.window.showInputBox({
      prompt: "Handoff server URL",
      placeHolder: "https://handoff.example.com",
      ignoreFocusOut: true,
      validateInput: (value) => value.startsWith("https://") || value.startsWith("http://localhost") || value.startsWith("http://127.0.0.1") ? undefined : "Enter an HTTPS URL (or localhost HTTP)."
    });
    if (!server) return;
    const token = await vscode.window.showInputBox({ prompt: "Handoff team token", password: true, ignoreFocusOut: true, validateInput: (value) => value.trim().length >= 32 ? undefined : "The token must be at least 32 characters." });
    if (!token) return;
    const cwd = repository?.rootUri.fsPath || vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
    await this.progress("Testing Handoff connection…", () => runHandoff(this.getBinary(), ["list", "--all", "--limit", "1"], {
      cwd,
      timeoutMs: 30000,
      env: { ...process.env, HANDOFF_SERVER: server.replace(/\/$/, ""), HANDOFF_TOKEN: token }
    }));
    const id = `${Date.now().toString(36)}-${name.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-|-$/g, "")}`;
    const profiles = this.context.globalState.get(profileStateKey, []).filter((profile) => profile.name !== name);
    profiles.push({ id, name, server: server.replace(/\/$/, "") });
    await this.context.globalState.update(profileStateKey, profiles);
    await this.context.secrets.store(`handoff.profile.${id}`, token);
    if (repository) await this.bindProfile(repository, id);
    else if (vscode.workspace.workspaceFolders?.length) await vscode.workspace.getConfiguration("handoff", vscode.workspace.workspaceFolders[0].uri).update("profile", id, vscode.ConfigurationTarget.WorkspaceFolder);
    vscode.window.showInformationMessage(`Handoff profile “${name}” connected to ${server}.`);
    await Promise.all(this.repositories.list().map((value) => value.refresh().catch(() => undefined)));
    await this.inbox.refresh().catch(() => undefined);
  }

  async testConnection() {
    const repository = await this.repositories.choose();
    if (!repository) return;
    await this.progress("Testing Handoff connection…", (signal) => this.execute(["list", "--all", "--limit", "1"], { cwd: repository.rootUri.fsPath, signal, timeoutMs: 30000 }));
    vscode.window.showInformationMessage(`Handoff connection is working for ${repositoryLabel(repository)}.`);
  }

  async selectProfile() {
    const repository = await this.repositories.choose("Select the repository whose Handoff server should change");
    if (!repository) return;
    const profiles = this.context.globalState.get(profileStateKey, []);
    if (!profiles.length) return this.configure();
    /** @type {any} */
    const selected = await vscode.window.showQuickPick(profiles.map((profile) => ({ label: profile.name, description: profile.server, profile })), { placeHolder: `Choose a server profile for ${repositoryLabel(repository)}` });
    if (!selected) return;
    await this.bindProfile(repository, selected.profile.id);
    await repository.refresh();
    await this.inbox.refresh();
    vscode.window.showInformationMessage(`${repositoryLabel(repository)} now uses Handoff profile “${selected.profile.name}”.`);
  }

  async bindProfile(repository, profileID) {
    const bindings = this.context.workspaceState.get(profileBindingsKey, {});
    bindings[repositoryKey(repository)] = profileID;
    await this.context.workspaceState.update(profileBindingsKey, bindings);
  }

  async copyDiagnostics() {
    const details = [
      `VS Code: ${vscode.version}`,
      `Remote: ${vscode.env.remoteName || "local"}`,
      `Platform: ${process.platform}/${process.arch}`,
      `Extension: ${this.context.extension.packageJSON.version}`,
      `Repositories: ${this.repositories.list().map((repository) => `${repositoryLabel(repository)} (${repository.info?.branch || "unknown"})`).join(", ") || "none"}`,
      `Last error: ${this.lastError ? `${this.lastError.code || "error"}: ${this.lastError.message}` : "none"}`
    ].join("\n");
    await vscode.env.clipboard.writeText(details);
    vscode.window.showInformationMessage("Copied redacted Handoff diagnostics.");
  }

  async offerOnboarding() {
    if (this.context.globalState.get("handoff.onboardingShown.v1")) return;
    await this.context.globalState.update("handoff.onboardingShown.v1", true);
    const action = await vscode.window.showInformationMessage(
      "Handoff is ready. Select files in Source Control, write a message, and create a Handoff without changing Git staging.",
      "Open Source Control",
      "Configure Server"
    );
    if (action === "Open Source Control") await vscode.commands.executeCommand("workbench.view.scm");
    if (action === "Configure Server") await vscode.commands.executeCommand("handoff.configure");
  }

  presentError(error) {
    if (error?.code === "cancelled") return;
    this.lastError = error;
    if (error?.code === "not_found") this.inbox?.refresh().catch(() => undefined);
    const message = error instanceof HandoffCommandError ? error.message : error?.message || "Handoff failed.";
    const signature = `${error?.code || "error"}:${message}`;
    if (this.lastErrorSignature === signature && Date.now() - (this.lastErrorShownAt || 0) < 10000) return;
    this.lastErrorSignature = signature;
    this.lastErrorShownAt = Date.now();
    const actions = [];
    if (["not_configured", "invalid_config"].includes(error?.code)) actions.push("Configure");
    if (error?.code === "conflict" || error?.recovery?.active) actions.push("Show Recovery");
    actions.push("Show Output");
    vscode.window.showErrorMessage(message, ...actions).then((action) => {
      if (action === "Configure") vscode.commands.executeCommand("handoff.configure");
      if (action === "Show Recovery") vscode.commands.executeCommand("handoff.showRecovery");
      if (action === "Show Output") this.output.show();
    });
  }
}

async function activate(context) {
  activeExtension = new HandoffExtension(context);
  await activeExtension.activate();
}

function deactivate() {
  activeExtension = undefined;
}

module.exports = { activate, deactivate };
