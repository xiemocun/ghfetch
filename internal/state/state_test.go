package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDefaultPathHonorsOverrides(t *testing.T) {
	t.Setenv("GHFETCH_STATE_FILE", "/tmp/elsewhere/rec.json")
	t.Setenv("XDG_STATE_HOME", "/tmp/xdg")
	if got, err := DefaultPath(); err != nil || got != "/tmp/elsewhere/rec.json" {
		t.Fatalf("DefaultPath with %s set = %q, %v", EnvPath, got, err)
	}

	t.Setenv("GHFETCH_STATE_FILE", "")
	if got, err := DefaultPath(); err != nil || got != filepath.Join("/tmp/xdg", "ghfetch", "installed.json") {
		t.Fatalf("DefaultPath with XDG_STATE_HOME set = %q, %v", got, err)
	}

	t.Setenv("XDG_STATE_HOME", "")
	got, err := DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath with no overrides: %v", err)
	}
	if filepath.Base(got) != "installed.json" || filepath.Base(filepath.Dir(got)) != "ghfetch" {
		t.Errorf("DefaultPath = %q, want .../ghfetch/installed.json", got)
	}
}

func TestLoadMissingFileIsEmptyNotAnError(t *testing.T) {
	store := Store{Path: filepath.Join(t.TempDir(), "nested", "installed.json")}
	recs, err := store.Load()
	if err != nil {
		t.Fatalf("a missing state file must not be an error: %v", err)
	}
	if len(recs) != 0 {
		t.Errorf("got %d records, want none", len(recs))
	}
}

func TestPutKeepsOtherEntries(t *testing.T) {
	store := Store{Path: filepath.Join(t.TempDir(), "installed.json")}
	first := Record{Repo: "cli/cli", Tag: "v2.103.3", Asset: "gh.tar.gz", SHA256: "aa", InstalledAt: time.Unix(1, 0).UTC()}
	second := Record{Repo: "jqlang/jq", Tag: "jq-1.8.2", Asset: "jq-linux-amd64", SHA256: "bb", InstalledAt: time.Unix(2, 0).UTC()}
	if err := store.Put("/usr/local/bin/gh", first); err != nil {
		t.Fatalf("put first: %v", err)
	}
	if err := store.Put("/usr/local/bin/jq", second); err != nil {
		t.Fatalf("put second: %v", err)
	}

	got, ok := store.Get("/usr/local/bin/gh")
	if !ok || got.Repo != "cli/cli" || got.SHA256 != "aa" {
		t.Errorf("Get(gh) = %+v, %v; want the first record", got, ok)
	}
	recs, err := store.Load()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(recs) != 2 {
		t.Errorf("got %d records, want 2 — the second Put dropped the first", len(recs))
	}

	// Paths are keyed after cleaning, so "./x/bin" and "x/bin" are one entry.
	if _, ok := store.Get(filepath.Clean("/usr/local/bin") + "/./jq"); !ok {
		t.Error("Get should find a record through an uncleaned path")
	}
}

func TestCorruptFileIsReportedAndRecovered(t *testing.T) {
	path := filepath.Join(t.TempDir(), "installed.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := Store{Path: path}
	if _, err := store.Load(); err == nil {
		t.Fatal("a corrupt state file must be reported, not silently treated as empty")
	}

	// Put must still work: bookkeeping must never block an install.
	rec := Record{Repo: "cli/cli", Tag: "v2.103.3", SHA256: "cc"}
	if err := store.Put("/usr/local/bin/gh", rec); err != nil {
		t.Fatalf("Put over a corrupt file: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]Record
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("recovered file is not valid JSON: %v\n%s", err, data)
	}
	if decoded["/usr/local/bin/gh"].Repo != "cli/cli" {
		t.Errorf("recovered file lost the new record: %s", data)
	}
}

// TestSaveLeavesNoTempFiles pins the atomic write: a leftover temp file in the
// state directory would look like corruption to the next reader.
func TestSaveLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	store := Store{Path: filepath.Join(dir, "installed.json")}
	if err := store.Put("/usr/local/bin/gh", Record{Repo: "cli/cli", Tag: "v1"}); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "installed.json" {
			t.Errorf("unexpected leftover file %q", e.Name())
		}
	}
}
