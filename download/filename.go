package download

import (
	"openDL/pathrec"
	"path/filepath"
	"strings"
)

// preserveExtension applies the source suffix only when the server supplies one.
func preserveExtension(name, original string) string {
	name = pathrec.SanitizeFilename(name)
	suffix := filepath.Ext(original)
	if suffix == "" || suffix == "." {
		return name
	}
	existing := filepath.Ext(name)
	if strings.EqualFold(existing, suffix) {
		return name
	}
	if existing != "" && len(existing) < len(name) {
		name = strings.TrimSuffix(name, existing)
	}
	return pathrec.SanitizeFilename(name + suffix)
}
