import assert from "node:assert/strict";
import { access, mkdir, mkdtemp, readFile, rm } from "node:fs/promises";
import { spawnSync } from "node:child_process";
import os from "node:os";
import path from "node:path";
import test from "node:test";

test("stages six native optional packages without a postinstall downloader", async () => {
  const directory = await mkdtemp(path.join(os.tmpdir(), "oe-release-"));
  try {
    const result = spawnSync(
      process.execPath,
      ["scripts/build-release.mjs", "0.1.0-test.1", directory],
      {
        cwd: path.resolve(import.meta.dirname, ".."),
        encoding: "utf8",
      },
    );
    assert.equal(result.status, 0, result.stderr || result.stdout);
    const cli = JSON.parse(
      await readFile(path.join(directory, "npm/cli/package.json"), "utf8"),
    );
    assert.equal(cli.version, "0.1.0-test.1");
    assert.equal(cli.scripts?.postinstall, undefined);
    assert.equal(Object.keys(cli.optionalDependencies).length, 6);
    for (const target of [
      "cli-darwin-arm64/bin/oe",
      "cli-darwin-x64/bin/oe",
      "cli-linux-arm64/bin/oe",
      "cli-linux-x64/bin/oe",
      "cli-win32-arm64/bin/oe.exe",
      "cli-win32-x64/bin/oe.exe",
    ]) {
      await access(path.join(directory, "npm", target));
    }
    const checksums = await readFile(
      path.join(directory, "artifacts/checksums.txt"),
      "utf8",
    );
    assert.equal(checksums.trim().split("\n").length, 6);

    const platformPackage = `cli-${process.platform}-${process.arch}`;
    const tarballs = [];
    for (const packageName of [platformPackage, "cli", "create-oe"]) {
      const packageDirectory = path.join(directory, "npm", packageName);
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
    const project = path.join(directory, "project");
    const initialized = spawnSync(
      process.execPath,
      [
        path.join(install, "node_modules/create-oe/bin/create-oe.js"),
        "--directory",
        project,
        "--name",
        "package-chat",
      ],
      { encoding: "utf8" },
    );
    assert.equal(initialized.status, 0, initialized.stderr);
    assert.match(initialized.stdout, /alice: hello/);
    await access(path.join(project, "open-e2ee.jsonc"));
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});

test("npm shim executes an explicitly selected native binary", async () => {
  const root = path.resolve(import.meta.dirname, "..");
  const binary = path.join(
    root,
    "bin",
    process.platform === "win32" ? "oe.exe" : "oe",
  );
  await mkdir(path.dirname(binary), { recursive: true });
  const built = spawnSync("go", ["build", "-o", binary, "./cmd/oe"], {
    cwd: root,
    encoding: "utf8",
  });
  assert.equal(built.status, 0, built.stderr);
  const result = spawnSync(
    process.execPath,
    ["packages/cli/bin/oe.js", "--json", "version"],
    {
      cwd: root,
      env: { ...process.env, OE_BINARY_PATH: binary },
      encoding: "utf8",
    },
  );
  assert.equal(result.status, 0, result.stderr);
  assert.match(result.stdout, /"command":"version"/);
});
