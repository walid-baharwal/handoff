"use strict";

const { spawn } = require("node:child_process");
const { existsSync } = require("node:fs");
const path = require("node:path");

const supportedPlatforms = Object.freeze({
  "linux-x64": "handoff",
  "linux-arm64": "handoff",
  "darwin-x64": "handoff",
  "darwin-arm64": "handoff",
  "win32-x64": "handoff.exe"
});

class HandoffCommandError extends Error {
  constructor(message, details = {}) {
    super(message);
    this.name = "HandoffCommandError";
    this.code = details.code || "operation_failed";
    this.recovery = details.recovery;
    this.stderr = details.stderr || "";
    this.exitCode = details.exitCode;
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

function resolveBinary({ extensionPath, configuredPath = "", platform, architecture }) {
  const candidate = configuredPath.trim() || bundledBinaryPath(extensionPath, platform, architecture);
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
    const child = spawnCommand(binary, jsonOutput ? [...args, "--json"] : args, {
      cwd,
      windowsHide: true,
      stdio: [input === undefined ? "ignore" : "pipe", "pipe", "pipe"]
    });
    let stdout = "";
    let stderr = "";
    child.stdout.on("data", (chunk) => { stdout += chunk; });
    child.stderr.on("data", (chunk) => { stderr += chunk; });
    child.on("error", (error) => reject(new HandoffCommandError(error.message, { code: "spawn_failed" })));
    child.on("close", (exitCode) => {
      if (exitCode === 0) {
		if (!jsonOutput) {
			resolve(undefined);
			return;
		}
        try {
          resolve(parseJSON(stdout, "stdout"));
        } catch (error) {
          reject(error);
        }
        return;
      }
		if (!jsonOutput) {
			reject(new HandoffCommandError(stderr.trim() || "Handoff command failed.", { exitCode, stderr }));
			return;
		}
      let response;
      try {
        response = parseJSON(stderr, "stderr");
      } catch (error) {
        reject(error);
        return;
      }
      const details = response.error || {};
      reject(new HandoffCommandError(details.message || "Handoff command failed.", {
        code: details.code,
        recovery: details.recovery,
        stderr,
        exitCode
      }));
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
