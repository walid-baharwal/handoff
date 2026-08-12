"use strict";

const assert = require("node:assert/strict");
const test = require("node:test");
const {
  handoffDescription,
  handoffFromCommand,
  handoffInspection,
  handoffLabel,
  handoffTooltip
} = require("../lib/inbox");

test("formats a concise inbox item from handoff metadata", () => {
  const handoff = {
    id: "abcdef123456",
    author: "Saif",
    branch: "feature/inbox",
    message: "Share inbox UI",
    file_count: 3,
    stored_at: "2026-08-06T10:00:00Z"
  };
  assert.equal(handoffLabel(handoff), "Share inbox UI");
  assert.equal(handoffDescription(handoff), "Saif - feature/inbox - 3 files");
  assert.match(handoffTooltip(handoff), /Handoff: abcdef123456/);
  assert.match(handoffTooltip(handoff), /Received: 2026-08-06T10:00:00Z/);
});

test("formats complete inspection details with changed paths", () => {
  const details = handoffInspection({
    id: "abcdef123456",
    author: "Saif",
    branch: "feature/chat",
    file_count: 2,
    files: ["src/chat/new file.ts", "src/chat/deleted.ts"]
  });
  assert.match(details, /ID: abcdef123456/);
  assert.match(details, /src\/chat\/new file\.ts/);
  assert.match(details, /src\/chat\/deleted\.ts/);
});

test("unwraps metadata passed through a tree item context command", () => {
  const handoff = { id: "abcdef123456" };
  assert.equal(handoffFromCommand({ handoff, id: "handoff:abcdef123456" }), handoff);
  assert.equal(handoffFromCommand(handoff), handoff);
});

test("uses clear fallbacks for incomplete inbox metadata", () => {
  const handoff = { file_count: 1 };
  assert.equal(handoffLabel(handoff), "Handoff changes");
  assert.equal(handoffDescription(handoff), "Unknown sender - unknown branch - 1 file");
  assert.match(handoffTooltip(handoff), /Handoff: unknown/);
});
