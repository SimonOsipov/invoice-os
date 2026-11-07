// Package assets embeds the design-token brand assets so the gateway binary can serve them.
package assets

import _ "embed"

// Mark is the master's raster for mail clients, served at /emails/mark.png.
//
//go:embed mail-mark.png
var Mark []byte
