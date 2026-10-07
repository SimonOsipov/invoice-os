// Package assets embeds the design-token brand assets so the gateway binary can serve them.
package assets

import _ "embed"

// Mark is the brand mark, served to mail clients at /emails/mark.png.
//
//go:embed mark.png
var Mark []byte
