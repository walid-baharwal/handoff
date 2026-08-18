"use strict";

const { spawn } = require("node:child_process");
const { existsSync } = require("node:fs");
const path = require("node:path");

const supportedPlatforms = Object.freeze({
  "linux-x64": "handoff",
  "linux-arm64": "handoff",
  "darwin-x64": "handoff",
  "darwin-arm64": "handoff",
  "win32-x64": "handoff.exe",
  "win32-arm64": "handoff.exe"
});

class HandoffCommandError extends Error {
  constructor(message, details = {}) {
    super(message);
    this.name = "HandoffCommandError";
    this.code = details.code || "operation_failed";
    this.recovery = details.recovery;
    this.stderr = details.stderr || "";
    this.exitCode = details.exitCode;
    this.retryable = details.retryable || false;
  }
}

function platformKey(platform = process.platform, architecture = process.arch) {
  return `${platform}-${architecture}`;
}

function bundledBinaryPath(extensionPath, platform = process.platform, architecture = process.arch) {
  const binary = supportedPlatforms[platformKey(platform, architecture)];
  if (!binary) {
    throw new HandoffCommandError(
      `Handoff does not support ${platform}/${architecture} in VS Code yet.`,
      { code: "unsupported_platform" }
    );
  }
  return path.join(extensionPath, "bin", binary);
}

/** @param {{extensionPath: string, configuredPath?: string, platform?: NodeJS.Platform, architecture?: NodeJS.Architecture}} options */
function resolveBinary({ extensionPath, configuredPath = "", platform, architecture }) {
  const candidate = configuredPath.trim() || bundledBinaryPath(extensionPath, platform || process.platform, architecture || process.arch);
  if (!existsSync(candidate)) {
    throw new HandoffCommandError(
      `The Handoff binary was not found at ${candidate}. Reinstall the extension or set handoff.binaryPath for development.`,
      { code: "binary_missing" }
    );
  }
  return candidate;
}

function parseJSON(value, stream) {
  try {
    return JSON.parse(value);
  } catch {
    throw new HandoffCommandError(
      `Handoff returned invalid JSON on ${stream}. Update the Handoff extension and binary together.`,
      { code: "invalid_response", stderr: value }
    );
  }
}

function runHandoff(binary, args, options = {}) {
  const spawnCommand = options.spawnCommand || spawn;
  const cwd = options.cwd;
  const input = options.input;
  const jsonOutput = options.json !== false;
  return new Promise((resolve, reject) => {
    const commandArgs = jsonOutput ? [args[0], "--json", ...args.slice(1)] : args;
    const child = spawnCommand(binary, commandArgs, {
      cwd,
      windowsHide: true,
      stdio: [input === undefined ? "ignore" : "pipe", "pipe", "pipe"],
      env: options.env || process.env
    });
    let settled = false;
    const finish = (callback) => {
      if (settled) return;
      settled = true;
      if (timer) clearTimeout(timer);
      options.signal?.removeEventListener("abort", abort);
      callback();
    };
    const abort = () => {
      child.kill();
      finish(() => reject(new HandoffCommandError("Handoff command was cancelled.", { code: "cancelled" })));
    };
    const timer = options.timeoutMs > 0 ? setTimeout(() => {
      child.kill();
      finish(() => reject(new HandoffCommandError("Handoff command timed out.", { code: "timeout", retryable: true })));
    }, options.timeoutMs) : undefined;
    options.signal?.addEventListener("abort", abort, { once: true });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (chunk) => { stdout += chunk; });
    child.stderr.on("data", (chunk) => { stderr += chunk; });
    child.on("error", (error) => {
      const denied = error.code === "EACCES" || error.code === "EPERM";
      const message = denied
        ? `The Handoff binary cannot be executed (${error.message}). Check file permissions or operating-system quarantine settings.`
        : error.message;
      finish(() => reject(new HandoffCommandError(message, { code: denied ? "binary_not_executable" : "spawn_failed" })));
    });
    child.on("close", (exitCode) => {
      if (settled) return;
      if (exitCode === 0) {
        if (!jsonOutput) {
          finish(() => resolve(stdout.trim()));
          return;
        }
        try {
          const response = parseJSON(stdout, "stdout");
          if (![1, 2].includes(response.schema_version)) {
            throw new HandoffCommandError(
              `Handoff returned unsupported integration schema ${response.schema_version}. Update the extension and binary together.`,
              { code: "unsupported_schema" }
            );
          }
          finish(() => resolve(response));
        } catch (error) {
          finish(() => reject(error));
        }
        return;
      }
      if (!jsonOutput) {
        finish(() => reject(new HandoffCommandError(stderr.trim() || "Handoff command failed.", { exitCode, stderr })));
        return;
      }
      let response;
      try {
        response = parseJSON(stderr, "stderr");
      } catch (error) {
        finish(() => reject(error));
        return;
      }
      const details = response.error || {};
      finish(() => reject(new HandoffCommandError(details.message || "Handoff command failed.", {
        code: details.code,
        recovery: details.recovery,
        stderr,
        exitCode,
        retryable: ["server_unavailable", "timeout"].includes(details.code)
      })));
    });
    if (input !== undefined) {
      child.stdin.end(input);
    }
  });
}

module.exports = {
  HandoffCommandError,
  bundledBinaryPath,
  platformKey,
  resolveBinary,
  runHandoff,
  supportedPlatforms
};
