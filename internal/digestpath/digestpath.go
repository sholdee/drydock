// Package digestpath holds the canonical path rules shared by the committed
// (gitref) and filesystem (filedigest) digest schemes. The two schemes are two
// halves of one persistent render-cache key space: their canonicalization and
// record framing must stay byte-identical, so both packages call this one.
package digestpath

import (
	"fmt"
	"hash"
	"path"
	"strings"
)

// canonical returns the canonical repository-relative form of value. label
// names the path family in error text ("git path", "filesystem path").
// allowEmpty preserves gitref's historical acceptance of "" (which cleans to
// "."); filedigest rejects empty input.
func canonical(value, label string, allowEmpty bool) (string, error) {
	if value == "" && !allowEmpty {
		return "", fmt.Errorf("%s must not be empty", label)
	}
	if strings.Contains(value, "\x00") {
		return "", fmt.Errorf("%s contains nul", label)
	}
	clean := path.Clean(strings.ReplaceAll(value, "\\", "/"))
	if clean == "." {
		return clean, nil
	}
	if strings.HasPrefix(clean, "/") || IsWindowsDrivePath(clean) || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("%s %q must be repository-relative", label, value)
	}
	return clean, nil
}

// CanonicalGitPath canonicalizes a committed-digest (gitref) path. Empty
// input is allowed and cleans to "." (historical gitref behavior).
func CanonicalGitPath(value string) (string, error) {
	return canonical(value, "git path", true)
}

// CanonicalFilesystemPath canonicalizes a filesystem-digest (filedigest)
// path. Empty input is rejected.
func CanonicalFilesystemPath(value string) (string, error) {
	return canonical(value, "filesystem path", false)
}

// IsWindowsDrivePath reports whether value begins with a drive designator.
func IsWindowsDrivePath(value string) bool {
	if len(value) < 2 || value[1] != ':' {
		return false
	}
	drive := value[0]
	return (drive >= 'A' && drive <= 'Z') || (drive >= 'a' && drive <= 'z')
}

// WriteRecord frames one digest record: each field NUL-terminated, the record
// newline-terminated. Both digest schemes use this exact framing.
func WriteRecord(digest hash.Hash, fields ...string) {
	for _, field := range fields {
		_, _ = digest.Write([]byte(field))
		_, _ = digest.Write([]byte{0})
	}
	_, _ = digest.Write([]byte{'\n'})
}
