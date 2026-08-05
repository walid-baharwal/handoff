import { access, chmod, copyFile, cp, mkdir, readFile, writeFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { mainPackageName, platforms } from "./packages.mjs";

const npmDirectory = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const repositoryDirectory = path.resolve(npmDirectory, "..");
const semverPattern = /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$/;

export function validateVersion(version) {
  if (!semverPattern.test(version)) {
    throw new Error(`invalid release version ${JSON.stringify(version)}; expected SemVer without a leading v`);
  }
}

async function pathExists(target) {
  try {
    await access(target);
    return true;
  } catch {
    return false;
  }
}

async function readJSON(file) {
  return JSON.parse(await readFile(file, "utf8"));
}

async function writeJSON(file, value) {
  await writeFile(file, `${JSON.stringify(value, null, 2)}\n`);
}

export async function preparePackages({ version, binariesDirectory, outputDirectory }) {
  validateVersion(version);
  if (await pathExists(outputDirectory)) {
    throw new Error(`output directory already exists: ${outputDirectory}`);
  }
  for (const platform of platforms) {
    const binary = path.join(binariesDirectory, platform.sourceBinary);
    if (!(await pathExists(binary))) {
      throw new Error(`missing release binary: ${binary}`);
    }
  }

  const mainOutput = path.join(outputDirectory, "handoff");
  await cp(path.join(npmDirectory, "cli"), mainOutput, { recursive: true });
  await copyFile(path.join(repositoryDirectory, "LICENSE"), path.join(mainOutput, "LICENSE"));
  await chmod(path.join(mainOutput, "bin", "handoff.js"), 0o755);
  const mainManifestPath = path.join(mainOutput, "package.json");
  const mainManifest = await readJSON(mainManifestPath);
  if (mainManifest.name !== mainPackageName) {
    throw new Error(`unexpected main package name: ${mainManifest.name}`);
  }
  mainManifest.version = version;
  delete mainManifest.private;
  for (const platform of platforms) {
    mainManifest.optionalDependencies[platform.packageName] = version;
  }
  await writeJSON(mainManifestPath, mainManifest);

  for (const platform of platforms) {
    const sourceDirectory = path.join(npmDirectory, "platforms", platform.id);
    const targetDirectory = path.join(outputDirectory, "platforms", platform.id);
    const binaryDirectory = path.join(targetDirectory, "bin");
    await mkdir(binaryDirectory, { recursive: true });
    await copyFile(path.join(sourceDirectory, "package.json"), path.join(targetDirectory, "package.json"));
    await copyFile(path.join(npmDirectory, "platforms", "README.md"), path.join(targetDirectory, "README.md"));
    await copyFile(path.join(repositoryDirectory, "LICENSE"), path.join(targetDirectory, "LICENSE"));
    const targetBinary = path.join(binaryDirectory, platform.targetBinary);
    await copyFile(path.join(binariesDirectory, platform.sourceBinary), targetBinary);
    await chmod(targetBinary, 0o755);

    const manifestPath = path.join(targetDirectory, "package.json");
    const manifest = await readJSON(manifestPath);
    if (manifest.name !== platform.packageName) {
      throw new Error(`unexpected package name for ${platform.id}: ${manifest.name}`);
    }
    manifest.version = version;
    delete manifest.private;
    await writeJSON(manifestPath, manifest);
  }
}

function parseArguments(arguments_) {
  const values = {};
  for (let index = 0; index < arguments_.length; index += 2) {
    const key = arguments_[index];
    const value = arguments_[index + 1];
    if (!key?.startsWith("--") || !value) {
      throw new Error("usage: prepare-packages.mjs --version VERSION --binaries DIR --output DIR");
    }
    values[key.slice(2)] = value;
  }
  if (!values.version || !values.binaries || !values.output) {
    throw new Error("usage: prepare-packages.mjs --version VERSION --binaries DIR --output DIR");
  }
  return {
    version: values.version,
    binariesDirectory: path.resolve(values.binaries),
    outputDirectory: path.resolve(values.output),
  };
}

if (import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    await preparePackages(parseArguments(process.argv.slice(2)));
  } catch (error) {
    console.error(`prepare npm packages: ${error.message}`);
    process.exitCode = 1;
  }
}
