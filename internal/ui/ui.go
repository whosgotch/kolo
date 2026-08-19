// Package ui holds the org's page, compiled into the binary.
//
// xterm.js is vendored rather than pulled from a CDN: kolo is a single binary run
// locally, and a viewer that needs a third party reachable is a strange thing to
// hand someone. It also keeps the page out of a build toolchain.
//
// assets/xterm.js and assets/xterm.css are @xterm/xterm v5.5.0, unmodified.
package ui

import "embed"

// FS is the pages, ready to serve: index.html and the join page at the root,
// assets beneath them.
//
//go:embed index.html join.html assets
var FS embed.FS
