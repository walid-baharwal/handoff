"use strict";

const vscode = require("vscode");
const {
  HandoffCommandError,
  resolveBinary,
  runHandoff
} = require("./lib/cli");
const {
  handoffDescription,
  handoffFromCommand,
  handoffInspection,
  handoffLabel,
  handoffTooltip
} = require("./lib/inbox");
const { buildPushArguments, changedFileChoices } = require("./lib/workflow");

const tokenSecretKey = "handoff.teamToken";
let activeInbox;

async function workspaceRoot() {
  const folders = vscode.workspace.workspaceFolders || [];
  if (folders.length === 0) {
    throw new HandoffCommandError("Open a Git repository folder before using Handoff.", { code: "repository_required" });
  }
  if (folders.length === 1) return folders[0].uri.fsPath;
  const selected = await vscode.window.showQuickPick(
    folders.map((folder) => ({ label: folder.name, description: folder.uri.fsPath, folder })),
    { placeHolder: "Select the repository for this Handoff command" }
  );
  return selected?.folder.uri.fsPath;
}

function getBinary(context) {
  const configuredPath = vscode.workspace.getConfiguration("handoff").get("binaryPath", "");
  return resolveBinary({ extensionPath: context.extensionPath, configuredPath });
}

async function execute(context, args, options = {}) {
  return runHandoff(getBinary(context), args, {
    cwd: options.cwd,
    input: options.input,
    json: options.json
  });
}

function handoffDetail(handoff) {
  const author = handoff.author || "unknown sender";
  const branch = handoff.branch || "unknown branch";
  return `${author} · ${branch} · ${handoff.file_count} file${handoff.file_count === 1 ? "" : "s"}`;
}

class InboxProvider {
  constructor() {
    this.state = "idle";
    this.handoffs = [];
    this.recovery = undefined;
    this.error = undefined;
    this.cwd = undefined;
    this.changeEmitter = new vscode.EventEmitter();
    this.onDidChangeTreeData = this.changeEmitter.event;
  }

  dispose() {
    this.changeEmitter.dispose();
  }

  getTreeItem(item) {
    return item;
  }

  getChildren() {
    if (this.state === "idle") return [this.stateItem("Handoff inbox", "Open or refresh to load this repository.", "inbox", "handoff.refreshInbox")];
    if (this.state === "loading") return [this.stateItem("Loading Handoffs…", "", "sync~spin")];
    if (this.state === "error") {
      const notConfigured = this.error?.code === "not_configured" || this.error?.code === "invalid_config";
      return [this.stateItem(
        notConfigured ? "Configure Handoff" : "Inbox unavailable",
        this.error?.message || "Refresh to try again.",
        notConfigured ? "gear" : "error",
        notConfigured ? "handoff.configure" : "handoff.refreshInbox"
      )];
    }
    const items = [];
    if (this.recovery?.active) items.push(this.recoveryItem(this.recovery));
    items.push(...this.handoffs.map((handoff) => this.handoffItem(handoff)));
    if (items.length === 0) items.push(this.stateItem("Inbox is empty", "No Handoffs are available for this repository.", "inbox"));
    return items;
  }

  stateItem(label, description, icon, command) {
    const item = new vscode.TreeItem(label);
    item.description = description;
    item.iconPath = new vscode.ThemeIcon(icon);
    item.contextValue = "handoff.inboxState";
    if (command) item.command = { command, title: label };
    return item;
  }

  recoveryItem(recovery) {
    const item = new vscode.TreeItem("Recovery required");
    item.id = `handoff:recovery:${recovery.handoff_id}`;
    item.description = `${recovery.handoff_id} · ${recovery.stage.replaceAll("_", " ")}`;
    item.tooltip = recovery.conflicted_files.length
      ? `Conflicted files:\n${recovery.conflicted_files.join("\n")}`
      : "Open recovery status for available actions.";
    item.iconPath = new vscode.ThemeIcon("warning");
    item.contextValue = "handoff.recoveryItem";
    item.command = {
      command: "handoff.showRecovery",
      title: "Show Recovery Status",
      arguments: [this.cwd]
    };
    return item;
  }

  handoffItem(handoff) {
    const item = new vscode.TreeItem(handoffLabel(handoff));
    item.id = `handoff:${handoff.id}`;
    item.handoff = handoff;
    item.cwd = this.cwd;
    item.description = handoffDescription(handoff);
    item.tooltip = handoffTooltip(handoff);
    item.iconPath = new vscode.ThemeIcon("git-pull-request");
    item.contextValue = "handoff.inboxItem";
    item.command = {
      command: "handoff.inspectInboxItem",
      title: "Inspect Handoff",
      arguments: [handoff, this.cwd]
    };
    return item;
  }

  async refresh(context, cwd) {
    this.state = "loading";
    this.changeEmitter.fire(undefined);
    try {
      const root = cwd || await workspaceRoot();
      if (!root) {
        this.state = "idle";
        return undefined;
      }
      const [inbox, status] = await Promise.all([
        execute(context, ["list"], { cwd: root }),
        execute(context, ["status"], { cwd: root })
      ]);
      this.cwd = root;
      this.handoffs = inbox.data.handoffs;
      this.recovery = status.data.recovery;
      this.error = undefined;
      this.state = "ready";
      return this.handoffs.length;
    } catch (error) {
      this.error = error;
      this.state = "error";
      throw error;
    } finally {
      this.changeEmitter.fire(undefined);
    }
  }
}

async function refreshInbox(context, cwd, notify = false) {
  if (!activeInbox) return undefined;
  const count = await activeInbox.refresh(context, cwd);
  if (notify && count !== undefined) vscode.window.showInformationMessage(`Handoff inbox refreshed: ${count} available.`);
  return count;
}

async function refreshInboxQuietly(context, cwd) {
  try {
    await refreshInbox(context, cwd);
  } catch {
    // The inbox shows its own retry/configuration state; keep the primary operation result.
  }
}

async function configure(context) {
  const server = await vscode.window.showInputBox({
    prompt: "Handoff server URL",
    placeHolder: "https://handoff.example.com",
    ignoreFocusOut: true,
    validateInput: (value) => value.startsWith("https://") || value.startsWith("http://localhost") || value.startsWith("http://127.0.0.1")
      ? undefined
      : "Enter an HTTPS URL (or localhost HTTP)."
  });
  if (!server) return;
  const token = await vscode.window.showInputBox({
    prompt: "Handoff team token",
    password: true,
    ignoreFocusOut: true,
    validateInput: (value) => value.trim().length >= 32 ? undefined : "The team token must be at least 32 characters."
  });
  if (!token) return;
  await execute(context, ["setup", "--server", server, "--token-stdin"], { input: `${token}\n`, json: false });
  await context.secrets.store(tokenSecretKey, token);
  vscode.window.showInformationMessage("Handoff server configured.");
  if (vscode.workspace.workspaceFolders?.length) await refreshInboxQuietly(context);
}

async function chooseInbox(context, cwd) {
  const result = await execute(context, ["list"], { cwd });
  const handoffs = result.data.handoffs;
  if (handoffs.length === 0) {
    vscode.window.showInformationMessage("No Handoffs are available for this repository.");
    return undefined;
  }
  return vscode.window.showQuickPick(handoffs.map((handoff) => ({
    label: handoff.message || "Handoff changes",
    description: handoffDetail(handoff),
    detail: `${handoff.id} · ${handoff.files.slice(0, 4).join(", ") || "no file summary"}`,
    handoff
  })), { placeHolder: "Select a Handoff" });
}

async function pull(context, id, cwd) {
  let result;
  try {
    result = await execute(context, ["pull", "--yes", id], { cwd });
  } catch (error) {
    await refreshInboxQuietly(context, cwd);
    throw error;
  }
  vscode.window.showInformationMessage(`Handoff ${result.data.handoff.id} applied as local, uncommitted changes.`);
  await refreshInboxQuietly(context, cwd);
}

async function openInbox(context) {
  const cwd = await workspaceRoot();
  if (!cwd) return;
  const choice = await chooseInbox(context, cwd);
  if (!choice) return;
  await inspectHandoff(context, choice.handoff.id, cwd);
}

async function inspectInboxItem(context, handoff, cwd) {
  const value = handoffFromCommand(handoff);
  const root = cwd || handoff?.cwd || await workspaceRoot();
  if (!root || !value?.id) return;
  await inspectHandoff(context, value.id, root);
}

async function pullInboxItem(context, handoff, cwd) {
  const value = handoffFromCommand(handoff);
  const root = cwd || handoff?.cwd || await workspaceRoot();
  if (!root || !value?.id) return;
  await inspectHandoff(context, value.id, root);
}

async function copyHandoffID(_context, handoff) {
  const value = handoffFromCommand(handoff);
  const id = typeof value === "string" ? value : value?.id;
  if (!id) return;
  await vscode.env.clipboard.writeText(id);
  vscode.window.showInformationMessage(`Copied Handoff ID: ${id}`);
}

async function inspectHandoff(context, id, cwd) {
  const inspected = await execute(context, ["inspect", id], { cwd });
  const handoff = inspected.data.handoff;
  const action = await vscode.window.showWarningMessage(
    `${handoff.author || "Unknown sender"}: ${handoff.message || "Handoff changes"}`,
    { modal: true, detail: handoffInspection(handoff) },
    "Pull",
    "Copy ID"
  );
  if (action === "Copy ID") await copyHandoffID(context, handoff);
  if (action === "Pull") await pull(context, handoff.id, cwd);
}

async function promptPushMessage() {
  return vscode.window.showInputBox({
    prompt: "Describe the changes to share",
    placeHolder: "Backend invoice changes",
    ignoreFocusOut: true
  });
}

async function pushChanges(context, selectFiles = false) {
  const cwd = await workspaceRoot();
  if (!cwd) return;
  const message = await promptPushMessage();
  if (message === undefined) return;
  const preview = await execute(context, buildPushArguments(message, { dryRun: true }), { cwd });
  const handoff = preview.data.handoff;
  let paths = [];
  if (selectFiles) {
    if (handoff.files_truncated) {
      throw new HandoffCommandError(
        "This change set is too large for the VS Code file picker. Use Push Changes to share everything or select paths with the CLI.",
        { code: "selection_truncated" }
      );
    }
    const selected = await vscode.window.showQuickPick(changedFileChoices(handoff), {
      canPickMany: true,
      ignoreFocusOut: true,
      placeHolder: "Select changed files to share",
      title: "Handoff: Push Selected Changes"
    });
    if (selected === undefined) return;
    if (selected.length === 0) {
      vscode.window.showInformationMessage("Select at least one changed file to create a Handoff.");
      return;
    }
    paths = selected.map((item) => item.path);
  }
  const fileCount = selectFiles ? paths.length : handoff.file_count;
  const confirmation = await vscode.window.showWarningMessage(
    `Share ${fileCount} changed file${fileCount === 1 ? "" : "s"}?`,
    { modal: true, detail: selectFiles ? paths.join("\n") : "All changed files in this repository will be included." },
    "Push Changes"
  );
  if (confirmation !== "Push Changes") return;
  const result = await execute(context, buildPushArguments(message, { paths }), { cwd });
  const id = result.data.handoff.id;
  const action = await vscode.window.showInformationMessage(`Handoff uploaded: ${id}`, "Copy ID");
  if (action === "Copy ID") await copyHandoffID(context, id);
  await refreshInboxQuietly(context, cwd);
}

async function pullHandoff(context) {
  const cwd = await workspaceRoot();
  if (!cwd) return;
  const id = await vscode.window.showInputBox({
    prompt: "Handoff ID to pull",
    placeHolder: "abcdef123456",
    ignoreFocusOut: true,
    validateInput: (value) => /^[0-9a-f]{12}$/i.test(value) ? undefined : "Enter a 12-character Handoff ID."
  });
  if (!id) return;
  await inspectHandoff(context, id, cwd);
}

async function showRecovery(context, knownCwd) {
  const cwd = knownCwd || await workspaceRoot();
  if (!cwd) return;
  const result = await execute(context, ["status"], { cwd });
  const recovery = result.data.recovery;
  if (!recovery.active) {
    vscode.window.showInformationMessage("No Handoff recovery is active.");
    return;
  }
  const actions = ["Abort Recovery"];
  if (recovery.can_continue) actions.unshift("Continue Recovery");
  const selected = await vscode.window.showWarningMessage(
    `Handoff ${recovery.handoff_id}: ${recovery.stage.replaceAll("_", " ")}${recovery.conflicted_files.length ? ` (${recovery.conflicted_files.join(", ")})` : ""}`,
    ...actions
  );
  if (selected === "Continue Recovery") await continueRecovery(context, recovery.handoff_id, cwd);
  if (selected === "Abort Recovery") await abortRecovery(context, recovery.handoff_id, cwd);
}

async function recoveryID(context, cwd) {
  const result = await execute(context, ["status"], { cwd });
  if (!result.data.recovery.active) {
    vscode.window.showInformationMessage("No Handoff recovery is active.");
    return undefined;
  }
  return result.data.recovery.handoff_id;
}

async function continueRecovery(context, knownID, knownCwd) {
  const cwd = knownCwd || await workspaceRoot();
  if (!cwd) return;
  const id = knownID || await recoveryID(context, cwd);
  if (!id) return;
  await execute(context, ["continue", id], { cwd, json: false });
  vscode.window.showInformationMessage(`Handoff ${id} recovery completed.`);
  await refreshInboxQuietly(context, cwd);
}

async function abortRecovery(context, knownID, knownCwd) {
  const cwd = knownCwd || await workspaceRoot();
  if (!cwd) return;
  const id = knownID || await recoveryID(context, cwd);
  if (!id) return;
  const confirmation = await vscode.window.showWarningMessage(
    `Abort Handoff ${id} and restore the original local state?`,
    { modal: true },
    "Abort Recovery"
  );
  if (confirmation !== "Abort Recovery") return;
  await execute(context, ["abort", id], { cwd, json: false });
  vscode.window.showInformationMessage(`Handoff ${id} was aborted and local changes were restored.`);
  await refreshInboxQuietly(context, cwd);
}

function presentError(error) {
  if (!(error instanceof HandoffCommandError)) {
    vscode.window.showErrorMessage(error.message || "Handoff failed.");
    return;
  }
  const actions = [];
  if (error.code === "conflict" || error.recovery?.active) actions.push("Show Recovery");
  vscode.window.showErrorMessage(error.message, ...actions).then((action) => {
    if (action === "Show Recovery") vscode.commands.executeCommand("handoff.showRecovery");
  });
}

function register(context, command, callback) {
  context.subscriptions.push(vscode.commands.registerCommand(command, (...args) => callback(context, ...args).catch(presentError)));
}

function activate(context) {
  activeInbox = new InboxProvider();
  context.subscriptions.push(activeInbox);
  const inboxView = vscode.window.createTreeView("handoff.inbox", { treeDataProvider: activeInbox });
  context.subscriptions.push(inboxView);
  context.subscriptions.push(inboxView.onDidChangeVisibility(({ visible }) => {
    if (visible && activeInbox.state === "idle") refreshInbox(context).catch(presentError);
  }));
  if (inboxView.visible) refreshInbox(context).catch(presentError);
  register(context, "handoff.configure", configure);
  register(context, "handoff.openInbox", openInbox);
  register(context, "handoff.refreshInbox", (extensionContext) => refreshInbox(extensionContext, undefined, true));
  register(context, "handoff.inspectInboxItem", inspectInboxItem);
  register(context, "handoff.pullInboxItem", pullInboxItem);
  register(context, "handoff.copyHandoffID", copyHandoffID);
  register(context, "handoff.pushChanges", (extensionContext) => pushChanges(extensionContext, false));
  register(context, "handoff.pushSelectedChanges", (extensionContext) => pushChanges(extensionContext, true));
  register(context, "handoff.pullHandoff", pullHandoff);
  register(context, "handoff.showRecovery", showRecovery);
  register(context, "handoff.continueRecovery", continueRecovery);
  register(context, "handoff.abortRecovery", abortRecovery);
}

function deactivate() {}

module.exports = { activate, deactivate };
