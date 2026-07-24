package scan

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	maxZipEntries = 20000
	maxZipTotal   = 1 << 30 // uncompressed cap against zip bombs
	maxZipPerFile = 128 << 20
)

func isZipInput(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ".zip")
}

// Refuses Zip Slip (non-local paths), symlinks, and oversized archives.
// Caller must invoke cleanup after the scan.
func extractZip(zipPath string) (dir string, cleanup func(), err error) {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return "", nil, fmt.Errorf("open zip %s: %w", zipPath, err)
	}
	defer func() { _ = r.Close() }()

	dir, err = os.MkdirTemp("", "lathe-scan-")
	if err != nil {
		return "", nil, err
	}
	cleanup = func() { _ = os.RemoveAll(dir) }

	if len(r.File) > maxZipEntries {
		cleanup()
		return "", nil, fmt.Errorf("zip %s has too many entries (%d)", zipPath, len(r.File))
	}

	var total int64
	for _, f := range r.File {
		if !filepath.IsLocal(f.Name) {
			continue
		}
		info := f.FileInfo()
		target := filepath.Join(dir, filepath.FromSlash(f.Name))
		if info.IsDir() {
			_ = os.MkdirAll(target, 0o755)
			continue
		}
		// Never materialize symlinks or special files.
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			continue
		}
		if int64(f.UncompressedSize64) > maxZipPerFile {
			cleanup()
			return "", nil, fmt.Errorf("zip entry %s exceeds %d bytes", f.Name, maxZipPerFile)
		}
		total += int64(f.UncompressedSize64)
		if total > maxZipTotal {
			cleanup()
			return "", nil, fmt.Errorf("zip %s exceeds %d bytes uncompressed", zipPath, maxZipTotal)
		}
		if err := writeZipFile(f, target); err != nil {
			cleanup()
			return "", nil, err
		}
	}
	return dir, cleanup, nil
}

func writeZipFile(f *zip.File, target string) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	rc, err := f.Open()
	if err != nil {
		return fmt.Errorf("open zip entry %s: %w", f.Name, err)
	}
	defer func() { _ = rc.Close() }()
	out, err := os.Create(target)
	if err != nil {
		return err
	}
	// Read one extra byte past the cap so a lying UncompressedSize still fails closed.
	n, err := io.Copy(out, io.LimitReader(rc, maxZipPerFile+1))
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("extract %s: %w", f.Name, err)
	}
	if n > maxZipPerFile {
		return fmt.Errorf("zip entry %s exceeds %d bytes", f.Name, maxZipPerFile)
	}
	return nil
}
