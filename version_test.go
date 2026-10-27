package main

import (
	"regexp"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
)

// TestResolveVersion pins the rule that made `ghfetch version` wrong for every
// binary built with `go install`: the version used to be a constant compiled
// into the source, so it answered "0.8.0" no matter which tag Go had just
// fetched. The constant is gone; the module version Go embeds is the fallback.
func TestResolveVersion(t *testing.T) {
	buildInfo := func(v string) *debug.BuildInfo {
		return &debug.BuildInfo{Main: debug.Module{Version: v}}
	}
	cases := []struct {
		name        string
		ldflags     string
		buildInfo   *debug.BuildInfo
		buildInfoOK bool
		want        string
	}{
		{"release build wins over the module version", "0.8.4", buildInfo("v0.8.0"), true, "0.8.4"},
		{"a leading v in the ldflag is stripped", "v0.8.4", buildInfo("v0.8.0"), true, "0.8.4"},
		{"go install reports the tag it fetched", "", buildInfo("v0.8.3"), true, "0.8.3"},
		{"a checkout build says dev, not a version", "", buildInfo("(devel)"), true, "dev"},
		{"an empty module version says dev", "", buildInfo(""), true, "dev"},
		{"missing build info says dev", "", nil, false, "dev"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveVersion(c.ldflags, c.buildInfo, c.buildInfoOK); got != c.want {
				t.Fatalf("resolveVersion(%q, %v, %v) = %q, want %q",
					c.ldflags, c.buildInfo, c.buildInfoOK, got, c.want)
			}
		})
	}
}

// TestVersionIsNotAConstantInTheSource is the regression guard: the fallback is
// only reachable while `version` is empty, so a future release that hardcodes
// the number again is caught here instead of in a bug report that says "0.8.0"
// for a binary built at 0.9.1.
func TestVersionIsNotAConstantInTheSource(t *testing.T) {
	if version != "" {
		t.Fatalf("main.version is compiled in as %q; leave it empty and set it with "+
			"-X main.version at release time, or `go install` users get a stale number", version)
	}
}

// TestVersionLineNamesTheToolchain checks the shape of `ghfetch version`. The Go
// version is not decoration: the trust store is compiled in, so the same ghfetch
// release built by a different toolchain trusts a different set of CAs. A bug
// report without that line can describe a binary nobody else can reproduce.
func TestVersionLineNamesTheToolchain(t *testing.T) {
	line := versionString()
	pattern := regexp.MustCompile(`^ghfetch \S+ \(go[0-9][^,]*, [^)]+\)$`)
	if !pattern.MatchString(line) {
		t.Fatalf("version line %q does not match %s", line, pattern)
	}
	if !strings.Contains(line, runtime.Version()) {
		t.Fatalf("version line %q does not name the toolchain %s", line, runtime.Version())
	}
	if strings.TrimSpace(strings.TrimPrefix(line, "ghfetch")) == "" {
		t.Fatalf("version line %q has an empty version", line)
	}
}
