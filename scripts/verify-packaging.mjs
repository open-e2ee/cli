import { access, readdir, readFile } from "node:fs/promises";
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
  "scripts/test-native-install.mjs",
  "scripts/verify-release-sbom.mjs",
];

for (const relative of required) {
  await access(path.join(root, relative));
}

const cli = JSON.parse(
  await readFile(path.join(root, "packages/cli/package.json"), "utf8"),
);
const workspace = JSON.parse(
  await readFile(path.join(root, "package.json"), "utf8"),
);
const initializer = JSON.parse(
  await readFile(path.join(root, "packages/create-oe/package.json"), "utf8"),
);
const packageNames = [
  "cli",
  "create-oe",
  "cli-darwin-arm64",
  "cli-darwin-x64",
  "cli-linux-arm64",
  "cli-linux-x64",
  "cli-win32-arm64",
  "cli-win32-x64",
];
for (const packageName of packageNames) {
  const manifest = JSON.parse(
    await readFile(
      path.join(root, "packages", packageName, "package.json"),
      "utf8",
    ),
  );
  if (manifest.version !== workspace.version) {
    throw new Error(
      `${manifest.name} version ${manifest.version} does not match workspace ${workspace.version}`,
    );
  }
}
if (initializer.dependencies?.["@open-e2ee/cli"] !== workspace.version) {
  throw new Error("create-oe must depend on the exact coordinated CLI version");
}
const sdkVersion =
  workspace.devDependencies?.["@open-e2ee/signal-protocol-sdk"];
if (
  initializer.dependencies?.["@open-e2ee/signal-protocol-sdk"] !==
  `^${sdkVersion}`
) {
  throw new Error(
    "create-oe and the release workspace must use one SDK release",
  );
}
if (cli.scripts?.postinstall) {
  throw new Error("@open-e2ee/cli must not use a postinstall download");
}
if (Object.keys(cli.optionalDependencies ?? {}).length !== 6) {
  throw new Error(
    "@open-e2ee/cli must declare all six optional platform packages",
  );
}

const launcher = await readFile(
  path.join(root, "packages/cli/bin/oe.js"),
  "utf8",
);
if (/\bfetch\s*\(|https?:\/\//.test(launcher)) {
  throw new Error("the npm launcher must not contain a binary download path");
}

for (const packageName of Object.keys(cli.optionalDependencies)) {
  const sourceDirectory = path.join(
    root,
    "packages",
    packageName.replace("@open-e2ee/", ""),
    "bin",
  );
  const files = await readdir(sourceDirectory);
  if (files.some((file) => file === "oe" || file === "oe.exe")) {
    throw new Error(
      `source package ${packageName} contains a generated binary`,
    );
  }
}

const releaseWorkflow = await readFile(
  path.join(root, ".github/workflows/release.yml"),
  "utf8",
);
for (const contract of [
  "id-token: write",
  "attestations: write",
  "actions/attest@1e69f48acb82d1966a394da916b4c1698aa569d6",
  "CycloneDX/gh-gomod-generate-sbom@efc74245d6802c8cefd925620515442756c70d8f",
  "version: v1.10.0",
  "-output dist/artifacts/cli-sbom.cdx.json",
  "node scripts/verify-release-sbom.mjs",
  "sha256sum cli-sbom.cdx.json >> checksums.txt",
  "subject-path: dist/artifacts/*",
  "--provenance",
  'tags: ["v*"]',
  "gh release upload",
  "npm view",
]) {
  if (!releaseWorkflow.includes(contract)) {
    throw new Error(`release workflow is missing ${contract}`);
  }
}

for (const forbidden of ["workflow_dispatch:", "NPM_BOOTSTRAP_TOKEN"]) {
  if (releaseWorkflow.includes(forbidden)) {
    throw new Error(`release workflow must not contain ${forbidden}`);
  }
}

const packageLock = await readFile(
  path.join(root, "package-lock.json"),
  "utf8",
);
if (/\b(segment|posthog|mixpanel|amplitude|sentry)\b/i.test(packageLock)) {
  throw new Error("CLI dependencies contain a telemetry package");
}

process.stdout.write("packaging contract passed\n");
