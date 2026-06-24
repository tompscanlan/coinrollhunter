// Package web embeds the built Svelte SPA so the single binary serves it with no
// external files (ADR-001). The UI source lives in web/app; `npm run build`
// emits the static bundle to web/dist, which is embedded here. `all:dist` pulls
// in every asset (including dotted/underscored names).
package web

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var files embed.FS

// FS returns the embedded static file system rooted at the build output, so the
// server mounts dist/index.html at "/" and dist/assets/* alongside it.
func FS() fs.FS {
	sub, err := fs.Sub(files, "dist")
	if err != nil {
		panic(err) // dist is embedded at build time; this cannot fail at runtime
	}
	return sub
}
