"use strict";

const assert = require("node:assert/strict");
const test = require("node:test");
const {
  handoffDescription,
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

test("uses clear fallbacks for incomplete inbox metadata", () => {
  const handoff = { file_count: 1 };
  assert.equal(handoffLabel(handoff), "Handoff changes");
  assert.equal(handoffDescription(handoff), "Unknown sender - unknown branch - 1 file");
  assert.match(handoffTooltip(handoff), /Handoff: unknown/);
});
