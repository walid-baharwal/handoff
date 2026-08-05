import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import path from "node:path";
import { spawnSync } from "node:child_process";
import { pathToFileURL } from "node:url";
import { mainPackageName, packageDirectories, platforms } from "./packages.mjs";

export function parsePackReport(output, packageName) {
  const parsed = JSON.parse(output);
  const report = Array.isArray(parsed) ? parsed[0] : parsed?.[packageName];
  if (!report || !Array.isArray(report.files)) {
    throw new Error(`npm pack returned an invalid report for ${packageName}`);
  }
  return report;
}

export async function verifyPackages(outputDirectory) {
  const directories = packageDirectories(outputDirectory);
  const manifests = await Promise.all(
    directories.map(async (directory) => JSON.parse(await readFile(path.join(directory, "package.json"), "utf8"))),
  );
  const version = manifests[0].version;
  assert.ok(version && version !== "0.0.0-development", "generated packages must use a release version");
  assert.deepEqual(
    manifests.slice(0, platforms.length).map((manifest) => manifest.name),
    platforms.map((platform) => platform.packageName),
  );
  assert.equal(manifests.at(-1).name, mainPackageName);
  assert.ok(manifests.every((manifest) => manifest.version === version), "all package versions must match");

  for (let index = 0; index < directories.length; index += 1) {
    const packed = spawnSync("npm", ["pack", directories[index], "--dry-run", "--json"], {
      encoding: "utf8",
      maxBuffer: 10 * 1024 * 1024,
    });
    if (packed.error) throw packed.error;
    if (packed.status !== 0) {
      throw new Error(`npm pack failed for ${manifests[index].name}: ${packed.stderr}`);
    }
    const report = parsePackReport(packed.stdout, manifests[index].name);
    const files = report.files.map((file) => file.path);
    assert.ok(files.includes("package.json"), `${manifests[index].name} is missing package.json`);
    assert.ok(files.includes("README.md"), `${manifests[index].name} is missing README.md`);
    assert.ok(files.includes("LICENSE"), `${manifests[index].name} is missing LICENSE`);
    if (index < platforms.length) {
      assert.ok(files.includes(`bin/${platforms[index].targetBinary}`), `${manifests[index].name} is missing its binary`);
    } else {
      assert.ok(files.includes("bin/handoff.js"), `${mainPackageName} is missing its launcher`);
      assert.ok(files.includes("lib/cli.js"), `${mainPackageName} is missing its launcher library`);
    }
  }
  return version;
}

if (import.meta.url === pathToFileURL(process.argv[1]).href) {
  const outputDirectory = process.argv[2];
  if (!outputDirectory) {
    console.error("usage: verify-packages.mjs OUTPUT_DIRECTORY");
    process.exitCode = 1;
  } else {
    try {
      const version = await verifyPackages(path.resolve(outputDirectory));
      console.log(`verified npm packages for ${version}`);
    } catch (error) {
      console.error(`verify npm packages: ${error.message}`);
      process.exitCode = 1;
    }
  }
}
