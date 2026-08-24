// Package ui holds the org's page, compiled into the binary.
package ui

import "embed"

// FS serves the pages from the root, assets beneath them.
//
//go:embed index.html join.html assets
var FS embed.FS
