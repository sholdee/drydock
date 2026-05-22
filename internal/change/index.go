package change

import (
	"path"
	"sort"
	"strings"
)

type Index struct {
	inputs map[string][]string
}

type MatchResult struct {
	Applications []string
	Unowned      []string
	RenderAll    bool
}

func NewIndex() *Index {
	return &Index{inputs: make(map[string][]string)}
}

func (i *Index) Add(application string, inputs []string) {
	normalized := make([]string, 0, len(inputs))
	for _, input := range inputs {
		normalized = append(normalized, normalize(input))
	}
	i.inputs[application] = append(i.inputs[application], normalized...)
}

func (i *Index) Match(changed []string) MatchResult {
	applications := make(map[string]struct{})
	var unowned []string

	for _, changedPath := range changed {
		normalizedChanged := normalize(changedPath)
		owned := false
		for application, inputs := range i.inputs {
			for _, input := range inputs {
				if intersects(input, normalizedChanged) {
					applications[application] = struct{}{}
					owned = true
				}
			}
		}
		if !owned {
			unowned = append(unowned, normalizedChanged)
		}
	}

	result := MatchResult{
		Applications: sortedKeys(applications),
		Unowned:      sortedValues(unowned),
	}
	result.RenderAll = len(result.Unowned) > 0
	return result
}

func intersects(input, changed string) bool {
	if input == "" {
		return true
	}
	return changed == input || strings.HasPrefix(changed, input+"/")
}

func normalize(p string) string {
	cleaned := path.Clean(strings.Trim(p, "/"))
	if cleaned == "." {
		return ""
	}
	return cleaned
}

func sortedKeys(values map[string]struct{}) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func sortedValues(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}
