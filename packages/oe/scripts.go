// Package oe embeds the scripts that the binary runs with node to load and
// edit open-e2ee.config.ts, and the defineConfig module that the config file
// imports. The binary carries its own copy, so an install from any channel
// needs only Node.js, not this npm package.
package oe

import "embed"

// Scripts holds the files at their paths in this package.
//
//go:embed config/index.js lib/config-load.mjs lib/config-splice.mjs lib/config-node.mjs lib/vendor/acorn.mjs lib/vendor/acorn-LICENSE
var Scripts embed.FS
