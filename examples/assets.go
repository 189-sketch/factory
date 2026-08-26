package examples

import "embed"

// Files contains the default configuration installed by machinist init.
//
//go:embed config.toml worker.toml agents/*.md
var Files embed.FS
