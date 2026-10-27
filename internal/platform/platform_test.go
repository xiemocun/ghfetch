package platform

import "testing"

func TestBestPicksHostAsset(t *testing.T) {
	assets := []string{
		"gh_2.103.3_linux_amd64.deb",
		"gh_2.103.3_linux_amd64.rpm",
		"gh_2.103.3_linux_amd64.tar.gz",
		"gh_2.103.3_linux_arm64.tar.gz",
		"gh_2.103.3_macOS_amd64.zip",
		"gh_2.103.3_macOS_arm64.zip",
		"gh_2.103.3_windows_amd64.zip",
		"gh_2.103.3_checksums.txt",
	}
	cases := []struct {
		target Target
		want   string
	}{
		{Target{"linux", "amd64"}, "gh_2.103.3_linux_amd64.tar.gz"},
		{Target{"linux", "arm64"}, "gh_2.103.3_linux_arm64.tar.gz"},
		{Target{"darwin", "arm64"}, "gh_2.103.3_macOS_arm64.zip"},
		{Target{"darwin", "amd64"}, "gh_2.103.3_macOS_amd64.zip"},
		{Target{"windows", "amd64"}, "gh_2.103.3_windows_amd64.zip"},
	}
	for _, c := range cases {
		got, err := Best(assets, c.target)
		if err != nil {
			t.Fatalf("Best(%s): %v", c.target, err)
		}
		if got != c.want {
			t.Errorf("Best(%s) = %q, want %q", c.target, got, c.want)
		}
	}
}

func TestBestRejectsWrongPlatform(t *testing.T) {
	assets := []string{"tool_linux_amd64.tar.gz", "tool_macos_arm64.tar.gz"}
	if got, err := Best(assets, Target{"windows", "amd64"}); err == nil {
		t.Errorf("expected an error for windows/amd64, got %q", got)
	}
}

func TestBestHandlesAliases(t *testing.T) {
	assets := []string{"tool_1.0_linux_x86_64.tar.gz", "tool_1.0_linux_aarch64.tar.gz"}
	got, err := Best(assets, Target{"linux", "amd64"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "tool_1.0_linux_x86_64.tar.gz" {
		t.Errorf("got %q", got)
	}
}

func TestScoreRejectsNonBinaryArtifacts(t *testing.T) {
	for _, name := range []string{
		"tool_2.0_linux_amd64_checksums.txt",
		"tool_2.0_linux_amd64.tar.gz.sig",
		"sha256sums.txt",
	} {
		if s := Score(name, Target{"linux", "amd64"}); s >= 0 {
			t.Errorf("Score(%q) = %d, want negative", name, s)
		}
	}
}

func TestScoreZeroForPlatformlessAssets(t *testing.T) {
	// Source tarballs carry the version but no platform token, so they must
	// never be selected over a real binary.
	for _, name := range []string{"tool-1.8.2.tar.gz", "tool-1.8.2.zip", "notes.md"} {
		if s := Score(name, Target{"linux", "amd64"}); s != 0 {
			t.Errorf("Score(%q) = %d, want 0", name, s)
		}
	}
}

func TestHasTokenBoundaries(t *testing.T) {
	cases := []struct {
		s, tok string
		want   bool
	}{
		{"tool_linux_amd64", "linux", true},
		{"toollinux_amd64", "linux", false},
		{"tool-x86_64", "x86_64", true},
		{"tool_arm64", "arm", false},
		{"tool_arm.tar.gz", "arm", true},
	}
	for _, c := range cases {
		if got := hasToken(c.s, c.tok); got != c.want {
			t.Errorf("hasToken(%q, %q) = %v, want %v", c.s, c.tok, got, c.want)
		}
	}
}

func TestNormalize(t *testing.T) {
	if got := NormalizeOS("macOS"); got != "darwin" {
		t.Errorf("NormalizeOS(macOS) = %q", got)
	}
	if got := NormalizeArch("x86_64"); got != "amd64" {
		t.Errorf("NormalizeArch(x86_64) = %q", got)
	}
	if got := NormalizeArch("aarch64"); got != "arm64" {
		t.Errorf("NormalizeArch(aarch64) = %q", got)
	}
	if got := (Target{"windows", "amd64"}).BinSuffix(); got != ".exe" {
		t.Errorf("BinSuffix() = %q", got)
	}
}
