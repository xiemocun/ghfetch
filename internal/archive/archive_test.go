package archive

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"os"
	"strings"
	"testing"
)

type entry struct {
	name string
	body string
}

func tarGz(t *testing.T, entries []entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		hdr := &tar.Header{
			Name:     e.name,
			Mode:     0o755,
			Size:     int64(len(e.body)),
			Typeflag: tar.TypeReg,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("write header: %v", err)
		}
		if _, err := tw.Write([]byte(e.body)); err != nil {
			t.Fatalf("write body: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("close gzip: %v", err)
	}
	return buf.Bytes()
}

func zipFile(t *testing.T, entries []entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		w, err := zw.Create(e.name)
		if err != nil {
			t.Fatalf("create %s: %v", e.name, err)
		}
		if _, err := w.Write([]byte(e.body)); err != nil {
			t.Fatalf("write %s: %v", e.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}

func TestExtractTarGzByName(t *testing.T) {
	blob := tarGz(t, []entry{
		{"tool_1.0/LICENSE", "license text"},
		{"tool_1.0/README.md", "docs"},
		{"tool_1.0/tool", "#!/bin/sh\necho hi\n"},
	})
	name, body, mode, err := Extract(blob, "tool_1.0_linux_amd64.tar.gz", "tool")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if name != "tool" {
		t.Errorf("name = %q", name)
	}
	if string(body) != "#!/bin/sh\necho hi\n" {
		t.Errorf("body = %q", body)
	}
	if mode&0o111 == 0 {
		t.Errorf("mode %v is not executable", mode)
	}
}

// TestExtractPrefixedName covers binaries renamed with a platform suffix inside
// the archive, which is what a plain `go build -o ghfetch_linux_amd64` produces.
func TestExtractPrefixedName(t *testing.T) {
	blob := tarGz(t, []entry{{"ghfetch_linux_amd64", "binary"}})
	name, body, _, err := Extract(blob, "ghfetch_0.1.0_linux_amd64.tar.gz", "ghfetch")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if name != "ghfetch_linux_amd64" || string(body) != "binary" {
		t.Errorf("picked %q (%q)", name, body)
	}
}

func TestExtractTarGzAutoSelect(t *testing.T) {
	blob := tarGz(t, []entry{
		{"tool_1.0/README.md", "docs"},
		{"tool_1.0/tool", "binary-content"},
	})
	name, body, _, err := Extract(blob, "tool_1.0_linux_amd64.tar.gz", "")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if string(body) != "binary-content" {
		t.Errorf("picked %q, want the binary", body)
	}
	if name != "tool" {
		t.Errorf("name = %q", name)
	}
}

func TestExtractZip(t *testing.T) {
	blob := zipFile(t, []entry{
		{"tool.exe", "windows-binary"},
		{"CHANGELOG.md", "notes"},
	})
	name, body, _, err := Extract(blob, "tool_windows_amd64.zip", "tool.exe")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if name != "tool.exe" || string(body) != "windows-binary" {
		t.Errorf("got %q (%q)", name, body)
	}
}

func TestExtractTarBz2(t *testing.T) {
	blob, err := os.ReadFile("testdata/tool.tar.bz2")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	name, body, mode, err := Extract(blob, "tool_1.0_linux_amd64.tar.bz2", "tool")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if name != "tool" || string(body) != "#!/bin/sh\necho from-bzip2-or-xz\n" {
		t.Errorf("got %q (%q)", name, body)
	}
	if mode&0o111 == 0 {
		t.Errorf("mode %v is not executable", mode)
	}
}

func TestExtractTarXz(t *testing.T) {
	blob, err := os.ReadFile("testdata/tool.tar.xz")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	name, body, _, err := Extract(blob, "tool_1.0_linux_amd64.tar.xz", "tool")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if name != "tool" || string(body) != "#!/bin/sh\necho from-bzip2-or-xz\n" {
		t.Errorf("got %q (%q)", name, body)
	}
}

func TestExtractTarXzAutoSelect(t *testing.T) {
	blob, err := os.ReadFile("testdata/tool.tar.xz")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	// No --bin: the README must not win over the executable.
	_, body, _, err := Extract(blob, "tool_1.0_linux_amd64.tar.xz", "")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if strings.Contains(string(body), "# docs") {
		t.Errorf("picked the README: %q", body)
	}
}

func TestExtractTarXzRejectsGarbage(t *testing.T) {
	if _, _, _, err := Extract([]byte("not an xz stream"), "tool.tar.xz", "tool"); err == nil {
		t.Fatal("expected an error for a corrupt xz stream")
	}
}

func TestExtractRawBinary(t *testing.T) {
	_, body, mode, err := Extract([]byte("raw"), "tool_linux_amd64", "tool")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if string(body) != "raw" || mode != 0o755 {
		t.Errorf("got %q mode %v", body, mode)
	}
}

func TestExtractMissingReportsIndex(t *testing.T) {
	blob := tarGz(t, []entry{{"tool_1.0/other", "x"}})
	_, _, _, err := Extract(blob, "tool_1.0_linux_amd64.tar.gz", "tool")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
	if want := "other"; !strings.Contains(err.Error(), want) {
		t.Errorf("error %q should list the archive contents", err)
	}
}

func TestExtractRejectsTraversal(t *testing.T) {
	blob := tarGz(t, []entry{
		{"../../etc/passwd", "pwned"},
		{"tool_1.0/tool", "real"},
	})
	_, body, _, err := Extract(blob, "tool_1.0_linux_amd64.tar.gz", "")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if string(body) != "real" {
		t.Errorf("traversal entry was selected: %q", body)
	}
}