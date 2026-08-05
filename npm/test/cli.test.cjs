"use strict";

const assert = require("node:assert/strict");
const { EventEmitter } = require("node:events");
const path = require("node:path");
const test = require("node:test");
const { resolveBinary, run, signalExitCode, targetFor } = require("../cli/lib/cli");

const expectedTargets = [
  ["linux", "x64", "@walid-baharwal/handoff-linux-x64", "handoff"],
  ["linux", "arm64", "@walid-baharwal/handoff-linux-arm64", "handoff"],
  ["darwin", "x64", "@walid-baharwal/handoff-darwin-x64", "handoff"],
  ["darwin", "arm64", "@walid-baharwal/handoff-darwin-arm64", "handoff"],
  ["win32", "x64", "@walid-baharwal/handoff-windows-x64", "handoff.exe"],
];

test("maps every release target to its native package", () => {
  for (const [platform, architecture, packageName, binaryName] of expectedTargets) {
    assert.deepEqual(targetFor(platform, architecture), { packageName, binaryName });
  }
});

test("rejects unsupported platforms with a standalone-download fallback", () => {
  assert.throws(() => targetFor("freebsd", "x64"), /standalone binary/);
});

test("resolves the binary from the installed optional dependency", () => {
  let requestedPackage;
  const binary = resolveBinary({
    platform: "win32",
    architecture: "x64",
    resolvePackage(specification) {
      requestedPackage = specification;
      return path.join("packages", "windows", "package.json");
    },
  });
  assert.equal(requestedPackage, "@walid-baharwal/handoff-windows-x64/package.json");
  assert.equal(binary, path.join("packages", "windows", "bin", "handoff.exe"));
});

test("forwards arguments, stdio, and the native exit status", () => {
  const child = new EventEmitter();
  let invocation;
  let exitCode;
  const result = run({
    platform: "linux",
    architecture: "x64",
    argv: ["push", "--dry-run"],
    resolvePackage: () => path.join("packages", "linux", "package.json"),
    spawn(binary, args, options) {
      invocation = { binary, args, options };
      return child;
    },
    setExitCode(code) {
      exitCode = code;
    },
  });
  assert.equal(result, child);
  assert.deepEqual(invocation, {
    binary: path.join("packages", "linux", "bin", "handoff"),
    args: ["push", "--dry-run"],
    options: { stdio: "inherit" },
  });
  child.emit("exit", 23, null);
  assert.equal(exitCode, 23);
});

test("reports a missing optional dependency without spawning", () => {
  let output = "";
  let exitCode;
  const result = run({
    platform: "linux",
    architecture: "arm64",
    resolvePackage() {
      throw new Error("not installed");
    },
    stderr: { write(value) { output += value; } },
    setExitCode(code) { exitCode = code; },
  });
  assert.equal(result, null);
  assert.equal(exitCode, 1);
  assert.match(output, /optional dependencies are enabled/);
});

test("converts common termination signals to conventional exit codes", () => {
  assert.equal(signalExitCode("SIGINT"), 130);
  assert.equal(signalExitCode("SIGTERM"), 143);
  assert.equal(signalExitCode("UNKNOWN"), 129);
});
