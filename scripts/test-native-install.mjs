import assert from "node:assert/strict";
import { chmod, cp, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { spawnSync } from "node:child_process";
import os from "node:os";
import path from "node:path";

const binary = process.argv[2];
if (!binary)
  throw new Error("usage: node scripts/test-native-install.mjs <binary>");

const root = path.resolve(import.meta.dirname, "..");
const platformPackage = `cli-${process.platform}-${process.arch}`;
const directory = await mkdtemp(path.join(os.tmpdir(), "oe-native-install-"));

try {
  const stagedCLI = path.join(directory, "cli");
  const stagedPlatform = path.join(directory, platformPackage);
  await cp(path.join(root, "packages/cli"), stagedCLI, { recursive: true });
  await cp(path.join(root, "packages", platformPackage), stagedPlatform, {
    recursive: true,
  });
  const executable = process.platform === "win32" ? "oe.exe" : "oe";
  const target = path.join(stagedPlatform, "bin", executable);
  await cp(path.resolve(binary), target);
  if (process.platform !== "win32") await chmod(target, 0o755);

  const packagePath = path.join(stagedCLI, "package.json");
  const cli = JSON.parse(await readFile(packagePath, "utf8"));
  cli.optionalDependencies = {
    [`@open-e2ee/${platformPackage}`]: cli.version,
  };
  await writeFile(packagePath, JSON.stringify(cli, null, 2) + "\n");

  const tarballs = [];
  for (const packageDirectory of [stagedPlatform, stagedCLI]) {
    const packed = spawnSync("npm", ["pack", "--silent"], {
      cwd: packageDirectory,
      encoding: "utf8",
    });
    assert.equal(packed.status, 0, packed.stderr);
    tarballs.push(path.join(packageDirectory, packed.stdout.trim()));
  }
  const install = path.join(directory, "install");
  const installed = spawnSync(
    "npm",
    ["install", "--prefix", install, "--silent", ...tarballs],
    { encoding: "utf8" },
  );
  assert.equal(installed.status, 0, installed.stderr);
  const result = spawnSync(
    process.execPath,
    [
      path.join(install, "node_modules/@open-e2ee/cli/bin/oe.js"),
      "--json",
      "version",
    ],
    { encoding: "utf8" },
  );
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /"command":"version"/);
} finally {
  await rm(directory, { recursive: true, force: true });
}
