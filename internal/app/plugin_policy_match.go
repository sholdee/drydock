package app

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/sholdee/drydock/internal/pluginpolicy"
	"github.com/sholdee/drydock/internal/render"
)

type policyPluginStaticMatch struct {
	name       string
	plugin     pluginpolicy.Plugin
	discoverBy string
}

func (p localProvider) policyPluginStaticDiscoveryMatches(source render.ResolvedSource) ([]policyPluginStaticMatch, error) {
	if source.Path == "" {
		return nil, nil
	}
	sourcePath, err := cleanLocalSourcePath(source.Path)
	if err != nil {
		return nil, err
	}
	if err := rejectLocalSymlinkComponents(source.RepoRoot, sourcePath); err != nil {
		return nil, err
	}
	sourceDir := filepath.Join(source.RepoRoot, sourcePath)
	names := make([]string, 0, len(p.pluginPolicy.Plugins))
	for name := range p.pluginPolicy.Plugins {
		names = append(names, name)
	}
	sort.Strings(names)
	matches := make([]policyPluginStaticMatch, 0, len(names))
	for _, name := range names {
		plugin := p.pluginPolicy.Plugins[name]
		discover, discoverBy, ok := policyPluginStaticDiscoverRule(plugin)
		if !ok {
			continue
		}
		matched, err := policyPluginDiscoverMatch(sourceDir, discover)
		if err != nil {
			return nil, err
		}
		if matched {
			matches = append(matches, policyPluginStaticMatch{name: name, plugin: plugin, discoverBy: discoverBy})
		}
	}
	return matches, nil
}

func policyPluginStaticDiscoverRule(plugin pluginpolicy.Plugin) (pluginpolicy.PluginDiscoverMatch, string, bool) {
	if plugin.Match != nil {
		return plugin.Match.Discover, "match.discover", true
	}
	if plugin.ConfigManagementPlugin != nil && plugin.ConfigManagementPlugin.Discover != nil {
		return *plugin.ConfigManagementPlugin.Discover, "configManagementPlugin.discover", true
	}
	return pluginpolicy.PluginDiscoverMatch{}, "", false
}

func policyPluginDiscoverMatch(sourceDir string, rule pluginpolicy.PluginDiscoverMatch) (bool, error) {
	switch {
	case rule.FileName != "":
		return policyPluginFileNameMatch(sourceDir, rule.FileName)
	case rule.FindGlob != "":
		return policyPluginFindGlobMatch(sourceDir, rule.FindGlob)
	default:
		return false, nil
	}
}

func policyPluginFileNameMatch(sourceDir, pattern string) (bool, error) {
	matches, err := filepath.Glob(filepath.Join(sourceDir, filepath.FromSlash(pattern)))
	if err != nil {
		return false, err
	}
	for _, match := range matches {
		if policyPluginMatchedPathAllowed(sourceDir, match) {
			return true, nil
		}
	}
	return false, nil
}

func policyPluginFindGlobMatch(sourceDir, pattern string) (bool, error) {
	matched := false
	err := filepath.WalkDir(sourceDir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == sourceDir {
			return nil
		}
		rel, err := filepath.Rel(sourceDir, path)
		if err != nil {
			return err
		}
		if policyPluginMatchHasDotGit(filepath.ToSlash(rel)) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		ok, err := doublestar.Match(pattern, filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		if ok {
			matched = true
		}
		return nil
	})
	return matched, err
}

func policyPluginMatchedPathAllowed(sourceDir, match string) bool {
	if !cmpDiscoveryMatchInside(sourceDir, match) {
		return false
	}
	rel, err := filepath.Rel(sourceDir, match)
	if err != nil {
		return false
	}
	if policyPluginMatchHasDotGit(filepath.ToSlash(rel)) {
		return false
	}
	return rejectLocalSymlinkComponents(sourceDir, rel) == nil
}

func policyPluginMatchHasDotGit(rel string) bool {
	return slices.Contains(strings.Split(filepath.ToSlash(rel), "/"), ".git")
}

func policyPluginMatchNames(matches []policyPluginStaticMatch) string {
	names := make([]string, 0, len(matches))
	for _, match := range matches {
		if match.discoverBy != "" {
			names = append(names, match.name+" via "+match.discoverBy)
			continue
		}
		names = append(names, match.name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func unnamedPolicyPluginNoMatchMessage() string {
	return "config management plugin name is required and no trusted plugin policy match.discover or configManagementPlugin.discover rule matched this source"
}

func unnamedPolicyPluginAmbiguousMessage(matches []policyPluginStaticMatch) string {
	return fmt.Sprintf("config management plugin name is ambiguous; trusted plugin policy discovery rules matched: %s", policyPluginMatchNames(matches))
}
