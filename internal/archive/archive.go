// Package archive pulls a single executable out of the archive formats release
// artifacts are published in.
package archive

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
)

// MaxBinarySize caps a single extracted file, so a hostile archive cannot
// exhaust memory. 512 MiB is far above any plausible CLI binary.
const MaxBinarySize = 512 << 20

// ErrNotFound is returned when the archive holds no file matching the request.
var ErrNotFound = errors.New("no matching executable in archive")

// Extract returns the bytes of one executable plus the mode to install it with.
// want is a file name without any directory part; when it is empty the largest
// non-document regular file is returned.
func Extract(data []byte, filename, want string) ([]byte, os.FileMode, error) {
	low := strings.ToLower(filename)
	switch {
	case strings.HasSuffix(low, ".tar.gz"), strings.HasSuffix(low, ".tgz"):
		gz, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, 0, fmt.Errorf("open gzip stream: %w", err)
		}
		defer gz.Close()
		return fromTar(tar.NewReader(gz), want)
	case strings.HasSuffix(low, ".zip"):
		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return nil, 0, fmt.Errorf("open zip: %w", err)
		}
		return fromZip(zr, want)
	case strings.HasSuffix(low, ".gz"):
		gz, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, 0, fmt.Errorf("open gzip stream: %w", err)
		}
		defer gz.Close()
		body, err := io.ReadAll(io.LimitReader(gz, MaxBinarySize))
		if err != nil {
			return nil, 0, fmt.Errorf("decompress: %w", err)
		}
		return body, 0o755, nil
	}
	// Anything else is assumed to already be the raw executable.
	if len(data) > MaxBinarySize {
		return nil, 0, fmt.Errorf("asset is %d bytes, over the %d byte limit", len(data), MaxBinarySize)
	}
	return data, 0o755, nil
}

func fromTar(tr *tar.Reader, want string) ([]byte, os.FileMode, error) {
	var best []byte
	var bestMode os.FileMode
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, 0, fmt.Errorf("read tar: %w", err)
		}
		if !hdr.FileInfo().Mode().IsRegular() || hdr.Size > MaxBinarySize {
			continue
		}
		name := path.Base(hdr.Name)
		if !safeName(hdr.Name) {
			continue
		}
		body, err := io.ReadAll(io.LimitReader(tr, MaxBinarySize))
		if err != nil {
			return nil, 0, fmt.Errorf("read %s: %w", name, err)
		}
		if want != "" {
			if !nameMatch(name, want) {
				continue
			}
			return body, execMode(hdr.FileInfo().Mode()), nil
		}
		if !isBinaryCandidate(name) {
			continue
		}
		if len(body) > len(best) {
			best, bestMode = body, execMode(hdr.FileInfo().Mode())
		}
	}
	if best == nil {
		return nil, 0, ErrNotFound
	}
	return best, bestMode, nil
}

func fromZip(zr *zip.Reader, want string) ([]byte, os.FileMode, error) {
	var best []byte
	var bestMode os.FileMode
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || f.UncompressedSize64 > MaxBinarySize {
			continue
		}
		if !safeName(f.Name) {
			continue
		}
		name := path.Base(f.Name)
		rc, err := f.Open()
		if err != nil {
			return nil, 0, fmt.Errorf("open %s: %w", name, err)
		}
		body, err := io.ReadAll(io.LimitReader(rc, MaxBinarySize))
		rc.Close()
		if err != nil {
			return nil, 0, fmt.Errorf("read %s: %w", name, err)
		}
		if want != "" {
			if !nameMatch(name, want) {
				continue
			}
			return body, execMode(f.Mode()), nil
		}
		if !isBinaryCandidate(name) {
			continue
		}
		if len(body) > len(best) {
			best, bestMode = body, execMode(f.Mode())
		}
	}
	if best == nil {
		return nil, 0, ErrNotFound
	}
	return best, bestMode, nil
}

// safeName rejects absolute paths and any traversal component, so a hostile
// archive cannot influence where bytes are written.
func safeName(p string) bool {
	if strings.HasPrefix(p, "/") || strings.HasPrefix(p, `\`) || strings.Contains(p, "..") {
		return false
	}
	return path.Clean(p) != "." && path.Clean(p) != ".."
}

func nameMatch(name, want string) bool {
	name = strings.TrimSuffix(name, ".exe")
	want = strings.TrimSuffix(want, ".exe")
	return name == want
}

func isBinaryCandidate(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".md", ".txt", ".json", ".yaml", ".yml", ".html", ".1", ".ps1", ".bat", ".sh":
		return false
	}
	return true
}

func execMode(m os.FileMode) os.FileMode {
	if m.Perm()&0o111 == 0 {
		return 0o755
	}
	return m.Perm()
}