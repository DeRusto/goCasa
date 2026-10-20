package web

import "embed"

// Static is the vendored front end, served under /static/. Each file's name
// carries its pinned version (htmx.org 2.0.11, htmx-ext-sse 2.2.4, both from
// npm), so a new version is a new URL and browsers may cache them for good.
//
//go:embed static
var Static embed.FS
