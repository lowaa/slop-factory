// Package personas embeds the shipped persona library so slop-review runs
// without a checkout.
package personas

import "embed"

// FS holds the library's markdown files at its root.
//
//go:embed *.md
var FS embed.FS
