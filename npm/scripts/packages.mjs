export const packageScope = "@walid-baharwal";
export const mainPackageName = `${packageScope}/handoff`;

export const platforms = Object.freeze([
  Object.freeze({
    id: "linux-x64",
    packageName: `${packageScope}/handoff-linux-x64`,
    os: "linux",
    cpu: "x64",
    sourceBinary: "handoff-linux-amd64",
    targetBinary: "handoff",
  }),
  Object.freeze({
    id: "linux-arm64",
    packageName: `${packageScope}/handoff-linux-arm64`,
    os: "linux",
    cpu: "arm64",
    sourceBinary: "handoff-linux-arm64",
    targetBinary: "handoff",
  }),
  Object.freeze({
    id: "darwin-x64",
    packageName: `${packageScope}/handoff-darwin-x64`,
    os: "darwin",
    cpu: "x64",
    sourceBinary: "handoff-darwin-amd64",
    targetBinary: "handoff",
  }),
  Object.freeze({
    id: "darwin-arm64",
    packageName: `${packageScope}/handoff-darwin-arm64`,
    os: "darwin",
    cpu: "arm64",
    sourceBinary: "handoff-darwin-arm64",
    targetBinary: "handoff",
  }),
  Object.freeze({
    id: "windows-x64",
    packageName: `${packageScope}/handoff-windows-x64`,
    os: "win32",
    cpu: "x64",
    sourceBinary: "handoff-windows-amd64.exe",
    targetBinary: "handoff.exe",
  }),
  Object.freeze({
    id: "windows-arm64",
    packageName: `${packageScope}/handoff-windows-arm64`,
    os: "win32",
    cpu: "arm64",
    sourceBinary: "handoff-windows-arm64.exe",
    targetBinary: "handoff.exe",
  }),
]);

export function packageDirectories(outputDirectory) {
  return [
    ...platforms.map((platform) => `${outputDirectory}/platforms/${platform.id}`),
    `${outputDirectory}/handoff`,
  ];
}
