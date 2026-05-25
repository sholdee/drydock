package chart

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
)

func extractChartArchive(r io.Reader, dest, chartName string) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("open chart archive gzip: %w", err)
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read chart archive: %w", err)
		}
		rel, err := safeChartArchivePath(header.Name, chartName)
		if err != nil {
			return err
		}
		if rel == "" {
			continue
		}
		target := filepath.Join(dest, filepath.FromSlash(rel))
		if err := ensureContainedPath(dest, target); err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return fmt.Errorf("create chart archive directory %s: %w", target, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("create chart archive directory %s: %w", filepath.Dir(target), err)
			}
			if err := writeArchiveFile(target, tr, header.FileInfo().Mode().Perm()); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported chart archive entry %s", header.Name)
		}
	}
}
func safeChartArchivePath(name, chartName string) (string, error) {
	normalized := strings.ReplaceAll(name, "\\", "/")
	if name == "" || path.IsAbs(normalized) || filepath.IsAbs(name) {
		return "", fmt.Errorf("unsafe chart archive path %q", name)
	}
	for _, segment := range strings.Split(normalized, "/") {
		if segment == ".." {
			return "", fmt.Errorf("unsafe chart archive path %q", name)
		}
	}
	cleaned := path.Clean(normalized)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("unsafe chart archive path %q", name)
	}
	root, rel, ok := strings.Cut(cleaned, "/")
	if root != chartName {
		return "", fmt.Errorf("chart archive entry %q is outside chart root %q", name, chartName)
	}
	if !ok {
		return "", nil
	}
	if rel == "." || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("unsafe chart archive path %q", name)
	}
	return rel, nil
}
func ensureContainedPath(root, target string) error {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return fmt.Errorf("validate chart archive path %s: %w", target, err)
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("chart archive path %s escapes destination %s", target, root)
	}
	return nil
}
func writeArchiveFile(target string, src io.Reader, mode os.FileMode) error {
	if mode == 0 {
		mode = 0o600
	}
	file, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("create chart archive file %s: %w", target, err)
	}
	_, copyErr := io.Copy(file, src)
	closeErr := file.Close()
	if copyErr != nil {
		return fmt.Errorf("write chart archive file %s: %w", target, copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close chart archive file %s: %w", target, closeErr)
	}
	return nil
}
func chartArchiveContainsNamedChart(r io.Reader, name string) bool {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return false
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	want := path.Join(name, "Chart.yaml")
	for {
		header, err := tr.Next()
		if err == io.EOF {
			return false
		}
		if err != nil {
			return false
		}
		cleaned := path.Clean(strings.ReplaceAll(header.Name, "\\", "/"))
		if cleaned == want && header.Typeflag == tar.TypeReg {
			return true
		}
	}
}
