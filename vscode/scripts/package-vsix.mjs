import { access, chmod, copyFile, cp, mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { execFileSync } from "node:child_process";
import os from "node:os";
import path from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const vscodeDirectory = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const vsceScript = path.join(vscodeDirectory, "node_modules", "@vscode", "vsce", "vsce");
const semverPattern = /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$/;

export const platforms = Object.freeze([
  Object.freeze({ target: "linux-x64", sourceBinary: "handoff-linux-amd64", binary: "handoff" }),
  Object.freeze({ target: "linux-arm64", sourceBinary: "handoff-linux-arm64", binary: "handoff" }),
  Object.freeze({ target: "darwin-x64", sourceBinary: "handoff-darwin-amd64", binary: "handoff" }),
  Object.freeze({ target: "darwin-arm64", sourceBinary: "handoff-darwin-arm64", binary: "handoff" }),
  Object.freeze({ target: "win32-x64", sourceBinary: "handoff-windows-amd64.exe", binary: "handoff.exe" }),
  Object.freeze({ target: "win32-arm64", sourceBinary: "handoff-windows-arm64.exe", binary: "handoff.exe" })
]);

export function validateVersion(version) {
  if (!semverPattern.test(version)) {
    throw new Error(`invalid extension version ${JSON.stringify(version)}; expected SemVer without a leading v`);
  }
}

export function parseArguments(arguments_) {
  const values = {};
  for (let index = 0; index < arguments_.length; index += 2) {
    const key = arguments_[index];
    const value = arguments_[index + 1];
    if (!key?.startsWith("--") || !value) {
      throw new Error("usage: package-vsix.mjs --version VERSION --binaries DIRECTORY --output DIRECTORY");
    }
    values[key.slice(2)] = value;
  }
  if (!values.version || !values.binaries || !values.output) {
    throw new Error("usage: package-vsix.mjs --version VERSION --binaries DIRECTORY --output DIRECTORY");
  }
  return {
    version: values.version,
    binariesDirectory: path.resolve(values.binaries),
    outputDirectory: path.resolve(values.output)
  };
}

export function vsceInvocation(arguments_, { node = process.execPath, script = vsceScript } = {}) {
  return { command: node, arguments: [script, ...arguments_] };
}

async function pathExists(target) {
  try {
    await access(target);
    return true;
  } catch {
    return false;
  }
}

function includeInPackage(source) {
  const relative = path.relative(vscodeDirectory, source);
  if (!relative) return true;
  if (relative === "package-lock.json") return false;
  const first = relative.split(path.sep)[0];
  return ![".npm-cache", "dist", "node_modules", "scripts", "test", "test-binaries"].includes(first);
}

async function prepareStagingDirectory(platform, version, binariesDirectory, stagingDirectory) {
  await cp(vscodeDirectory, stagingDirectory, { recursive: true, filter: includeInPackage });
  const sourceBinary = path.join(binariesDirectory, platform.sourceBinary);
  if (!(await pathExists(sourceBinary))) {
    throw new Error(`missing release binary: ${sourceBinary}`);
  }
  const binaryDirectory = path.join(stagingDirectory, "bin");
  const targetBinary = path.join(binaryDirectory, platform.binary);
  await rm(path.join(binaryDirectory, ".gitkeep"), { force: true });
  await copyFile(sourceBinary, targetBinary);
  await chmod(targetBinary, 0o755);

  const manifestPath = path.join(stagingDirectory, "package.json");
  const manifest = JSON.parse(await readFile(manifestPath, "utf8"));
  manifest.version = version;
  await writeFile(manifestPath, `${JSON.stringify(manifest, null, 2)}\n`);
}

export async function packageVSIX({
  version,
  binariesDirectory,
  outputDirectory,
  packageCommand = process.execPath,
  packageScript = vsceScript
}) {
  validateVersion(version);
  if (await pathExists(outputDirectory)) {
    throw new Error(`output directory already exists: ${outputDirectory}`);
  }
  await mkdir(outputDirectory, { recursive: true });
  const stagingRoot = await mkdtemp(path.join(os.tmpdir(), "handoff-vscode-"));
  try {
    for (const platform of platforms) {
      const stagingDirectory = path.join(stagingRoot, platform.target);
      await prepareStagingDirectory(platform, version, binariesDirectory, stagingDirectory);
      const outputFile = path.join(outputDirectory, `handoff-${version}-${platform.target}.vsix`);
      const invocation = vsceInvocation([
        "package",
        "--no-dependencies",
        "--target",
        platform.target,
        "--out",
        outputFile
      ], { node: packageCommand, script: packageScript });
      execFileSync(invocation.command, invocation.arguments, { cwd: stagingDirectory, stdio: "inherit" });
    }
  } finally {
    await rm(stagingRoot, { recursive: true, force: true });
  }
}

if (import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    await packageVSIX(parseArguments(process.argv.slice(2)));
  } catch (error) {
    console.error(`package VS Code extension: ${error.message}`);
    process.exitCode = 1;
  }
}
