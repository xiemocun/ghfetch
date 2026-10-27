// Package archive pulls a single executable out of the archive formats release
// artifacts are published in.
package archive

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"github.com/ulikunitz/xz"
)

// MaxBinarySize caps a single extracted file, so a hostile archive cannot
// exhaust memory. 512 MiB is far above any plausible CLI binary.
const MaxBinarySize = 512 << 20

// ErrNotFound is returned when the archive holds no file matching the request.
var ErrNotFound = errors.New("no matching executable in archive")

// Extract returns the name and bytes of one executable plus the mode to install
// it with. want is a file name without any directory part; when it is empty the
// largest non-document regular file is returned. name is empty for a raw
// (non-archive) asset.
func Extract(data []byte, filename, want string) (name string, body []byte, mode os.FileMode, err error) {
	low := strings.ToLower(filename)
	switch {
	case strings.HasSuffix(low, ".tar.gz"), strings.HasSuffix(low, ".tgz"):
		gz, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return "", nil, 0, fmt.Errorf("open gzip stream: %w", err)
		}
		defer gz.Close()
		return fromTar(tar.NewReader(gz), want)
	case strings.HasSuffix(low, ".tar.bz2"), strings.HasSuffix(low, ".tbz2"):
		return fromTar(tar.NewReader(bzip2.NewReader(bytes.NewReader(data))), want)
	case strings.HasSuffix(low, ".tar.xz"), strings.HasSuffix(low, ".txz"):
		xr, err := xz.NewReader(bytes.NewReader(data))
		if err != nil {
			return "", nil, 0, fmt.Errorf("open xz stream: %w", err)
		}
		return fromTar(tar.NewReader(xr), want)
	case strings.HasSuffix(low, ".zip"):
		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return "", nil, 0, fmt.Errorf("open zip: %w", err)
		}
		return fromZip(zr, want)
	case strings.HasSuffix(low, ".gz"):
		gz, err := gzip.NewReader(bytes.NewReader(data))
		if err != nil {
			return "", nil, 0, fmt.Errorf("open gzip stream: %w", err)
		}
		defer gz.Close()
		untar, err := io.ReadAll(io.LimitReader(gz, MaxBinarySize))
		if err != nil {
			return "", nil, 0, fmt.Errorf("decompress: %w", err)
		}
		return "", untar, 0o755, nil
	}
	// Anything else is assumed to already be the raw executable.
	if len(data) > MaxBinarySize {
		return "", nil, 0, fmt.Errorf("asset is %d bytes, over the %d byte limit", len(data), MaxBinarySize)
	}
	return "", data, 0o755, nil
}

func fromTar(tr *tar.Reader, want string) (string, []byte, os.FileMode, error) {
	var bestName string
	var bestBody []byte
	var bestMode os.FileMode
	var seen []string

	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return "", nil, 0, fmt.Errorf("read tar: %w", err)
		}
		if !hdr.FileInfo().Mode().IsRegular() || hdr.Size > MaxBinarySize || !safeName(hdr.Name) {
			continue
		}
		name := path.Base(hdr.Name)
		seen = append(seen, name)
		body, err := io.ReadAll(io.LimitReader(tr, MaxBinarySize))
		if err != nil {
			return "", nil, 0, fmt.Errorf("read %s: %w", name, err)
		}
		if want != "" {
			if !nameMatch(name, want) {
				continue
			}
			return name, body, execMode(hdr.FileInfo().Mode()), nil
		}
		if !isBinaryCandidate(name) {
			continue
		}
		if len(body) > len(bestBody) {
			bestName, bestBody, bestMode = name, body, execMode(hdr.FileInfo().Mode())
		}
	}
	if bestBody == nil {
		return "", nil, 0, notFound(seen)
	}
	return bestName, bestBody, bestMode, nil
}

func fromZip(zr *zip.Reader, want string) (string, []byte, os.FileMode, error) {
	var bestName string
	var bestBody []byte
	var bestMode os.FileMode
	var seen []string

	for _, f := range zr.File {
		if f.FileInfo().IsDir() || f.UncompressedSize64 > MaxBinarySize || !safeName(f.Name) {
			continue
		}
		name := path.Base(f.Name)
		seen = append(seen, name)
		rc, err := f.Open()
		if err != nil {
			return "", nil, 0, fmt.Errorf("open %s: %w", name, err)
		}
		body, err := io.ReadAll(io.LimitReader(rc, MaxBinarySize))
		rc.Close()
		if err != nil {
			return "", nil, 0, fmt.Errorf("read %s: %w", name, err)
		}
		if want != "" {
			if !nameMatch(name, want) {
				continue
			}
			return name, body, execMode(f.Mode()), nil
		}
		if !isBinaryCandidate(name) {
			continue
		}
		if len(body) > len(bestBody) {
			bestName, bestBody, bestMode = name, body, execMode(f.Mode())
		}
	}
	if bestBody == nil {
		return "", nil, 0, notFound(seen)
	}
	return bestName, bestBody, bestMode, nil
}

// notFound wraps ErrNotFound with the archive index, so the error says what was
// actually inside instead of only what was missing.
func notFound(seen []string) error {
	if len(seen) == 0 {
		return ErrNotFound
	}
	const max = 8
	shown := seen
	if len(shown) > max {
		shown = shown[:max]
	}
	return fmt.Errorf("%w (archive contains: %s)", ErrNotFound, strings.Join(shown, ", "))
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
	if name == want {
		return true
	}
	// "ghfetch_linux_amd64" should satisfy a request for "ghfetch".
	for _, sep := range []string{"_", "-", "."} {
		if strings.HasPrefix(name, want+sep) {
			return true
		}
	}
	return false
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
