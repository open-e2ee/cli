import { readFile } from "node:fs/promises";
import { spawnSync } from "node:child_process";
import path from "node:path";
import process from "node:process";

const [sbomPath, releaseVersion] = process.argv.slice(2);
if (
  !sbomPath ||
  !/^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$/.test(releaseVersion ?? "")
) {
  throw new Error(
    "usage: node scripts/verify-release-sbom.mjs <sbom.cdx.json> <version>",
  );
}

const sbom = JSON.parse(await readFile(path.resolve(sbomPath), "utf8"));
const expectedModules = goModules();
const expectedGoVersion = goValue(["env", "GOVERSION"]);
const failures = [];

expect(sbom.bomFormat === "CycloneDX", "bomFormat must be CycloneDX");
expect(sbom.specVersion === "1.6", "specVersion must be 1.6");
expect(!Object.hasOwn(sbom, "serialNumber"), "serialNumber must be omitted");
expect(
  !Object.hasOwn(sbom.metadata ?? {}, "timestamp"),
  "metadata.timestamp must be omitted",
);

const tool = (sbom.metadata?.tools ?? []).find(
  (candidate) => candidate.name === "cyclonedx-gomod",
);
expect(tool?.vendor === "CycloneDX", "CycloneDX must own the SBOM tool record");
expect(tool?.version === "v1.10.0", "cyclonedx-gomod must be v1.10.0");

const main = sbom.metadata?.component;
expect(main?.type === "application", "main component must be an application");
expect(
  main?.name === "github.com/open-e2ee/cli",
  "main component must be github.com/open-e2ee/cli",
);
expect(
  main?.version === `v${releaseVersion}`,
  "main component version must match the release",
);
expect(
  main?.purl === `pkg:golang/github.com/open-e2ee/cli@v${releaseVersion}`,
  "main component must have the exact platform-neutral package URL",
);
expect(hasLicense(main, "Apache-2.0"), "main component must report Apache-2.0");

const expected = new Map(
  expectedModules.map(({ name, version }) => [name, version]),
);
expected.set("std", expectedGoVersion);
const components = new Map(
  (sbom.components ?? []).map((component) => [component.name, component]),
);
expect(
  components.size === expected.size,
  `component count must be ${expected.size}, got ${components.size}`,
);

for (const [name, version] of expected) {
  const component = components.get(name);
  expect(component !== undefined, `component ${name}@${version} is missing`);
  if (!component) continue;
  expect(component.type === "library", `component ${name} must be a library`);
  expect(
    component.version === version,
    `component ${name} must use ${version}`,
  );
  expect(
    component.purl === `pkg:golang/${name}@${version}`,
    `component ${name} must have an exact platform-neutral package URL`,
  );
  if (name !== "std") {
    expect(
      (component.evidence?.licenses ?? []).length > 0,
      `component ${name} must include license evidence`,
    );
  }
}

for (const name of components.keys()) {
  expect(expected.has(name), `unexpected component ${name}`);
}

const expectedRefs = new Set([
  main?.["bom-ref"],
  ...(sbom.components ?? []).map((component) => component["bom-ref"]),
]);
const dependencyRefs = new Set(
  (sbom.dependencies ?? []).map((dependency) => dependency.ref),
);
expect(
  dependencyRefs.size === expectedRefs.size &&
    [...expectedRefs].every((reference) => dependencyRefs.has(reference)),
  "dependency graph must contain the main component and every dependency exactly once",
);

if (failures.length > 0) {
  throw new Error(`CLI SBOM verification failed:\n- ${failures.join("\n- ")}`);
}

process.stdout.write(
  `CLI SBOM contract passed for ${releaseVersion}: ${components.size} components\n`,
);

function expect(condition, message) {
  if (!condition) failures.push(message);
}

function hasLicense(component, identifier) {
  return (component?.evidence?.licenses ?? []).some(
    (entry) => entry.license?.id === identifier,
  );
}

function goModules() {
  return JSON.parse(goValue(["mod", "edit", "-json"])).Require.map(
    ({ Path: name, Version: version }) => ({ name, version }),
  );
}

function goValue(args) {
  const result = spawnSync("go", args, { encoding: "utf8" });
  if (result.status !== 0) {
    throw new Error(
      result.stderr || result.stdout || `go ${args.join(" ")} failed`,
    );
  }
  return result.stdout.trim();
}
