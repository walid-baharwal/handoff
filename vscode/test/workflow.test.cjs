"use strict";

const assert = require("node:assert/strict");
const test = require("node:test");
const { buildPushArguments, changedFileChoices } = require("../lib/workflow");

test("builds selected-file push arguments without splitting paths", () => {
  assert.deepEqual(
    buildPushArguments("Share chat changes", {
      dryRun: true,
      paths: ["src/chat/new file.ts", "src/chat/deleted.ts"]
    }),
    ["push", "--dry-run", "-m", "Share chat changes", "src/chat/new file.ts", "src/chat/deleted.ts"]
  );
});

test("marks every changed file as initially selected", () => {
  assert.deepEqual(changedFileChoices({ files: ["new.ts", "deleted.ts"] }), [
    { label: "new.ts", path: "new.ts", picked: true },
    { label: "deleted.ts", path: "deleted.ts", picked: true }
  ]);
});
