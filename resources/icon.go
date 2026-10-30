// Package resources exposes shared application assets.
package resources

import _ "embed"

// IconPNG is the application icon shared by the desktop interface.
//
//go:embed icon.png
var IconPNG []byte
