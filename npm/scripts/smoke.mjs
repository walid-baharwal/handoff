import assert from "node:assert/strict";
import { cp, mkdir, mkdtemp, readFile, rm } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { spawnSync } from "node:child_process";
import { pathToFileURL } from "node:url";
import { mainPackageName, platforms } from "./packages.mjs";

export async function smokeTest(outputDirectory) {
  const platform = platforms.find((candidate) => candidate.os === process.platform && candidate.cpu === process.arch);
  if (!platform) throw new Error(`no smoke-test package for ${process.platform}/${process.arch}`);

  const temporaryDirectory = await mkdtemp(path.join(os.tmpdir(), "handoff-npm-smoke-"));
  try {
    const scopeDirectory = path.join(temporaryDirectory, "node_modules", "@walid-baharwal");
    await mkdir(scopeDirectory, { recursive: true });
    const mainDirectory = path.join(scopeDirectory, "handoff");
    await cp(path.join(outputDirectory, "handoff"), mainDirectory, { recursive: true });
    await cp(
      path.join(outputDirectory, "platforms", platform.id),
      path.join(scopeDirectory, path.basename(platform.packageName)),
      { recursive: true },
    );

    const manifest = JSON.parse(await readFile(path.join(mainDirectory, "package.json"), "utf8"));
    assert.equal(manifest.name, mainPackageName);
    const result = spawnSync(process.execPath, [path.join(mainDirectory, "bin", "handoff.js"), "version"], {
      encoding: "utf8",
    });
    if (result.error) throw result.error;
    assert.equal(result.status, 0, result.stderr);
    assert.equal(result.stdout.trim(), manifest.version);
    return manifest.version;
  } finally {
    await rm(temporaryDirectory, { recursive: true, force: true });
  }
}

if (import.meta.url === pathToFileURL(process.argv[1]).href) {
  const outputDirectory = process.argv[2];
  if (!outputDirectory) {
    console.error("usage: smoke.mjs OUTPUT_DIRECTORY");
    process.exitCode = 1;
  } else {
    try {
      const version = await smokeTest(path.resolve(outputDirectory));
      console.log(`smoke-tested npm launcher for ${version}`);
    } catch (error) {
      console.error(`smoke test npm package: ${error.message}`);
      process.exitCode = 1;
    }
  }
}
