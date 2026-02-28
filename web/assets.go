// Package web contains embedded web UI template assets.
package web

import "embed"

// Assets contains embedded web UI assets for the built-in dashboard.
//
//go:embed *.html *.png
var Assets embed.FS
