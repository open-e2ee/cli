import { access, readFile } from "node:fs/promises";
import { fileURLToPath } from "node:url";
import path from "node:path";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const required = [
  "packages/cli/package.json",
  "packages/create-oe/package.json",
  "packages/cli-darwin-arm64/package.json",
  "packages/cli-darwin-x64/package.json",
  "packages/cli-linux-arm64/package.json",
  "packages/cli-linux-x64/package.json",
  "packages/cli-win32-arm64/package.json",
  "packages/cli-win32-x64/package.json",
  "Formula/oe.rb",
  ".github/workflows/release.yml",
];

for (const relative of required) {
  await access(path.join(root, relative));
}

const cli = JSON.parse(await readFile(path.join(root, "packages/cli/package.json"), "utf8"));
if (cli.scripts?.postinstall) {
  throw new Error("@open-e2ee/cli must not use a postinstall download");
}
if (Object.keys(cli.optionalDependencies ?? {}).length !== 6) {
  throw new Error("@open-e2ee/cli must declare all six optional platform packages");
}

process.stdout.write("packaging contract passed\n");
