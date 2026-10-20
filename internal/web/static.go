package web

import (
	"crypto/sha256"
	"embed"
	"fmt"
)

// Static is the vendored front end, served under /static/. Each file's name
// carries its pinned version (htmx.org 2.0.11, htmx-ext-sse 2.2.4, sortablejs
// 1.15.6, all from npm), so a new version is a new URL and browsers may cache them for good.
// Our own files (app.css, the favicon) are linked by assetURL instead.
//
//go:embed static
var Static embed.FS

// Rebuild app.css after changing classes: needs the Tailwind standalone CLI on PATH (see README).
//go:generate go tool templ generate -path ../..
//go:generate tailwindcss -i input.css -o static/app.css --minify

var cssURL, faviconURL = assetURL("app.css"), assetURL("gocasa-favicon.png")

// assetURL is a static file's URL with its content hash, so the immutable cache follows each edit.
func assetURL(name string) string {
	b, err := Static.ReadFile("static/" + name)
	if err != nil {
		panic(err)
	}
	h := sha256.Sum256(b)
	return fmt.Sprintf("/static/%s?v=%x", name, h[:6])
}
