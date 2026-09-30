// defineConfig returns the project config unchanged. The types are in
// index.d.ts. oe reads the value that open-e2ee.config.ts exports as its
// default, and it resolves this module to its own copy, so the file loads
// without node_modules.
export function defineConfig(config) {
  return config;
}
