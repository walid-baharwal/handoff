import { readFile } from "node:fs/promises";
import path from "node:path";
import { spawnSync } from "node:child_process";
import { pathToFileURL } from "node:url";
import { packageDirectories } from "./packages.mjs";

export function distTagForVersion(version) {
  return version.includes("-") ? "next" : "latest";
}

function runNpm(arguments_, options = {}) {
  return spawnSync("npm", arguments_, {
    encoding: options.encoding,
    stdio: options.stdio,
    maxBuffer: 10 * 1024 * 1024,
  });
}

async function manifest(directory) {
  return JSON.parse(await readFile(path.join(directory, "package.json"), "utf8"));
}

export async function publishPackages(outputDirectory, options = {}) {
  const npmRunner = options.runNpm || runNpm;
  const log = options.log || console.log;
  let releaseVersion;
  for (const directory of packageDirectories(outputDirectory)) {
    const packageManifest = await manifest(directory);
    if (!releaseVersion) releaseVersion = packageManifest.version;
    if (packageManifest.version !== releaseVersion) {
      throw new Error(`${packageManifest.name} has version ${packageManifest.version}; expected ${releaseVersion}`);
    }

    const specification = `${packageManifest.name}@${packageManifest.version}`;
    const existing = npmRunner(["view", specification, "version", "--json"], { encoding: "utf8" });
    if (existing.status === 0) {
      log(`npm package already exists; skipping ${specification}`);
      continue;
    }
    const lookupError = `${existing.stdout || ""}\n${existing.stderr || ""}`;
    if (!/(?:E404|404 Not Found)/i.test(lookupError)) {
      throw new Error(`could not check ${specification}: ${lookupError.trim()}`);
    }

    const tag = distTagForVersion(packageManifest.version);
    log(`publishing ${specification} with dist-tag ${tag}`);
    const published = npmRunner(["publish", directory, "--access", "public", "--tag", tag], { stdio: "inherit" });
    if (published.error) throw published.error;
    if (published.status !== 0) {
      throw new Error(`npm publish failed for ${specification} with exit code ${published.status}`);
    }
  }
}

if (import.meta.url === pathToFileURL(process.argv[1]).href) {
  const outputDirectory = process.argv[2];
  if (!outputDirectory) {
    console.error("usage: publish-packages.mjs OUTPUT_DIRECTORY");
    process.exitCode = 1;
  } else {
    try {
      await publishPackages(path.resolve(outputDirectory));
    } catch (error) {
      console.error(`publish npm packages: ${error.message}`);
      process.exitCode = 1;
    }
  }
}
