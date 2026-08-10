import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import path from "node:path";
import test from "node:test";
import { parseArguments, platforms, validateVersion, vsceInvocation } from "../scripts/package-vsix.mjs";

test("maps every release binary to a VS Code target", () => {
  assert.deepEqual(platforms.map((platform) => platform.target), [
    "linux-x64",
    "linux-arm64",
    "darwin-x64",
    "darwin-arm64",
    "win32-x64"
  ]);
  assert.equal(new Set(platforms.map((platform) => platform.sourceBinary)).size, platforms.length);
});

test("accepts release versions and parses packaging arguments", () => {
  validateVersion("1.2.3");
  validateVersion("1.2.3-rc.1");
  assert.throws(() => validateVersion("v1.2.3"), /invalid extension version/);
  assert.deepEqual(parseArguments(["--version", "1.2.3", "--binaries", "dist", "--output", "vscode/dist"]), {
    version: "1.2.3",
    binariesDirectory: path.resolve("dist"),
    outputDirectory: path.resolve("vscode/dist")
  });
  assert.throws(() => parseArguments(["--version", "1.2.3"]), /usage/);
});

test("extension manifest whitelists only runtime files", async () => {
  const manifest = JSON.parse(await readFile(new URL("../package.json", import.meta.url), "utf8"));
  assert.deepEqual(manifest.files, ["extension.js", "lib/**", "bin/**", "resources/**", "README.md", "LICENSE", "CHANGELOG.md"]);
});

test("extension registers the native Handoff inbox view", async () => {
  const manifest = JSON.parse(await readFile(new URL("../package.json", import.meta.url), "utf8"));
  assert.deepEqual(manifest.activationEvents, ["onView:handoff.inbox"]);
  assert.equal(manifest.contributes.viewsContainers.activitybar[0].id, "handoff");
  assert.equal(manifest.contributes.views.handoff[0].id, "handoff.inbox");
});

test("invokes VSCE through Node without shell path parsing", () => {
  assert.deepEqual(vsceInvocation(["package"], {
    node: "C:\\Program Files\\node.exe",
    script: "C:\\repo with spaces\\vsce"
  }), {
    command: "C:\\Program Files\\node.exe",
    arguments: ["C:\\repo with spaces\\vsce", "package"]
  });
});

test("extension contributes selected push and Source Control actions", async () => {
  const manifest = JSON.parse(await readFile(new URL("../package.json", import.meta.url), "utf8"));
  const commands = new Set(manifest.contributes.commands.map(({ command }) => command));
  assert.ok(commands.has("handoff.pushSelectedChanges"));
  assert.ok(commands.has("handoff.copyHandoffID"));
  assert.deepEqual(
    manifest.contributes.menus["scm/title"].map(({ command }) => command),
    ["handoff.pushSelectedChanges", "handoff.pushChanges"]
  );
  assert.deepEqual(
    manifest.contributes.menus.commandPalette.map(({ command }) => command),
    ["handoff.inspectInboxItem", "handoff.pullInboxItem", "handoff.copyHandoffID"]
  );
});
