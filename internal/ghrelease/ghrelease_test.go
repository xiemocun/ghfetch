package ghrelease

import "testing"

func TestNormalizeRepo(t *testing.T) {
	cases := map[string]string{
		"cli/cli":                        "cli/cli",
		" https://github.com/cli/cli ":   "cli/cli",
		"https://github.com/cli/cli.git": "cli/cli",
		"http://github.com/cli/cli/":     "cli/cli",
	}
	for in, want := range cases {
		got, err := NormalizeRepo(in)
		if err != nil {
			t.Fatalf("NormalizeRepo(%q): %v", in, err)
		}
		if got != want {
			t.Errorf("NormalizeRepo(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeRepoRejects(t *testing.T) {
	for _, bad := range []string{"", "cli", "a/b/c", "cli/", "/cli", "github.com/cli/cli"} {
		if got, err := NormalizeRepo(bad); err == nil {
			t.Errorf("NormalizeRepo(%q) = %q, want an error", bad, got)
		}
	}
}

func TestChecksumAsset(t *testing.T) {
	rel := &Release{Assets: []Asset{
		{Name: "tool_1.0_linux_amd64.tar.gz"},
		{Name: "gh_2.0_checksums.txt"},
	}}
	if a, ok := rel.ChecksumAsset(); !ok || a.Name != "gh_2.0_checksums.txt" {
		t.Errorf("ChecksumAsset = %v, %v", a.Name, ok)
	}

	empty := &Release{Assets: []Asset{{Name: "tool_1.0_linux_amd64.tar.gz"}}}
	if _, ok := empty.ChecksumAsset(); ok {
		t.Error("unexpected checksum asset")
	}
}

func TestDigestFor(t *testing.T) {
	rel := &Release{Assets: []Asset{
		{Name: "tool_1.0_linux_amd64.tar.gz"},
		{Name: "tool_1.0_linux_amd64.tar.gz.sha256"},
		{Name: "other_1.0_darwin_arm64.tar.gz"},
		{Name: "other_1.0_darwin_arm64.tar.gz.digest"},
	}}
	if a, ok := rel.DigestFor("tool_1.0_linux_amd64.tar.gz"); !ok || a.Name != "tool_1.0_linux_amd64.tar.gz.sha256" {
		t.Errorf("DigestFor = %v, %v", a.Name, ok)
	}
	if a, ok := rel.DigestFor("other_1.0_darwin_arm64.tar.gz"); !ok || a.Name != "other_1.0_darwin_arm64.tar.gz.digest" {
		t.Errorf("DigestFor = %v, %v", a.Name, ok)
	}
	if _, ok := rel.DigestFor("missing_1.0_linux_amd64.tar.gz"); ok {
		t.Error("unexpected sidecar for a missing asset")
	}
}

func TestAssetAndNames(t *testing.T) {
	rel := &Release{Assets: []Asset{{Name: "a"}, {Name: "b"}}}
	if a, ok := rel.Asset("b"); !ok || a.Name != "b" {
		t.Error("Asset lookup failed")
	}
	if _, ok := rel.Asset("c"); ok {
		t.Error("unexpected asset")
	}
	names := rel.Names()
	if len(names) != 2 || names[0] != "a" || names[1] != "b" {
		t.Errorf("Names() = %v", names)
	}
}