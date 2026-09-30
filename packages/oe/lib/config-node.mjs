// The loader and the splicer share this runtime check and failure format. The
// module uses only syntax and built-ins that Node.js 20, Bun, and Deno
// understand, so an unsupported runtime reaches the refusal instead of a
// link or parse error.
import module from "node:module";
import process from "node:process";

// requireNode refuses every runtime except Node.js 22.18 or later with type
// stripping on. Node.js 23 strips types by default from 23.6.
export async function requireNode() {
  const { versions, features } = process;
  const wanted = "oe needs Node.js 22.18 or later";
  if (versions.bun) {
    return fail(
      "NODE_REQUIRED",
      `${wanted}; node on PATH is Bun ${versions.bun}.`,
    );
  }
  if (versions.deno || globalThis.Deno) {
    const version = versions.deno ?? globalThis.Deno?.version?.deno;
    return fail("NODE_REQUIRED", `${wanted}; node on PATH is Deno ${version}.`);
  }
  const [major, minor] = String(versions.node).split(".").map(Number);
  if (!(
    major > 23 ||
    (major === 23 && minor >= 6) ||
    (major === 22 && minor >= 18)
  )) {
    return fail("NODE_REQUIRED", `${wanted}; found Node.js ${versions.node}.`);
  }
  if (features.typescript !== "strip" && features.typescript !== "transform") {
    return fail(
      "NODE_REQUIRED",
      `${wanted} with type stripping on; Node.js ${versions.node} has it turned off by a flag or NODE_OPTIONS.`,
    );
  }
  if (
    typeof module.registerHooks !== "function" ||
    typeof module.stripTypeScriptTypes !== "function"
  ) {
    return fail(
      "NODE_REQUIRED",
      `${wanted}; Node.js ${versions.node} has no module.registerHooks.`,
    );
  }
}

// fail writes the failure as the last line of stderr and exits 1. The promise
// never settles, so the caller stops at its await while the line flushes.
export function fail(code, message) {
  return new Promise(() => {
    process.stderr.write(`${JSON.stringify({ code, message })}\n`, () =>
      process.exit(1),
    );
  });
}
