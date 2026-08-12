"use strict";

function textOr(value, fallback) {
  return typeof value === "string" && value.trim() ? value.trim() : fallback;
}

function fileLabel(count) {
  return `${count} file${count === 1 ? "" : "s"}`;
}

function handoffLabel(handoff) {
  return textOr(handoff.message, "Handoff changes");
}

function handoffDescription(handoff) {
  return `${textOr(handoff.author, "Unknown sender")} - ${textOr(handoff.branch, "unknown branch")} - ${fileLabel(handoff.file_count || 0)}`;
}

function handoffTooltip(handoff) {
  const lines = [
    `Handoff: ${textOr(handoff.id, "unknown")}`,
    `From: ${textOr(handoff.author, "Unknown sender")}`,
    `Branch: ${textOr(handoff.branch, "unknown branch")}`,
    `Files: ${fileLabel(handoff.file_count || 0)}`
  ];
  if (handoff.stored_at) lines.push(`Received: ${handoff.stored_at}`);
  return lines.join("\n");
}

function handoffInspection(handoff) {
  const lines = [
    `ID: ${textOr(handoff.id, "unknown")}`,
    `From: ${textOr(handoff.author, "Unknown sender")}`,
    `Branch: ${textOr(handoff.branch, "unknown branch")}`,
    `Files: ${fileLabel(handoff.file_count || 0)}`
  ];
  if (handoff.created_at) lines.push(`Created: ${handoff.created_at}`);
  if (handoff.files?.length) lines.push("", "Changed files:", ...handoff.files);
  if (handoff.files_truncated) lines.push("... additional changed files are not listed");
  return lines.join("\n");
}

function handoffFromCommand(value) {
  return value?.handoff || value;
}

module.exports = {
  handoffDescription,
  handoffFromCommand,
  handoffInspection,
  handoffLabel,
  handoffTooltip
};
