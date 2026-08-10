"use strict";

function buildPushArguments(message, { dryRun = false, paths = [] } = {}) {
  const args = ["push"];
  if (dryRun) args.push("--dry-run");
  args.push("-m", message, ...paths);
  return args;
}

function changedFileChoices(handoff) {
  return (handoff.files || []).map((file) => ({ label: file, path: file, picked: true }));
}

module.exports = { buildPushArguments, changedFileChoices };
