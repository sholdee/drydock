package appset

import (
	"slices"
	"strings"
)

func providerMatches(actual, expected string) bool {
	return normalizeProvider(actual) == normalizeProvider(expected)
}

func normalizeProvider(value string) string {
	value = strings.ToLower(value)
	value = strings.ReplaceAll(value, "-", "")
	value = strings.ReplaceAll(value, "_", "")
	value = strings.ReplaceAll(value, " ", "")
	return value
}

func scopeEqual(actual, expected string) bool {
	return expected != "" && actual == expected
}

func optionalScopeEqual(actual, expected string) bool {
	return expected == "" || actual == expected
}

func repositoryProjectScopeEqual(actualOrg, actualRepo, scopeOrg, scopeRepo, fullProject string) bool {
	if scopeRepo == "" {
		return actualRepo == fullProject || actualOrg == fullProject
	}
	return actualOrg == scopeOrg && actualRepo == scopeRepo
}

func splitProviderProject(project string) (string, string) {
	i := strings.LastIndex(project, "/")
	if i < 0 {
		return "", project
	}
	return project[:i], project[i+1:]
}

func gitLabGroupScopeEqual(actual, expected string, includeSubgroups bool) bool {
	if expected == "" {
		return false
	}
	if actual == expected {
		return true
	}
	return includeSubgroups && strings.HasPrefix(actual, expected+"/")
}

func providerProject(input SCMRepositoryInput) string {
	if input.Project != "" {
		return input.Project
	}
	return input.Organization
}

func providerProjectFromPR(input PullRequestInput) string {
	if input.Project != "" {
		return input.Project
	}
	return input.Organization
}

func stringSliceContains(values []string, value string) bool {
	return slices.Contains(values, value)
}

func shortString(value string, limit int) string {
	if len(value) < limit {
		return value
	}
	return value[:limit]
}

func stringMapAny(input map[string]string) map[string]any {
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}
