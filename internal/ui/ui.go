// Package ui holds the viewer page, compiled into the binary.
//
// xterm.js is vendored under assets rather than pulled from a CDN: kolo is a
// single binary a host runs locally, and a viewer that only works when a third
// party is reachable would be a strange thing to hand someone. It also keeps the
// page out of a build toolchain — there is no bundler and nothing to install.
//
// assets/xterm.js and assets/xterm.css are @xterm/xterm v5.5.0, unmodified.
package ui

import "embed"

// FS is the viewer, ready to serve: index.html at the root, assets beneath it.
//
//go:embed index.html assets
var FS embed.FS
