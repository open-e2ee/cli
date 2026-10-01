// Package skills embeds the agent skills that oe agent setup installs. The
// files stay at skills/<name>/SKILL.md, so a skill installer that reads this
// repository finds them, and the binary carries the same bytes.
package skills

import "embed"

// Files holds each SKILL.md at its path in this package.
//
//go:embed */SKILL.md
var Files embed.FS
