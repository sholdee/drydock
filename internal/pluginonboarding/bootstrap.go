package pluginonboarding

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func validateBootstrapHints(report Report) error {
	if len(report.BootstrapEntrypoints) == 0 {
		return nil
	}
	known := map[string]PluginReport{}
	for _, plugin := range report.Plugins {
		known[plugin.Name] = plugin
	}
	for _, entrypoint := range report.BootstrapEntrypoints {
		pluginName := strings.TrimSpace(entrypoint.Plugin)
		plugin, ok := known[pluginName]
		if !ok {
			return fmt.Errorf("bootstrap entrypoint plugin %q is not known from discovered plugin evidence", pluginName)
		}
		if plugin.Discover == nil {
			return fmt.Errorf("bootstrap entrypoint plugin %q must define trusted static discovery", pluginName)
		}
		sourcePath := cleanBootstrapSourcePath(entrypoint.SourcePath)
		if sourcePath == "" {
			return fmt.Errorf("bootstrap entrypoint source path %q must be a relative non-escaping path", entrypoint.SourcePath)
		}
		if err := validateBootstrapPath(report.Root, sourcePath); err != nil {
			return fmt.Errorf("bootstrap entrypoint source path %q: %w", entrypoint.SourcePath, err)
		}
	}
	return nil
}

func validateBootstrapPath(root, sourcePath string) error {
	if strings.TrimSpace(root) == "" {
		return nil
	}
	if info, err := os.Lstat(root); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("repository root is a symlink")
	} else if errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	current := root
	if sourcePath != "." {
		for part := range strings.SplitSeq(filepath.FromSlash(sourcePath), string(filepath.Separator)) {
			if part == "" || part == "." {
				continue
			}
			current = filepath.Join(current, part)
			info, err := os.Lstat(current)
			if err != nil {
				return err
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("%s is a symlink", filepath.ToSlash(current))
			}
		}
	}
	info, err := os.Lstat(filepath.Join(root, filepath.FromSlash(sourcePath)))
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("must be a directory")
	}
	return nil
}
