// Package checksum parses the digest manifests projects publish alongside their
// release binaries.
package checksum

import (
	"path"
	"regexp"
	"strings"
)

var sha256Hex = regexp.MustCompile(`\b[0-9a-fA-F]{64}\b`)

var fieldSeps = strings.NewReplacer("(", " ", ")", " ", "=", " ", "*", " ")

// Parse maps lower-cased base file names to lower-case sha256 digests. It
// understands the three layouts in common use:
//
//	<hash>  <file>        sha256sum output
//	<hash> *<file>        sha256sum --binary output
//	SHA256 (<file>) = <hash>   BSD style
func Parse(data []byte) map[string]string {
	out := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		digest := sha256Hex.FindString(line)
		if digest == "" {
			continue
		}
		rest := strings.Replace(line, digest, " ", 1)
		fields := strings.Fields(fieldSeps.Replace(rest))
		if len(fields) == 0 {
			continue
		}
		name := path.Base(fields[len(fields)-1])
		out[strings.ToLower(name)] = strings.ToLower(digest)
	}
	return out
}

// Lookup finds the digest recorded for filename, ignoring case and directories.
func Lookup(table map[string]string, filename string) (string, bool) {
	digest, ok := table[strings.ToLower(path.Base(filename))]
	return digest, ok
}

// ParseSingle reads a sidecar digest file: a bare sha256, or the usual
// "hash  file" line that some projects put in <asset>.sha256.
func ParseSingle(data []byte) (string, bool) {
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if digest := sha256Hex.FindString(line); digest != "" {
			return strings.ToLower(digest), true
		}
	}
	return "", false
}