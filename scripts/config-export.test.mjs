import assert from "node:assert/strict";
import { mkdir, mkdtemp, rm, writeFile } from "node:fs/promises";
import { spawnSync } from "node:child_process";
import os from "node:os";
import path from "node:path";
import test from "node:test";

const root = path.resolve(import.meta.dirname, "..");
const tsc = path.join(root, "node_modules/typescript/bin/tsc");

function config(deliveryRetention) {
  return `import { defineConfig } from "@open-e2ee/oe/config";

export default defineConfig({
  product: "signal-relay",
  project: "typed-chat",
  relay: { deliveryRetention: "${deliveryRetention}", attachmentRetention: "7d" },
  environments: { sandbox: {} },
});
`;
}

// installPackedCLI packs packages/oe and extracts the tarball into
// directory/node_modules, as npm install puts it.
async function installPackedCLI(directory) {
  const packed = spawnSync(
    "npm",
    ["pack", "--silent", "--pack-destination", directory],
    { cwd: path.join(root, "packages/oe"), encoding: "utf8" },
  );
  assert.equal(packed.status, 0, packed.stderr);
  const target = path.join(directory, "node_modules/@open-e2ee/oe");
  await mkdir(target, { recursive: true });
  const extracted = spawnSync(
    "tar",
    [
      "-xzf",
      path.join(directory, packed.stdout.trim()),
      "-C",
      target,
      "--strip-components=1",
    ],
    { encoding: "utf8" },
  );
  assert.equal(extracted.status, 0, extracted.stderr);
}

test("the ./config export resolves from a packed tarball", async () => {
  const directory = await mkdtemp(path.join(os.tmpdir(), "oe-config-export-"));
  try {
    await installPackedCLI(directory);
    await writeFile(path.join(directory, "open-e2ee.config.ts"), config("7d"));
    const resolved = spawnSync(
      process.execPath,
      [
        "--input-type=module",
        "--eval",
        `const { defineConfig } = await import("@open-e2ee/oe/config");
const { default: value } = await import("./open-e2ee.config.ts");
process.stdout.write(JSON.stringify({ defineConfig: typeof defineConfig, value }));`,
      ],
      { cwd: directory, encoding: "utf8" },
    );
    assert.equal(resolved.status, 0, resolved.stderr);
    const { defineConfig, value } = JSON.parse(resolved.stdout);
    assert.equal(defineConfig, "function");
    assert.equal(value.relay.deliveryRetention, "7d");
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});

test("a wrong enumerated value fails tsc --noEmit", async () => {
  const directory = await mkdtemp(path.join(os.tmpdir(), "oe-config-types-"));
  try {
    await installPackedCLI(directory);
    await writeFile(
      path.join(directory, "tsconfig.json"),
      JSON.stringify({
        compilerOptions: { module: "nodenext", strict: true },
        files: ["open-e2ee.config.ts"],
      }),
    );
    const check = () =>
      spawnSync(process.execPath, [tsc, "--noEmit", "-p", directory], {
        encoding: "utf8",
      });

    await writeFile(path.join(directory, "open-e2ee.config.ts"), config("7d"));
    const valid = check();
    assert.equal(valid.status, 0, valid.stdout + valid.stderr);

    await writeFile(path.join(directory, "open-e2ee.config.ts"), config("2d"));
    const wrong = check();
    assert.notEqual(wrong.status, 0, "tsc accepted deliveryRetention 2d");
    assert.match(
      wrong.stdout,
      /open-e2ee\.config\.ts\(6,\d+\): error TS2322: Type '"2d"' is not assignable to type 'Retention'/,
    );
  } finally {
    await rm(directory, { recursive: true, force: true });
  }
});
