// oe runs this script with node to read open-e2ee.config.ts:
//
//   node config-load.mjs <config file>
//
// It imports the file and writes the JSON of its default export as the only
// output on stdout. Output from the config file goes to stderr. A failure
// writes {code, message} as the last line of stderr and exits 1.
import module from "node:module";
import process from "node:process";
import { pathToFileURL } from "node:url";
import { fail, requireNode } from "./config-node.mjs";

await requireNode();

const file = process.argv[2];
const configURL = pathToFileURL(file).href;
const bundled = new URL("../config/index.js", import.meta.url).href;

// The config file imports defineConfig from @open-e2ee/oe/config. That name
// resolves to the copy next to this script, so the file loads without
// node_modules. The file is always an ES module with types, whatever the
// nearest package.json says.
module.registerHooks({
  resolve(specifier, context, nextResolve) {
    if (specifier === "@open-e2ee/oe/config") {
      return { url: bundled, format: "module", shortCircuit: true };
    }
    const resolved = nextResolve(specifier, context);
    if (specifier === configURL) {
      return { ...resolved, format: "module-typescript" };
    }
    return resolved;
  },
});

const write = process.stdout.write.bind(process.stdout);
process.stdout.write = process.stderr.write.bind(process.stderr);

let namespace;
try {
  namespace = await import(configURL);
} catch (error) {
  await fail("CONFIG_INVALID", `${file} failed to load: ${describe(error)}`);
}
if (!("default" in namespace)) {
  await fail("CONFIG_INVALID", `${file} has no default export.`);
}
const problem = findNonJSON(namespace.default, "", new Set());
if (problem) {
  const where = problem.path ? problem.path : "the default export";
  await fail(
    "CONFIG_INVALID",
    `${file}: ${where} is ${problem.found}; the default export must be JSON data.`,
  );
}
write(`${JSON.stringify(namespace.default)}\n`, () => process.exit(0));

function describe(error) {
  if (error instanceof Error) {
    return error.message.split("\n")[0];
  }
  return String(error);
}

// findNonJSON returns the path and kind of the first value that JSON cannot
// hold unchanged.
function findNonJSON(value, path, ancestors) {
  switch (typeof value) {
    case "string":
    case "boolean":
      return undefined;
    case "number":
      return Number.isFinite(value) ? undefined : { path, found: `${value}` };
    case "undefined":
      return { path, found: "undefined" };
    case "function":
      return { path, found: "a function" };
    case "symbol":
      return { path, found: "a symbol" };
    case "bigint":
      return { path, found: "a bigint" };
  }
  if (value === null) {
    return undefined;
  }
  if (ancestors.has(value)) {
    return { path, found: "a reference to itself" };
  }
  const prototype = Object.getPrototypeOf(value);
  ancestors.add(value);
  try {
    if (Array.isArray(value) && prototype === Array.prototype) {
      for (let index = 0; index < value.length; index++) {
        const item = `${path}[${index}]`;
        if (!(index in value)) {
          return { path: item, found: "an empty array slot" };
        }
        const problem = findNonJSON(value[index], item, ancestors);
        if (problem) {
          return problem;
        }
      }
      return undefined;
    }
    if (prototype !== Object.prototype && prototype !== null) {
      const name = prototype?.constructor?.name;
      return { path, found: name ? `a ${name}` : "an object with a prototype" };
    }
    for (const key of Object.keys(value)) {
      const problem = findNonJSON(
        value[key],
        path ? `${path}.${key}` : key,
        ancestors,
      );
      if (problem) {
        return problem;
      }
    }
    return undefined;
  } finally {
    ancestors.delete(value);
  }
}
