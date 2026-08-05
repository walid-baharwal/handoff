"use strict";

const { EventEmitter } = require("node:events");
const { mkdtempSync, writeFileSync } = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const test = require("node:test");
const assert = require("node:assert/strict");

const {
  HandoffCommandError,
  bundledBinaryPath,
  resolveBinary,
  runHandoff
} = require("../lib/cli");

function fakeSpawn({ stdout = "", stderr = "", exitCode = 0 }) {
  return (_binary, _args, options) => {
    const child = new EventEmitter();
    child.stdout = new EventEmitter();
    child.stderr = new EventEmitter();
    child.stdin = { end: (value) => { child.input = value; } };
    process.nextTick(() => {
      if (options.stdio[0] === "pipe") child.stdin.end = (value) => { child.input = value; };
      if (stdout) child.stdout.emit("data", Buffer.from(stdout));
      if (stderr) child.stderr.emit("data", Buffer.from(stderr));
      child.emit("close", exitCode);
    });
    return child;
  };
}

test("maps supported platforms to bundled binary paths", () => {
  assert.match(bundledBinaryPath("/extension", "linux", "x64"), /bin[\\/]handoff$/);
  assert.match(bundledBinaryPath("C:\\extension", "win32", "x64"), /bin[\\/]handoff\.exe$/);
  assert.throws(() => bundledBinaryPath("/extension", "freebsd", "x64"), /does not support/);
});

test("honors an explicit development binary path", () => {
  const directory = mkdtempSync(path.join(os.tmpdir(), "handoff-vscode-"));
  const binary = path.join(directory, "handoff");
  writeFileSync(binary, "binary");
  assert.equal(resolveBinary({ extensionPath: "/unused", configuredPath: binary }), binary);
});

test("adds JSON mode and parses a successful command", async () => {
  const result = await runHandoff("handoff", ["list"], {
    spawnCommand: fakeSpawn({ stdout: JSON.stringify({ schema_version: 1, command: "list", data: { handoffs: [] } }) })
  });
  assert.deepEqual(result.data.handoffs, []);
});

test("surfaces structured CLI failures", async () => {
  await assert.rejects(
    runHandoff("handoff", ["pull", "abcdef123456"], {
      spawnCommand: fakeSpawn({
        exitCode: 1,
        stderr: JSON.stringify({ error: { code: "conflict", message: "resolve conflicts", recovery: { active: true } } })
      })
    }),
    (error) => error instanceof HandoffCommandError && error.code === "conflict" && error.recovery.active
  );
});
