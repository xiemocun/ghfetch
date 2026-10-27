package archive

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
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
	body, mode, err := Extract(blob, "tool_1.0_linux_amd64.tar.gz", "tool")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if string(body) != "#!/bin/sh\necho hi\n" {
		t.Errorf("body = %q", body)
	}
	if mode&0o111 == 0 {
		t.Errorf("mode %v is not executable", mode)
	}
}

func TestExtractTarGzAutoSelect(t *testing.T) {
	blob := tarGz(t, []entry{
		{"tool_1.0/README.md", "docs"},
		{"tool_1.0/tool", "binary-content"},
	})
	body, _, err := Extract(blob, "tool_1.0_linux_amd64.tar.gz", "")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if string(body) != "binary-content" {
		t.Errorf("picked %q, want the binary", body)
	}
}

func TestExtractZip(t *testing.T) {
	blob := zipFile(t, []entry{
		{"tool.exe", "windows-binary"},
		{"CHANGELOG.md", "notes"},
	})
	body, _, err := Extract(blob, "tool_windows_amd64.zip", "tool.exe")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if string(body) != "windows-binary" {
		t.Errorf("body = %q", body)
	}
}

func TestExtractRawBinary(t *testing.T) {
	body, mode, err := Extract([]byte("raw"), "tool_linux_amd64", "tool")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if string(body) != "raw" || mode != 0o755 {
		t.Errorf("got %q mode %v", body, mode)
	}
}

func TestExtractMissing(t *testing.T) {
	blob := tarGz(t, []entry{{"tool_1.0/other", "x"}})
	if _, _, err := Extract(blob, "tool_1.0_linux_amd64.tar.gz", "tool"); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestExtractRejectsTraversal(t *testing.T) {
	blob := tarGz(t, []entry{
		{"../../etc/passwd", "pwned"},
		{"tool_1.0/tool", "real"},
	})
	body, _, err := Extract(blob, "tool_1.0_linux_amd64.tar.gz", "")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if string(body) != "real" {
		t.Errorf("traversal entry was selected: %q", body)
	}
}