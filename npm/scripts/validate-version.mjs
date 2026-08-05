import { validateVersion } from "./prepare-packages.mjs";

const version = process.argv[2];
try {
  validateVersion(version || "");
  console.log(`release version: ${version}`);
} catch (error) {
  console.error(error.message);
  process.exitCode = 1;
}
