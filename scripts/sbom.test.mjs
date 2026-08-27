import assert from "node:assert/strict";
import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { spawnSync } from "node:child_process";
import os from "node:os";
import path from "node:path";
import test from "node:test";

const root = path.resolve(import.meta.dirname, "..");

test("accepts the complete platform-neutral release SBOM", async () => {
  await assertPasses(createFixture());
});

test("rejects a release-version mismatch", async () => {
  const sbom = createFixture();
  sbom.metadata.component.version = "v1.0.1";
  await assertFails(sbom, "main component version must match the release");
});

test("rejects an incomplete component inventory", async () => {
  const sbom = createFixture();
  sbom.components.pop();
  await assertFails(sbom, "component count must be");
});

test("rejects platform-qualified package URLs", async () => {
  const sbom = createFixture();
  sbom.components[0].purl += "?goos=linux";
  await assertFails(sbom, "exact platform-neutral package URL");
});

test("rejects nondeterministic metadata", async () => {
  const sbom = createFixture();
  sbom.metadata.timestamp = "2026-08-27T00:00:00Z";
  await assertFails(sbom, "metadata.timestamp must be omitted");
});

test("rejects a different generator version", async () => {
  const sbom = createFixture();
  sbom.metadata.tools[0].version = "v1.9.0";
  await assertFails(sbom, "cyclonedx-gomod must be v1.10.0");
});

test("rejects missing dependency license evidence", async () => {
  const sbom = createFixture();
  delete sbom.components[0].evidence;
  await assertFails(sbom, "must include license evidence");
});

test("rejects an incomplete dependency graph", async () => {
  const sbom = createFixture();
  sbom.dependencies.pop();
  await assertFails(
    sbom,
    "dependency graph must contain the main component and every dependency exactly once",
  );
});

test("rejects an unexpected component", async () => {
  const sbom = createFixture();
  sbom.components.push({
    "bom-ref": "pkg:golang/example.com/extra@v1.0.0?type=module",
    type: "library",
    name: "example.com/extra",
    version: "v1.0.0",
    purl: "pkg:golang/example.com/extra@v1.0.0",
  });
  await assertFails(sbom, "unexpected component example.com/extra");
});

function createFixture() {
  const modules = JSON.parse(goValue(["mod", "edit", "-json"])).Require.map(
    ({ Path: name, Version: version }) => ({ name, version }),
  );
  modules.push({ name: "std", version: goValue(["env", "GOVERSION"]) });
  const mainRef = "pkg:golang/github.com/open-e2ee/cli@v1.0.0?type=module";
  const components = modules.map(({ name, version }) => ({
    "bom-ref": `pkg:golang/${name}@${version}?type=module`,
    type: "library",
    name,
    version,
    purl: `pkg:golang/${name}@${version}`,
    ...(name === "std"
      ? {}
      : { evidence: { licenses: [{ license: { id: "MIT" } }] } }),
  }));
  return {
    bomFormat: "CycloneDX",
    specVersion: "1.6",
    metadata: {
      tools: [
        { vendor: "CycloneDX", name: "cyclonedx-gomod", version: "v1.10.0" },
      ],
      component: {
        "bom-ref": mainRef,
        type: "application",
        name: "github.com/open-e2ee/cli",
        version: "v1.0.0",
        purl: "pkg:golang/github.com/open-e2ee/cli@v1.0.0",
        evidence: { licenses: [{ license: { id: "Apache-2.0" } }] },
      },
    },
    components,
    dependencies: [
      {
        ref: mainRef,
        dependsOn: components.map((component) => component["bom-ref"]),
      },
      ...components.map((component) => ({ ref: component["bom-ref"] })),
    ],
  };
}

async function writeFixture(sbom) {
  const directory = await mkdtemp(path.join(os.tmpdir(), "oe-cli-sbom-test-"));
  const target = path.join(directory, "cli-sbom.cdx.json");
  await writeFile(target, JSON.stringify(sbom));
  return target;
}

async function run(sbom) {
  const target = await writeFixture(sbom);
  try {
    return spawnSync(
      process.execPath,
      ["scripts/verify-release-sbom.mjs", target, "1.0.0"],
      { cwd: root, encoding: "utf8" },
    );
  } finally {
    await rm(path.dirname(target), { recursive: true, force: true });
  }
}

async function assertPasses(sbom) {
  const result = await run(sbom);
  assert.equal(result.status, 0, result.stderr || result.stdout);
}

async function assertFails(sbom, message) {
  const result = await run(sbom);
  assert.notEqual(result.status, 0, result.stdout);
  assert.match(result.stderr, new RegExp(message));
}

function goValue(args) {
  const result = spawnSync("go", args, { cwd: root, encoding: "utf8" });
  assert.equal(result.status, 0, result.stderr || result.stdout);
  return result.stdout.trim();
}
