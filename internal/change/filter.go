package change

import (
	"fmt"
	"path"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

type PathFilter struct {
	includes []string
	ignores  []string
}

type PathFilterConfig struct {
	Includes []string
	Ignores  []string
}

type FilterResult struct {
	Paths    []string
	Included []string
	Ignored  []string
}

func NewPathFilter(config PathFilterConfig) (PathFilter, error) {
	includes, err := normalizePatterns(config.Includes, "include")
	if err != nil {
		return PathFilter{}, err
	}
	ignores, err := normalizePatterns(config.Ignores, "ignore")
	if err != nil {
		return PathFilter{}, err
	}
	return PathFilter{includes: includes, ignores: ignores}, nil
}

func (filter PathFilter) Apply(paths []string) FilterResult {
	result := FilterResult{Paths: make([]string, 0, len(paths))}
	for _, inputPath := range paths {
		normalized := normalizeFilterPath(inputPath)
		if !filter.included(normalized) {
			continue
		}
		result.Included = append(result.Included, normalized)
		if filter.ignored(normalized) {
			result.Ignored = append(result.Ignored, normalized)
			continue
		}
		result.Paths = append(result.Paths, normalized)
	}
	return result
}

func (filter PathFilter) included(inputPath string) bool {
	if len(filter.includes) == 0 {
		return true
	}
	for _, pattern := range filter.includes {
		if doublestar.MatchUnvalidated(pattern, inputPath) {
			return true
		}
	}
	return false
}

func (filter PathFilter) ignored(inputPath string) bool {
	for _, pattern := range filter.ignores {
		if doublestar.MatchUnvalidated(pattern, inputPath) {
			return true
		}
	}
	return false
}

func normalizePatterns(patterns []string, label string) ([]string, error) {
	out := make([]string, 0, len(patterns))
	for _, pattern := range patterns {
		normalized := normalizeFilterPath(pattern)
		if normalized == "" {
			return nil, fmt.Errorf("changed-only %s glob must not be blank", label)
		}
		if !doublestar.ValidatePattern(normalized) {
			return nil, fmt.Errorf("changed-only %s glob %q is invalid", label, pattern)
		}
		out = append(out, normalized)
	}
	return out, nil
}

func normalizeFilterPath(input string) string {
	normalized := strings.ReplaceAll(strings.TrimSpace(input), "\\", "/")
	for strings.HasPrefix(normalized, "./") {
		normalized = strings.TrimPrefix(normalized, "./")
	}
	cleaned := path.Clean(strings.Trim(normalized, "/"))
	if cleaned == "." {
		return ""
	}
	return cleaned
}
