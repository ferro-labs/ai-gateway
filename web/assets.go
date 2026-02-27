// Package web contains embedded web UI template assets.
package web

import "embed"

// Templates contains embedded HTML templates for the built-in web UI.
//
//go:embed *.html
var Templates embed.FS
