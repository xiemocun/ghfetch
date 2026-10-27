package main

import (
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The release audit only has value if it actually runs on every published
// release, and if it compares content instead of trusting an exit status.
// Both are easy to lose silently in a YAML edit, so they are checked here.
func TestReleaseAuditWorkflowRunsOnPublish(t *testing.T) {
	raw, err := os.ReadFile(".github/workflows/verify-release.yml")
	if err != nil {
		t.Fatalf("read verify-release.yml: %v", err)
	}
	var wf struct {
		On map[string]any `yaml:"on"`
	}
	if err := yaml.Unmarshal(raw, &wf); err != nil {
		t.Fatalf("parse verify-release.yml: %v", err)
	}
	release, ok := wf.On["release"].(map[string]any)
	if !ok {
		t.Fatalf("verify-release.yml does not trigger on `release`; triggers: %v", triggerNames(wf.On))
	}
	types := toStrings(release["types"])
	if !hasString(types, "published") {
		t.Fatalf("verify-release.yml triggers on release events %v, not `published`: an audit that runs before upload cannot see the assets", types)
	}
	if _, ok := wf.On["workflow_dispatch"]; !ok {
		t.Errorf("verify-release.yml has no workflow_dispatch, so a release cannot be re-audited by hand")
	}
	if !strings.Contains(string(raw), "scripts/verify-release.sh") {
		t.Errorf("verify-release.yml no longer calls scripts/verify-release.sh")
	}
}

// A verifier that checks only that `gh attestation verify` exited 0 would pass
// on a release whose assets are not attested by this repository at all: some gh
// versions returned 0 for a missing attestation. The digest and the source
// repository have to be compared.
func TestReleaseAuditComparesContentNotExitStatus(t *testing.T) {
	raw, err := os.ReadFile("scripts/verify-release.sh")
	if err != nil {
		t.Fatalf("read verify-release.sh: %v", err)
	}
	script := string(raw)
	for _, needle := range []string{
		".digest.sha256",      // the subject digest we compare against the file
		"sourceRepositoryURI", // provenance must point at this repository
		"sourceRepositoryRef", // and at the tag, not at a branch
		`"sha256:$sha"`,       // the API's own record, which no mirror serves
		"isImmutable",         // the tag cannot be repointed later
	} {
		if !strings.Contains(script, needle) {
			t.Errorf("verify-release.sh no longer checks %q", needle)
		}
	}
	if strings.Contains(script, "gh attestation verify") &&
		!strings.Contains(script, "assert_attestation_json") {
		t.Errorf("verify-release.sh runs gh attestation verify without parsing its JSON report")
	}
}

func triggerNames(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func toStrings(v any) []string {
	switch t := v.(type) {
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case string:
		return []string{t}
	}
	return nil
}

func hasString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
