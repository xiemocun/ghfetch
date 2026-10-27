// Package state remembers which release ghfetch put on disk where, so that
// installing the same tag twice can be a no-op instead of another download.
//
// The bookkeeping is best effort by design: a missing, unreadable or corrupt
// state file must never block an install, because the worst case of losing it
// is downloading a binary that was already installed. That is why every error
// here is reported to the caller to print as a warning rather than escalated
// into a failed install.
package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// EnvPath overrides the location of the state file. Tests use it; users with a
// non-standard layout can too.
const EnvPath = "GHFETCH_STATE_FILE"

// Record is one binary ghfetch installed.
type Record struct {
	Repo string `json:"repo"`
	Tag  string `json:"tag"`
	// Path is where the executable was written and Binary is its file name.
	// Both are recorded because the release archive often names its member
	// differently from the repository (ripgrep publishes rg), and that name is
	// only known once the archive has been fetched — so it cannot be derived
	// from the repository when the next run decides whether to skip.
	Path   string `json:"path"`
	Binary string `json:"binary"`
	Asset  string `json:"asset"`
	// SHA256 is the digest of the installed executable itself, not of the
	// downloaded asset. The asset may be an archive, and the member extracted
	// from it is what ends up on PATH, so recording the asset digest would make
	// the local file impossible to compare against.
	SHA256      string    `json:"sha256"`
	InstalledAt time.Time `json:"installed_at"`
}

// Key identifies an install: one repository in one destination directory.
// Keying on the resulting file path instead would miss every project whose
// archive member is not named after the repository.
func Key(repo, destDir string) string {
	return repo + "@" + filepath.Clean(destDir)
}

// Store is a JSON object keyed by Key(repo, destination directory).
type Store struct {
	Path string
}

// DefaultPath returns $GHFETCH_STATE_FILE, else
// $XDG_STATE_HOME/ghfetch/installed.json, else ~/.local/state/ghfetch/installed.json.
func DefaultPath() (string, error) {
	if p := strings.TrimSpace(os.Getenv(EnvPath)); p != "" {
		return p, nil
	}
	base := strings.TrimSpace(os.Getenv("XDG_STATE_HOME"))
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("locate home directory for the install record: %w", err)
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "ghfetch", "installed.json"), nil
}

// Load reads every record. A missing or empty file is an empty set, not an
// error; a file that cannot be parsed is.
func (s Store) Load() (map[string]Record, error) {
	data, err := os.ReadFile(s.Path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return map[string]Record{}, nil
		}
		return map[string]Record{}, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return map[string]Record{}, nil
	}
	recs := map[string]Record{}
	if err := json.Unmarshal(data, &recs); err != nil {
		return nil, fmt.Errorf("parse %s: %w", s.Path, err)
	}
	if recs == nil {
		recs = map[string]Record{}
	}
	return recs, nil
}

// Put records one installed binary, preserving the other entries. A state file
// that cannot be parsed is replaced rather than appended to: keeping a corrupt
// file forever is worse than losing the history it no longer carries.
func (s Store) Put(binPath string, rec Record) error {
	recs, err := s.Load()
	if err != nil {
		recs = map[string]Record{}
	}
	recs[filepath.Clean(binPath)] = rec
	return s.save(recs)
}

// Get looks up one installed binary.
func (s Store) Get(binPath string) (Record, bool) {
	recs, err := s.Load()
	if err != nil {
		return Record{}, false
	}
	rec, ok := recs[filepath.Clean(binPath)]
	return rec, ok
}

// save writes the whole map through a temp file and a rename, so a process that
// dies mid-write leaves the previous file intact instead of a truncated one.
func (s Store) save(recs map[string]Record) error {
	dir := filepath.Dir(s.Path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	data, err := json.MarshalIndent(recs, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(dir, ".installed-*")
	if err != nil {
		return fmt.Errorf("write %s: %w", s.Path, err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write %s: %w", tmpName, err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmpName, err)
	}
	if err := os.Rename(tmpName, s.Path); err != nil {
		return fmt.Errorf("replace %s: %w", s.Path, err)
	}
	return nil
}
