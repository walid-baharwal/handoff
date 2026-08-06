"use strict";

const vscode = require("vscode");
const {
  HandoffCommandError,
  resolveBinary,
  runHandoff
} = require("./lib/cli");

const tokenSecretKey = "handoff.teamToken";

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
  const result = await execute(context, ["pull", "--yes", id], { cwd });
  vscode.window.showInformationMessage(`Handoff ${result.data.handoff.id} applied as local, uncommitted changes.`);
}

async function openInbox(context) {
  const cwd = await workspaceRoot();
  if (!cwd) return;
  const choice = await chooseInbox(context, cwd);
  if (!choice) return;
  const inspected = await execute(context, ["inspect", choice.handoff.id], { cwd });
  const handoff = inspected.data.handoff;
  const action = await vscode.window.showInformationMessage(
    `${handoff.author || "Unknown sender"}: ${handoff.message || "Handoff changes"} (${handoff.file_count} files)`,
    "Pull"
  );
  if (action === "Pull") await pull(context, handoff.id, cwd);
}

async function pushChanges(context) {
  const cwd = await workspaceRoot();
  if (!cwd) return;
  const message = await vscode.window.showInputBox({
    prompt: "Describe the changes to share",
    placeHolder: "Backend invoice changes",
    ignoreFocusOut: true
  });
  if (message === undefined) return;
  const preview = await execute(context, ["push", "--dry-run", "-m", message], { cwd });
  const handoff = preview.data.handoff;
  const confirmation = await vscode.window.showInformationMessage(
    `Share ${handoff.file_count} changed file${handoff.file_count === 1 ? "" : "s"}?`,
    { modal: true },
    "Push Changes"
  );
  if (confirmation !== "Push Changes") return;
  const result = await execute(context, ["push", "-m", message], { cwd });
  vscode.window.showInformationMessage(`Handoff uploaded: ${result.data.handoff.id}`);
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
  const inspected = await execute(context, ["inspect", id], { cwd });
  const handoff = inspected.data.handoff;
  const confirmation = await vscode.window.showWarningMessage(
    `Apply Handoff ${id} from ${handoff.author || "unknown sender"} (${handoff.file_count} files)?`,
    { modal: true },
    "Pull"
  );
  if (confirmation === "Pull") await pull(context, id, cwd);
}

async function showRecovery(context) {
  const cwd = await workspaceRoot();
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
  context.subscriptions.push(vscode.commands.registerCommand(command, () => callback(context).catch(presentError)));
}

function activate(context) {
  register(context, "handoff.configure", configure);
  register(context, "handoff.openInbox", openInbox);
  register(context, "handoff.pushChanges", pushChanges);
  register(context, "handoff.pullHandoff", pullHandoff);
  register(context, "handoff.showRecovery", showRecovery);
  register(context, "handoff.continueRecovery", continueRecovery);
  register(context, "handoff.abortRecovery", abortRecovery);
}

function deactivate() {}

module.exports = { activate, deactivate };
