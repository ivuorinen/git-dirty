package git

import (
	"fmt"
	"strings"
)

// knownExtensions are the repository extensions this package can read with.
// Like git, a version 1 repository naming any other extension is refused:
// its on-disk data may not mean what a reader without the extension assumes.
var knownExtensions = map[string]bool{
	"objectformat":    true,
	"worktreeconfig":  true,
	"partialclone":    true,
	"noop":            true,
	"preciousobjects": true,
}

// checkFormat validates core.repositoryFormatVersion and the extensions
// section, and returns the object format. extensions maps lowercase
// extension names to values. Version 0 ignores extensions, as git does.
func checkFormat(version string, extensions map[string]string) (Format, error) {
	switch strings.TrimSpace(version) {
	case "", "0":
		return SHA1, nil
	case "1":
	default:
		return Format{}, fmt.Errorf("unsupported repository format version %q", version)
	}
	for name := range extensions {
		if !knownExtensions[name] {
			return Format{}, fmt.Errorf("unsupported repository extension %q", name)
		}
	}
	switch v := strings.ToLower(extensions["objectformat"]); v {
	case "", "sha1":
		return SHA1, nil
	case "sha256":
		return SHA256, nil
	default:
		return Format{}, fmt.Errorf("unsupported object format %q", v)
	}
}
