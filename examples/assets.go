package examples

import "embed"

// Files contains the default configuration installed by factory init.
//
//go:embed config.toml worker.toml agents/*.md
var Files embed.FS
