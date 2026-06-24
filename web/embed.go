// Package web embeds the static UI so the single binary serves it with no
// external files (ADR-001). For now it carries a placeholder index.html; once the
// Svelte app builds to web/dist, switch the directive to `//go:embed all:dist`
// and return a sub-FS rooted there.
package web

import (
	"embed"
	"io/fs"
)

//go:embed index.html
var files embed.FS

// FS returns the embedded static file system to mount at the server root.
func FS() fs.FS { return files }
