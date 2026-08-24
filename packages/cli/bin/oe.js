#!/usr/bin/env node

import { createRequire } from "node:module";
import { spawnSync } from "node:child_process";

const platformPackages = {
  "darwin-arm64": "@open-e2ee/cli-darwin-arm64",
  "darwin-x64": "@open-e2ee/cli-darwin-x64",
  "linux-arm64": "@open-e2ee/cli-linux-arm64",
  "linux-x64": "@open-e2ee/cli-linux-x64",
  "win32-arm64": "@open-e2ee/cli-win32-arm64",
  "win32-x64": "@open-e2ee/cli-win32-x64",
};

const require = createRequire(import.meta.url);
const target = `${process.platform}-${process.arch}`;
const packageName = platformPackages[target];

if (!packageName) {
  process.stderr.write(`OpenE2EE CLI does not support ${target}.\n`);
  process.exit(1);
}

let binary;
try {
  binary =
    process.env.OE_BINARY_PATH ||
    require.resolve(
      `${packageName}/bin/oe${process.platform === "win32" ? ".exe" : ""}`,
    );
} catch {
  process.stderr.write(
    `The optional package ${packageName} is missing. Reinstall @open-e2ee/cli with optional dependencies enabled.\n`,
  );
  process.exit(1);
}

const result = spawnSync(binary, process.argv.slice(2), { stdio: "inherit" });
if (result.error) {
  process.stderr.write(`Could not run OpenE2EE CLI: ${result.error.message}\n`);
  process.exit(1);
}
process.exit(result.status ?? 1);
