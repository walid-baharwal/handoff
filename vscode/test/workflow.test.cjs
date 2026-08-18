"use strict";

const assert = require("node:assert/strict");
const test = require("node:test");
const {
  buildPushArguments,
  changeFingerprint,
  changedFileChoices,
  excludedByPattern,
  groupChanges,
  selectedPaths,
  sensitiveFileReason,
  validateMessage
} = require("../lib/workflow");

test("builds selected-file push arguments without splitting paths", () => {
  assert.deepEqual(
    buildPushArguments("Share chat changes", {
      dryRun: true,
      paths: ["src/chat/new file.ts", "src/chat/deleted.ts"]
    }),
    ["push", "--dry-run", "-m", "Share chat changes", "src/chat/new file.ts", "src/chat/deleted.ts"]
  );
});

test("builds private recipient and team push arguments", () => {
  assert.deepEqual(buildPushArguments("Review", {
    recipients: ["saif@example.com", "walid"],
    team: "backend",
    private: true,
    paths: ["src/app.ts"]
  }), ["push", "-m", "Review", "--to", "saif@example.com", "--to", "walid", "--team", "backend", "--private", "src/app.ts"]);
});

test("provides legacy changed files as picker choices", () => {
  assert.deepEqual(changedFileChoices({ files: ["new.ts", "deleted.ts"] }), [
    { label: "new.ts", description: "modified", detail: undefined, path: "new.ts", paths: ["new.ts"], picked: true },
    { label: "deleted.ts", description: "modified", detail: undefined, path: "deleted.ts", paths: ["deleted.ts"], picked: true }
  ]);
});

test("keeps rename pairs together and groups a draft independently", () => {
  const changes = [
    { path: "new.ts", original_path: "old.ts", status: "renamed", supported: true },
    { path: "other.ts", status: "modified", supported: true },
    { path: "large.bin", status: "modified", supported: false, excluded_reason: "unsupported" }
  ];
  assert.deepEqual(selectedPaths([changes[0]]), ["old.ts", "new.ts"]);
  assert.deepEqual(groupChanges(changes, new Set(["new.ts"])), {
    included: [changes[0]],
    available: [changes[1]],
    excluded: [changes[2]]
  });
});

test("validates messages and detects sensitive or excluded paths", () => {
  assert.match(validateMessage(""), /Describe/);
  assert.equal(validateMessage("Useful context"), undefined);
  assert.equal(sensitiveFileReason("config/.env.local"), "environment file");
  assert.equal(sensitiveFileReason("keys/id_ed25519"), "SSH key");
  assert.equal(excludedByPattern("dist/app.js", ["dist/**"]), "dist/**");
  assert.equal(excludedByPattern("src/app.js", ["dist/**"]), undefined);
});

test("uses Git content IDs to detect same-size stale draft changes", () => {
  const original = { path: "src/app.js", status: "modified", size_bytes: 10, content_id: "one" };
  const changed = { ...original, content_id: "two" };
  assert.notEqual(changeFingerprint(original), changeFingerprint(changed));
  assert.equal(changeFingerprint({ path: "new.txt", status: "untracked", size_bytes: 3 }), "untracked::new.txt:3");
});
