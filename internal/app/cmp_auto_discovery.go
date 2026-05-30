package app

import (
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/pathsafety"
	"github.com/sholdee/drydock/internal/render"
)

type cmpStaticDiscoveryMatch struct {
	pluginName string
	method     string
	pattern    string
	provenance diagnostic.Provenance
}

func (p localProvider) cmpAutoDiscoveryDeferredDiagnostics(source render.ResolvedSource, opts render.RenderOptions) []diagnostic.Diagnostic {
	if opts.Plugin != nil || source.Path == "" || len(p.configManagementPlugins) == 0 {
		return nil
	}
	match, ok := p.firstStaticCMPDiscoveryMatch(source)
	if !ok {
		return nil
	}
	return []diagnostic.Diagnostic{{
		Code:     diagnostic.CodePluginAutoDiscovery,
		Severity: diagnostic.SeverityWarning,
		Category: "plugin",
		Message: fmt.Sprintf(
			"sidecar config management plugin %s %s discovery pattern %q statically matches this source, but drydock did not run sidecar CMP auto-discovery; set an explicit plugin source, use native compatibility, use AVP compatibility, or configure a trusted drydock plugin policy for deterministic plugin rendering",
			pluginDisplayName(match.pluginName),
			match.method,
			match.pattern,
		),
		Provenance: match.provenance,
	}}
}

func (p localProvider) firstStaticCMPDiscoveryMatch(source render.ResolvedSource) (cmpStaticDiscoveryMatch, bool) {
	sourcePath, err := cleanLocalSourcePath(source.Path)
	if err != nil {
		return cmpStaticDiscoveryMatch{}, false
	}
	sourceDir := filepath.Join(source.RepoRoot, sourcePath)
	names := make([]string, 0, len(p.configManagementPlugins))
	for name := range p.configManagementPlugins {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		plugin := p.configManagementPlugins[name]
		method, pattern, ok := cmpStaticDiscoveryPattern(plugin)
		if !ok {
			continue
		}
		matched, err := cmpDiscoveryPatternMatches(sourceDir, method, pattern)
		if err != nil || !matched {
			continue
		}
		return cmpStaticDiscoveryMatch{
			pluginName: name,
			method:     method,
			pattern:    pattern,
			provenance: plugin.Provenance,
		}, true
	}
	return cmpStaticDiscoveryMatch{}, false
}

func cmpStaticDiscoveryPattern(plugin config.ConfigManagementPlugin) (method, pattern string, ok bool) {
	if plugin.Discover.FileName != "" {
		return "fileName", plugin.Discover.FileName, true
	}
	if plugin.Discover.FindGlob != "" {
		return "find.glob", plugin.Discover.FindGlob, true
	}
	return "", "", false
}

func cmpDiscoveryPatternMatches(sourceDir, method, pattern string) (bool, error) {
	cleanPattern, ok := cleanCMPDiscoveryPattern(pattern)
	if !ok {
		return false, nil
	}
	fullPattern := filepath.Join(sourceDir, cleanPattern)
	var (
		matches []string
		err     error
	)
	switch method {
	case "fileName":
		matches, err = filepath.Glob(fullPattern)
	case "find.glob":
		matches, err = doublestar.FilepathGlob(fullPattern)
	default:
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, match := range matches {
		if cmpDiscoveryMatchInside(sourceDir, match) {
			return true, nil
		}
	}
	return false, nil
}

func cleanCMPDiscoveryPattern(pattern string) (string, bool) {
	pattern = filepath.FromSlash(strings.TrimSpace(pattern))
	if pattern == "" || filepath.IsAbs(pattern) {
		return "", false
	}
	if slices.Contains(strings.Split(pattern, string(filepath.Separator)), "..") {
		return "", false
	}
	clean := filepath.Clean(pattern)
	if clean == "." || pathsafety.RelEscapes(clean) {
		return "", false
	}
	return clean, true
}

func cmpDiscoveryMatchInside(sourceDir, match string) bool {
	absSourceDir, err := filepath.Abs(sourceDir)
	if err != nil {
		return false
	}
	absMatch, err := filepath.Abs(match)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absSourceDir, absMatch)
	if err != nil {
		return false
	}
	return !pathsafety.RelEscapes(rel)
}
