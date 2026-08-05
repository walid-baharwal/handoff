import assert from "node:assert/strict";
import { mkdir, mkdtemp, readFile, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { distTagForVersion, publishPackages } from "../scripts/publish-packages.mjs";
import { mainPackageName, platforms } from "../scripts/packages.mjs";
import { preparePackages, validateVersion } from "../scripts/prepare-packages.mjs";

const npmDirectory = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");

test("source manifests stay synchronized with the package map", async () => {
  const main = JSON.parse(await readFile(path.join(npmDirectory, "cli", "package.json"), "utf8"));
  assert.equal(main.name, mainPackageName);
  assert.equal(main.version, "0.0.0-development");
  assert.equal(main.private, true);
  assert.deepEqual(Object.keys(main.optionalDependencies).sort(), platforms.map((platform) => platform.packageName).sort());
	for (const platform of platforms) {
    const manifest = JSON.parse(
      await readFile(path.join(npmDirectory, "platforms", platform.id, "package.json"), "utf8"),
    );
    assert.equal(manifest.name, platform.packageName);
    assert.deepEqual(manifest.os, [platform.os]);
    assert.deepEqual(manifest.cpu, [platform.cpu]);
    assert.equal(manifest.version, "0.0.0-development");
    assert.equal(manifest.private, true);
  }
});

test("prepares version-matched packages from release binaries", async () => {
  const temporaryDirectory = await mkdtemp(path.join(os.tmpdir(), "handoff-npm-test-"));
  const binariesDirectory = path.join(temporaryDirectory, "binaries");
  const outputDirectory = path.join(temporaryDirectory, "output");
  await mkdir(binariesDirectory);
  for (const platform of platforms) {
    await writeFile(path.join(binariesDirectory, platform.sourceBinary), platform.sourceBinary);
  }

  await preparePackages({ version: "1.2.3-beta.1", binariesDirectory, outputDirectory });
  const main = JSON.parse(await readFile(path.join(outputDirectory, "handoff", "package.json"), "utf8"));
  assert.equal(main.version, "1.2.3-beta.1");
  assert.equal(main.private, undefined);
  for (const platform of platforms) {
    assert.equal(main.optionalDependencies[platform.packageName], "1.2.3-beta.1");
    const manifest = JSON.parse(
      await readFile(path.join(outputDirectory, "platforms", platform.id, "package.json"), "utf8"),
    );
    assert.equal(manifest.version, "1.2.3-beta.1");
    assert.equal(manifest.private, undefined);
    assert.equal(
      await readFile(path.join(outputDirectory, "platforms", platform.id, "bin", platform.targetBinary), "utf8"),
      platform.sourceBinary,
    );
  }

  const publishCalls = [];
  await publishPackages(outputDirectory, {
    log() {},
    runNpm(arguments_) {
      publishCalls.push(arguments_);
      return arguments_[0] === "view"
        ? { status: 1, stdout: "", stderr: "npm error code E404" }
        : { status: 0 };
    },
  });
  const publishes = publishCalls.filter((arguments_) => arguments_[0] === "publish");
  assert.equal(publishes.length, platforms.length + 1);
  assert.deepEqual(
    publishes.slice(0, platforms.length).map((arguments_) => path.basename(arguments_[1])),
    platforms.map((platform) => platform.id),
  );
  assert.equal(path.basename(publishes.at(-1)[1]), "handoff");
  assert.ok(publishes.every((arguments_) => arguments_.at(-1) === "next"));

  let attemptedPublish = false;
  await publishPackages(outputDirectory, {
    log() {},
    runNpm(arguments_) {
      if (arguments_[0] === "publish") attemptedPublish = true;
      return { status: 0, stdout: '"1.2.3-beta.1"', stderr: "" };
    },
  });
  assert.equal(attemptedPublish, false);
  await assert.rejects(
    preparePackages({ version: "1.2.3", binariesDirectory, outputDirectory }),
    /output directory already exists/,
  );
});

test("validates release versions and chooses safe npm dist-tags", () => {
  assert.doesNotThrow(() => validateVersion("2.0.0"));
  assert.doesNotThrow(() => validateVersion("2.0.0-rc.1"));
  assert.throws(() => validateVersion("v2.0.0"), /invalid release version/);
  assert.throws(() => validateVersion("latest"), /invalid release version/);
  assert.equal(distTagForVersion("2.0.0"), "latest");
  assert.equal(distTagForVersion("2.0.0-rc.1"), "next");
});
