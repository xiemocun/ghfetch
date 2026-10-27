// Package ghrelease is a deliberately small GitHub Releases API client: one
// call, no pagination, no caching.
package ghrelease

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"
)

// DefaultBaseURL is the public GitHub API.
const DefaultBaseURL = "https://api.github.com"

// Asset is one downloadable file attached to a release.
type Asset struct {
	Name      string `json:"name"`
	URL       string `json:"browser_download_url"`
	Size      int64  `json:"size"`
	Downloads int    `json:"download_count"`
}

// Release is the subset of a GitHub release that matters here.
type Release struct {
	Tag        string  `json:"tag_name"`
	Name       string  `json:"name"`
	Prerelease bool    `json:"prerelease"`
	Draft      bool    `json:"draft"`
	Assets     []Asset `json:"assets"`
}

// Names lists the asset file names in release order.
func (r *Release) Names() []string {
	names := make([]string, 0, len(r.Assets))
	for _, a := range r.Assets {
		names = append(names, a.Name)
	}
	return names
}

// Asset looks up an asset by exact file name.
func (r *Release) Asset(name string) (Asset, bool) {
	for _, a := range r.Assets {
		if a.Name == name {
			return a, true
		}
	}
	return Asset{}, false
}

// ChecksumAsset returns the asset that carries the digest manifest, if the
// release publishes one.
func (r *Release) ChecksumAsset() (Asset, bool) {
	for _, a := range r.Assets {
		name := strings.ToLower(a.Name)
		if strings.Contains(name, "checksum") ||
			strings.Contains(name, "sha256sum") ||
			strings.Contains(name, "sha256sums") {
			return a, true
		}
	}
	return Asset{}, false
}

// sidecarSuffixes are per-asset digest files, which projects publish when they
// do not ship a whole manifest.
var sidecarSuffixes = []string{".sha256", ".sha256sum", ".sha256.txt", ".digest"}

// DigestFor returns the sidecar asset that carries the digest of name, if one
// exists. Candidates are the full name first, then the name without extension.
func (r *Release) DigestFor(name string) (Asset, bool) {
	stems := []string{strings.ToLower(name)}
	if i := strings.LastIndex(stems[0], "."); i > 0 {
		stems = append(stems, stems[0][:i])
	}
	for _, stem := range stems {
		for _, suffix := range sidecarSuffixes {
			for _, a := range r.Assets {
				if strings.ToLower(a.Name) == stem+suffix {
					return a, true
				}
			}
		}
	}
	return Asset{}, false
}

// Client talks to the Releases API.
type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// New returns a client that authenticates with GH_TOKEN or GITHUB_TOKEN when
// either is set.
func New() *Client {
	return &Client{
		BaseURL: DefaultBaseURL,
		Token:   tokenFromEnv(),
		HTTP:    &http.Client{Timeout: 60 * time.Second},
	}
}

func tokenFromEnv() string {
	for _, key := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	return ""
}

var repoPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

// NormalizeRepo accepts owner/repo and the usual github.com URL spellings.
func NormalizeRepo(s string) (string, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "https://github.com/")
	s = strings.TrimPrefix(s, "http://github.com/")
	s = strings.TrimSuffix(s, "/")
	s = strings.TrimSuffix(s, ".git")
	if !repoPattern.MatchString(s) {
		return "", fmt.Errorf("expected owner/repo, got %q", s)
	}
	return s, nil
}

// Release fetches the latest release, or the one identified by tag.
func (c *Client) Release(ctx context.Context, repo, tag string) (*Release, error) {
	repo, err := NormalizeRepo(repo)
	if err != nil {
		return nil, err
	}
	url := c.BaseURL + "/repos/" + repo + "/releases/latest"
	if tag != "" {
		url = c.BaseURL + "/repos/" + repo + "/releases/tags/" + tag
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "ghfetch")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("query %s: %w", repo, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		if tag != "" {
			return nil, fmt.Errorf("%s has no release tagged %q", repo, tag)
		}
		return nil, fmt.Errorf("%s has no published release", repo)
	case http.StatusForbidden, http.StatusTooManyRequests:
		return nil, fmt.Errorf("GitHub API rate limit hit for %s; set GH_TOKEN to raise it", repo)
	default:
		return nil, fmt.Errorf("query %s: %s", repo, resp.Status)
	}

	var rel Release
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, fmt.Errorf("decode release for %s: %w", repo, err)
	}
	return &rel, nil
}
