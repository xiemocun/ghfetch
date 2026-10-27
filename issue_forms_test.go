package main

import (
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The issue forms are the only channel a stranger has to report a bug, and
// GitHub fails silently on them: a form with a duplicated id, a missing label or
// a body element it does not recognise simply does not appear in the "New issue"
// chooser. Nothing in `go test ./...` would notice, so these tests read the files
// the way GitHub does.

type issueFormElement struct {
	Type       string `yaml:"type"`
	ID         string `yaml:"id"`
	Attributes struct {
		Label       string `yaml:"label"`
		Description string `yaml:"description"`
		Value       string `yaml:"value"`
		Options     []struct {
			Label    string `yaml:"label"`
			Required bool   `yaml:"required"`
		} `yaml:"options"`
	} `yaml:"attributes"`
	Validations struct {
		Required bool `yaml:"required"`
	} `yaml:"validations"`
}

type issueForm struct {
	Name        string             `yaml:"name"`
	Description string             `yaml:"description"`
	Labels      []string           `yaml:"labels"`
	Body        []issueFormElement `yaml:"body"`
}

var knownElementTypes = map[string]bool{
	"markdown":   true,
	"input":      true,
	"textarea":   true,
	"dropdown":   true,
	"checkboxes": true,
}

func readIssueForm(t *testing.T, name string) issueForm {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(".github", "ISSUE_TEMPLATE", name))
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	var form issueForm
	if err := yaml.Unmarshal(raw, &form); err != nil {
		t.Fatalf("%s is not valid YAML: %v", name, err)
	}
	return form
}

func TestIssueFormsAreAcceptedByGitHub(t *testing.T) {
	for _, name := range []string{"bug_report.yml", "feature_request.yml"} {
		t.Run(name, func(t *testing.T) {
			form := readIssueForm(t, name)
			if strings.TrimSpace(form.Name) == "" || strings.TrimSpace(form.Description) == "" {
				t.Fatalf("%s needs a name and a description; without both GitHub hides it "+
					"from the New issue chooser", name)
			}
			// GitHub truncates the chooser description, so a long one is a
			// description nobody reads in full.
			if len(form.Description) > 200 {
				t.Errorf("%s description is %d characters, the chooser shows roughly 200",
					name, len(form.Description))
			}
			if len(form.Labels) == 0 {
				t.Errorf("%s sets no labels, so a new issue arrives untriaged", name)
			}
			if len(form.Body) == 0 {
				t.Fatalf("%s has an empty body", name)
			}

			seen := map[string]bool{}
			required := 0
			for i, el := range form.Body {
				if !knownElementTypes[el.Type] {
					t.Errorf("%s body[%d] has type %q, which is not an issue-form element",
						name, i, el.Type)
				}
				if el.Type == "markdown" {
					if strings.TrimSpace(el.Attributes.Value) == "" {
						t.Errorf("%s body[%d] is markdown with no value", name, i)
					}
					if el.Validations.Required {
						t.Errorf("%s body[%d] is markdown and cannot be required", name, i)
					}
					continue
				}
				if strings.TrimSpace(el.ID) == "" {
					t.Errorf("%s body[%d] (%s) has no id, so its answer cannot be referenced",
						name, i, el.Type)
				}
				if seen[el.ID] {
					t.Errorf("%s repeats the id %q; GitHub drops the whole form", name, el.ID)
				}
				seen[el.ID] = true
				if strings.TrimSpace(el.Attributes.Label) == "" {
					t.Errorf("%s body[%d] (%s) has no label", name, i, el.ID)
				}
				if el.Type == "checkboxes" && len(el.Attributes.Options) == 0 {
					t.Errorf("%s body[%d] (%s) has no options", name, i, el.ID)
				}
				if el.Validations.Required {
					required++
				}
			}
			if required == 0 {
				t.Errorf("%s marks nothing required, so an empty issue is accepted", name)
			}
		})
	}
}

// TestIssueFormLinksAreAbsolute guards the trap that bit the first draft of
// bug_report.yml: a relative link inside a form body resolves against the issue
// URL once the form is rendered, not against the repository, so it 404s exactly
// when a reporter is looking for the security policy.
func TestIssueFormLinksAreAbsolute(t *testing.T) {
	link := regexp.MustCompile(`\]\(([^)]+)\)`)
	for _, name := range []string{"bug_report.yml", "feature_request.yml"} {
		form := readIssueForm(t, name)
		for _, el := range form.Body {
			for _, m := range link.FindAllStringSubmatch(el.Attributes.Value, -1) {
				u, err := url.Parse(m[1])
				if err != nil || u.Scheme != "https" || u.Host == "" {
					t.Errorf("%s links to %q; form bodies need absolute https URLs", name, m[1])
				}
			}
		}
	}
}

// TestIssueTemplateConfigPointsAtSecurity keeps the private advisory form one
// click from the issue chooser. A vulnerability reported in a public issue is a
// disclosure, and the config is the only place GitHub shows that link.
func TestIssueTemplateConfigPointsAtSecurity(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(".github", "ISSUE_TEMPLATE", "config.yml"))
	if err != nil {
		t.Fatalf("reading config.yml: %v", err)
	}
	var cfg struct {
		BlankIssuesEnabled *bool `yaml:"blank_issues_enabled"`
		ContactLinks       []struct {
			Name  string `yaml:"name"`
			URL   string `yaml:"url"`
			About string `yaml:"about"`
		} `yaml:"contact_links"`
	}
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("config.yml is not valid YAML: %v", err)
	}
	if cfg.BlankIssuesEnabled == nil || *cfg.BlankIssuesEnabled {
		t.Error("config.yml must set blank_issues_enabled: false, or the forms are bypassed")
	}
	if len(cfg.ContactLinks) == 0 {
		t.Fatal("config.yml has no contact_links")
	}
	foundSecurity := false
	for _, l := range cfg.ContactLinks {
		if strings.TrimSpace(l.Name) == "" || strings.TrimSpace(l.About) == "" {
			t.Errorf("contact link %q needs a name and an about line", l.URL)
		}
		u, err := url.Parse(l.URL)
		if err != nil || u.Scheme != "https" {
			t.Errorf("contact link %q is not an absolute https URL", l.URL)
		}
		if strings.Contains(l.URL, "/security/advisories/new") {
			foundSecurity = true
		}
	}
	if !foundSecurity {
		t.Error("config.yml does not point at the private advisory form")
	}
}

// TestSecurityPolicyStaysHonest is the small part of SECURITY.md that can be
// checked mechanically. The file's value is that it names the limits of what
// ghfetch verifies — the checksum arrives through the same mirror chain as the
// asset, so it cannot prove a mirror honest — and that claim is easy to lose in
// an edit that makes the project sound stronger than it is.
func TestSecurityPolicyStaysHonest(t *testing.T) {
	raw, err := os.ReadFile("SECURITY.md")
	if err != nil {
		t.Fatalf("reading SECURITY.md: %v", err)
	}
	policy := string(raw)
	for _, want := range []struct {
		needle string
		why    string
	}{
		{"security/advisories/new", "the private reporting channel"},
		{"## Supported versions", "which versions get fixes"},
		{"same source chain", "the limits of checksum verification through a mirror"},
		{"--source direct", "the mitigation a reader can act on"},
		{"--attestation=required", "the one check a mirror cannot satisfy"},
		{"Decompression bombs", "a known gap, so it is not promised"},
	} {
		if !strings.Contains(policy, want.needle) {
			t.Errorf("SECURITY.md no longer mentions %q (%s)", want.needle, want.why)
		}
	}

	readme, err := os.ReadFile("README.md")
	if err != nil {
		t.Fatalf("reading README.md: %v", err)
	}
	if !strings.Contains(string(readme), "SECURITY.md") {
		t.Error("README.md does not link SECURITY.md, so the policy is hard to find")
	}
}
