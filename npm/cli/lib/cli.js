"use strict";

const path = require("node:path");
const { spawn } = require("node:child_process");

const targets = Object.freeze({
  "darwin-arm64": Object.freeze({
    packageName: "@walid-baharwal/handoff-darwin-arm64",
    binaryName: "handoff",
  }),
  "darwin-x64": Object.freeze({
    packageName: "@walid-baharwal/handoff-darwin-x64",
    binaryName: "handoff",
  }),
  "linux-arm64": Object.freeze({
    packageName: "@walid-baharwal/handoff-linux-arm64",
    binaryName: "handoff",
  }),
  "linux-x64": Object.freeze({
    packageName: "@walid-baharwal/handoff-linux-x64",
    binaryName: "handoff",
  }),
  "win32-x64": Object.freeze({
    packageName: "@walid-baharwal/handoff-windows-x64",
    binaryName: "handoff.exe",
  }),
  "win32-arm64": Object.freeze({
    packageName: "@walid-baharwal/handoff-windows-arm64",
    binaryName: "handoff.exe",
  }),
});

function targetFor(platform = process.platform, architecture = process.arch) {
  const key = `${platform}-${architecture}`;
  const target = targets[key];
  if (target) {
    return target;
  }
  throw new Error(
    `Handoff does not provide an npm binary for ${platform}/${architecture}. ` +
      "Install a supported standalone binary from https://github.com/walid-baharwal/handoff/releases.",
  );
}

function resolveBinary(options = {}) {
  const target = targetFor(options.platform, options.architecture);
  const resolvePackage = options.resolvePackage || require.resolve;
  let packageJSON;
  try {
    packageJSON = resolvePackage(`${target.packageName}/package.json`);
  } catch (error) {
    throw new Error(
      `The native package ${target.packageName} is missing. ` +
        "Reinstall @walid-baharwal/handoff and make sure optional dependencies are enabled.",
      { cause: error },
    );
  }
  return path.join(path.dirname(packageJSON), "bin", target.binaryName);
}

function signalExitCode(signal) {
  const signalNumbers = { SIGHUP: 1, SIGINT: 2, SIGQUIT: 3, SIGKILL: 9, SIGTERM: 15 };
  return 128 + (signalNumbers[signal] || 1);
}

function run(options = {}) {
  const stderr = options.stderr || process.stderr;
  const setExitCode = options.setExitCode || ((code) => {
    process.exitCode = code;
  });
  let binary;
  try {
    binary = resolveBinary(options);
  } catch (error) {
    stderr.write(`handoff: ${error.message}\n`);
    setExitCode(1);
    return null;
  }

  const spawnProcess = options.spawn || spawn;
  let child;
  try {
    child = spawnProcess(binary, options.argv || process.argv.slice(2), {
      stdio: "inherit",
    });
  } catch (error) {
    stderr.write(`handoff: cannot start native binary: ${error.message}\n`);
    setExitCode(1);
    return null;
  }

  let completed = false;
  child.once("error", (error) => {
    if (completed) return;
    completed = true;
    stderr.write(`handoff: cannot start native binary: ${error.message}\n`);
    setExitCode(1);
  });
  child.once("exit", (code, signal) => {
    if (completed) return;
    completed = true;
    setExitCode(signal ? signalExitCode(signal) : (code ?? 1));
  });
  return child;
}

module.exports = { resolveBinary, run, signalExitCode, targetFor, targets };
