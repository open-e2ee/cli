// Package schema embeds the JSON Schema of the project config. oe validates
// the config with this copy, and each release publishes the same file at the
// schema's $id.
package schema

import _ "embed"

// ConfigV2 is config-v2.json.
//
//go:embed config-v2.json
var ConfigV2 []byte
