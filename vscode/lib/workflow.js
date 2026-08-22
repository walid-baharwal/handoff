"use strict";

const path = require("node:path");

function buildPushArguments(message, { dryRun = false, paths = [], recipients = [], team = "", private: privateHandoff = false } = {}) {
  const args = ["push"];
  if (dryRun) args.push("--dry-run");
  args.push("-m", message);
  for (const recipient of recipients) args.push("--to", recipient);
  if (team) args.push("--team", team);
  if (privateHandoff) args.push("--private");
  args.push(...paths);
  return args;
}

function changePaths(change) {
  return [...new Set([change.original_path, change.path].filter(Boolean))];
}

function changeFingerprint(change) {
  return change.content_id || [change.status, change.original_path || "", change.path, change.size_bytes || 0].join(":");
}

function selectedPaths(changes) {
  return [...new Set(changes.flatMap(changePaths))];
}

function changedFileChoices(handoff) {
  const changes = handoff.changes?.length
    ? handoff.changes
    : (handoff.files || []).map((file) => ({ path: file, status: "modified", supported: true }));
  return changes.map((change) => ({
    label: change.path,
    description: change.status,
    detail: change.original_path ? `Renamed from ${change.original_path}` : undefined,
    path: change.path,
    paths: changePaths(change),
    picked: true
  }));
}

function groupChanges(changes, includedPaths) {
  const included = [];
  const available = [];
  const excluded = [];
  for (const change of changes || []) {
    if (change.supported === false || change.excluded_reason) excluded.push(change);
    else if (includedPaths.has(change.path)) included.push(change);
    else available.push(change);
  }
  return { included, available, excluded };
}

function validateMessage(value) {
  const message = String(value || "").trim();
  if (!message) return "Describe the changes before creating the Handoff.";
  if (message.length > 500) return "The message cannot exceed 500 characters.";
  return undefined;
}

function sensitiveFileReason(file) {
  const normalized = String(file || "").replaceAll("\\", "/").toLowerCase();
  const name = path.posix.basename(normalized);
  if (name === ".env" || name.startsWith(".env.")) return "environment file";
  if (/^(id_rsa|id_dsa|id_ecdsa|id_ed25519)(\.pub)?$/.test(name)) return "SSH key";
  if (/\.(pem|p12|pfx|key|keystore|jks)$/.test(name)) return "key or certificate";
  if (/(credential|credentials|secret|secrets|token|tokens|password|passwd)/.test(name)) return "possibly sensitive file";
  return undefined;
}

function globToRegExp(glob) {
  let source = "^";
  const normalized = String(glob || "").replaceAll("\\", "/");
  for (let index = 0; index < normalized.length; index++) {
    const character = normalized[index];
    if (character === "*") {
      if (normalized[index + 1] === "*") {
        source += ".*";
        index++;
      } else source += "[^/]*";
    } else if (character === "?") source += "[^/]";
    else source += character.replace(/[|\\{}()[\]^$+?.]/g, "\\$&");
  }
  return new RegExp(`${source}$`, "i");
}

function excludedByPattern(file, patterns) {
  const normalized = String(file || "").replaceAll("\\", "/");
  return (patterns || []).find((pattern) => globToRegExp(pattern).test(normalized));
}

module.exports = {
  buildPushArguments,
  changeFingerprint,
  changePaths,
  changedFileChoices,
  excludedByPattern,
  globToRegExp,
  groupChanges,
  selectedPaths,
  sensitiveFileReason,
  validateMessage
};
