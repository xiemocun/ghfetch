// Package platform maps a host OS/architecture pair onto the token vocabulary
// release assets use in their filenames, and scores candidate assets.
package platform

import (
	"fmt"
	"runtime"
	"sort"
	"strings"
)

// Target is a canonical build target, for example linux/amd64 or darwin/arm64.
type Target struct {
	OS   string
	Arch string
}

func (t Target) String() string { return t.OS + "/" + t.Arch }

// BinSuffix is the suffix executables carry on the target platform.
func (t Target) BinSuffix() string {
	if t.OS == "windows" {
		return ".exe"
	}
	return ""
}

// Detect reports the running host as a Target.
func Detect() Target {
	return Target{OS: NormalizeOS(runtime.GOOS), Arch: NormalizeArch(runtime.GOARCH)}
}

// NormalizeOS folds the many spellings of an OS into one canonical name.
func NormalizeOS(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "macos", "mac", "osx", "apple", "darwin":
		return "darwin"
	case "win", "win32", "win64", "windows", "windowsnt":
		return "windows"
	case "freebsd":
		return "freebsd"
	case "linux":
		return "linux"
	}
	return strings.ToLower(strings.TrimSpace(s))
}

// NormalizeArch folds the many spellings of an architecture into one canonical
// name. Note that "x86" is deliberately not an alias: it is a prefix of
// "x86_64", and the boundary check would match it by accident.
func NormalizeArch(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "x86_64", "x86-64", "x64", "amd64":
		return "amd64"
	case "aarch64", "arm64":
		return "arm64"
	case "i386", "i686", "386", "x86":
		return "386"
	case "armv7", "armv6", "armhf", "arm":
		return "arm"
	}
	return strings.ToLower(strings.TrimSpace(s))
}

var osAliases = map[string][]string{
	"linux":   {"linux"},
	"darwin":  {"darwin", "macos", "osx", "apple"},
	"windows": {"windows", "win32", "win64", "win"},
	"freebsd": {"freebsd"},
}

var archAliases = map[string][]string{
	"amd64": {"amd64", "x86_64", "x86-64", "x64"},
	"arm64": {"arm64", "aarch64"},
	"386":   {"386", "i386", "i686"},
	"arm":   {"armv7", "armv6", "armhf", "arm"},
}

// nonBinary marks artifacts that are never the executable we want.
var nonBinary = []string{
	"checksums", "checksum", "sha256sums", "sha256sum", "sha256", "sha512",
	".sig", ".asc", ".sbom", "provenance",
}

// Score rates how well an asset name matches the target. A negative score means
// the asset belongs to a different platform and must never be chosen.
func Score(name string, t Target) int {
	low := strings.ToLower(name)
	score := 0
	matchedOS, matchedArch := false, false

	for os, aliases := range osAliases {
		for _, a := range aliases {
			if !hasToken(low, a) {
				continue
			}
			if NormalizeOS(os) != NormalizeOS(t.OS) {
				return -1
			}
			score += 100
			matchedOS = true
			break
		}
	}
	for arch, aliases := range archAliases {
		for _, a := range aliases {
			if !hasToken(low, a) {
				continue
			}
			if NormalizeArch(arch) != NormalizeArch(t.Arch) {
				return -1
			}
			score += 50
			matchedArch = true
			break
		}
	}
	for _, junk := range nonBinary {
		if strings.Contains(low, junk) {
			return -1
		}
	}
	// An asset that names no platform at all is a source tarball, a signature,
	// or something else that is never the binary we want.
	if !matchedOS && !matchedArch {
		return 0
	}

	switch {
	case strings.HasSuffix(low, ".tar.gz"), strings.HasSuffix(low, ".tgz"),
		strings.HasSuffix(low, ".tar.xz"), strings.HasSuffix(low, ".txz"),
		strings.HasSuffix(low, ".tar.bz2"), strings.HasSuffix(low, ".tbz2"),
		strings.HasSuffix(low, ".zip"):
		score += 10
	case strings.HasSuffix(low, ".deb"), strings.HasSuffix(low, ".rpm"),
		strings.HasSuffix(low, ".msi"), strings.HasSuffix(low, ".apk"),
		strings.HasSuffix(low, ".dmg"), strings.HasSuffix(low, ".pkg"):
		score -= 30
	}
	if strings.Contains(low, "source") || strings.Contains(low, "src.") {
		score -= 20
	}
	return score
}

// Best picks the highest scoring asset. Ties go to the shorter name, then to
// the lexicographically smaller one, so the choice is deterministic.
func Best(names []string, t Target) (string, error) {
	type candidate struct {
		name  string
		score int
	}
	var candidates []candidate
	for _, n := range names {
		if s := Score(n, t); s > 0 {
			candidates = append(candidates, candidate{name: n, score: s})
		}
	}
	if len(candidates) == 0 {
		return "", fmt.Errorf("no asset matches %s among %d candidates", t, len(names))
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].score != candidates[j].score {
			return candidates[i].score > candidates[j].score
		}
		if len(candidates[i].name) != len(candidates[j].name) {
			return len(candidates[i].name) < len(candidates[j].name)
		}
		return candidates[i].name < candidates[j].name
	})
	return candidates[0].name, nil
}

// hasToken reports whether tok occurs in s as a whole token, where a token is
// delimited by any non-alphanumeric byte or the string edge.
func hasToken(s, tok string) bool {
	for from := 0; ; {
		i := strings.Index(s[from:], tok)
		if i < 0 {
			return false
		}
		start := from + i
		end := start + len(tok)
		leftOK := start == 0 || isBoundary(s[start-1])
		rightOK := end == len(s) || isBoundary(s[end])
		if leftOK && rightOK {
			return true
		}
		from = start + 1
	}
}

func isBoundary(b byte) bool {
	return b == '-' || b == '_' || b == '.' || b == ' ' || b == '/' || b == '+'
}