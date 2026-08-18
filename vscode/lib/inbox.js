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
  if (handoff.team) lines.push(`Team: ${handoff.team}`);
  if (handoff.recipients?.length) lines.push(`Recipients: ${handoff.recipients.join(", ")}`);
  if (handoff.assigned_to) lines.push(`Assigned: ${handoff.assigned_to}`);
  if (handoff.lifecycle) lines.push(`Status: ${handoff.lifecycle}`);
  if (handoff.acknowledged_by?.length) lines.push(`Acknowledged by: ${handoff.acknowledged_by.join(", ")}`);
  if (handoff.applied_by?.length) lines.push(`Applied by: ${handoff.applied_by.join(", ")}`);
  return lines.join("\n");
}

function handoffInspection(handoff) {
  const lines = [
    `ID: ${textOr(handoff.id, "unknown")}`,
    `From: ${textOr(handoff.author, "Unknown sender")}`,
    `Repository: ${textOr(handoff.project, handoff.repository_id || "unknown repository")}`,
    `Branch: ${textOr(handoff.branch, "unknown branch")}`,
    `Files: ${fileLabel(handoff.file_count || 0)}`
  ];
  if (handoff.base_commit) lines.push(`Base commit: ${handoff.base_commit}`);
  if (handoff.package_bytes) lines.push(`Package size: ${handoff.package_bytes.toLocaleString()} bytes`);
  if (handoff.created_at) lines.push(`Created: ${handoff.created_at}`);
  if (handoff.expires_at) lines.push(`Expires: ${handoff.expires_at}`);
  if (handoff.team) lines.push(`Team: ${handoff.team}`);
  if (handoff.recipients?.length) lines.push(`Recipients: ${handoff.recipients.join(", ")}`);
  if (handoff.assigned_to) lines.push(`Assigned to: ${handoff.assigned_to}`);
  if (handoff.lifecycle) lines.push(`Status: ${handoff.lifecycle}`);
  if (handoff.comments_count) lines.push(`Comments: ${handoff.comments_count}`);
  if (handoff.acknowledged_by?.length) lines.push(`Acknowledged by: ${handoff.acknowledged_by.join(", ")}`);
  if (handoff.applied_by?.length) lines.push(`Applied by: ${handoff.applied_by.join(", ")}`);
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
