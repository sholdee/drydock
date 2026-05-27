package app

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/discovery"
)

func mergeDiscoveryResultsWithDiagnostics(base, overlay discovery.Result) (discovery.Result, []diagnostic.Diagnostic) {
	out := base
	diags := make([]diagnostic.Diagnostic, 0, len(overlay.Applications)+len(overlay.ApplicationSets)+len(overlay.Projects)+len(overlay.SettingsCandidates))
	var next []diagnostic.Diagnostic
	out.Applications, next = mergeApplications(out.Applications, overlay.Applications)
	diags = append(diags, next...)
	out.ApplicationSets, next = mergeApplicationSets(out.ApplicationSets, overlay.ApplicationSets)
	diags = append(diags, next...)
	out.Projects, next = mergeProjects(out.Projects, overlay.Projects)
	diags = append(diags, next...)
	out.SettingsCandidates, next = mergeSettingsCandidates(out.SettingsCandidates, overlay.SettingsCandidates)
	diags = append(diags, next...)
	return out, diags
}

func mergeApplications(base, overlay []discovery.ApplicationFile) ([]discovery.ApplicationFile, []diagnostic.Diagnostic) {
	out := append([]discovery.ApplicationFile(nil), base...)
	indexes := make(map[string]int, len(out))
	looseIndexes := make(map[string]discoveryLooseIndex, len(out))
	for i, item := range out {
		addDiscoveryIndex(indexes, looseIndexes, applicationDiscoveryKey(item.Application), i)
	}
	var diags []diagnostic.Diagnostic
	for _, item := range overlay {
		key := applicationDiscoveryKey(item.Application)
		if index, ok := indexes[key]; ok {
			if sameApplicationDiscoveryObject(out[index], item) {
				continue
			}
			replacement, diag := resolveDiscoveryConflict("Application", key, out[index].Tier, out[index].Path, out[index].DocumentIndex, item.Tier, item.Path, item.DocumentIndex)
			if diag != nil {
				diags = append(diags, *diag)
			}
			if replacement {
				out[index] = item
			}
			continue
		}
		if index, existingKey, ok := namespaceDefaultedConflict(indexes, looseIndexes, key); ok {
			replacement, diag := resolveNamespaceDefaultedDiscoveryConflict("Application", existingKey, key, out[index].Tier, out[index].Path, out[index].DocumentIndex, item.Tier, item.Path, item.DocumentIndex)
			if diag != nil {
				diags = append(diags, *diag)
			}
			if replacement {
				delete(indexes, existingKey)
				removeDiscoveryLooseIndex(looseIndexes, existingKey)
				out[index] = item
				addDiscoveryIndex(indexes, looseIndexes, key, index)
			}
			continue
		}
		addDiscoveryIndex(indexes, looseIndexes, key, len(out))
		out = append(out, item)
	}
	return out, diags
}

func mergeApplicationSets(base, overlay []discovery.ApplicationSetFile) ([]discovery.ApplicationSetFile, []diagnostic.Diagnostic) {
	out := append([]discovery.ApplicationSetFile(nil), base...)
	indexes := make(map[string]int, len(out))
	looseIndexes := make(map[string]discoveryLooseIndex, len(out))
	for i, item := range out {
		addDiscoveryIndex(indexes, looseIndexes, applicationSetDiscoveryKey(item.ApplicationSet), i)
	}
	var diags []diagnostic.Diagnostic
	for _, item := range overlay {
		key := applicationSetDiscoveryKey(item.ApplicationSet)
		if index, ok := indexes[key]; ok {
			if sameApplicationSetDiscoveryObject(out[index], item) {
				continue
			}
			replacement, diag := resolveDiscoveryConflict("ApplicationSet", key, out[index].Tier, out[index].Path, out[index].DocumentIndex, item.Tier, item.Path, item.DocumentIndex)
			if diag != nil {
				diags = append(diags, *diag)
			}
			if replacement {
				out[index] = item
			}
			continue
		}
		if index, existingKey, ok := namespaceDefaultedConflict(indexes, looseIndexes, key); ok {
			replacement, diag := resolveNamespaceDefaultedDiscoveryConflict("ApplicationSet", existingKey, key, out[index].Tier, out[index].Path, out[index].DocumentIndex, item.Tier, item.Path, item.DocumentIndex)
			if diag != nil {
				diags = append(diags, *diag)
			}
			if replacement {
				delete(indexes, existingKey)
				removeDiscoveryLooseIndex(looseIndexes, existingKey)
				out[index] = item
				addDiscoveryIndex(indexes, looseIndexes, key, index)
			}
			continue
		}
		addDiscoveryIndex(indexes, looseIndexes, key, len(out))
		out = append(out, item)
	}
	return out, diags
}

func mergeProjects(base, overlay []discovery.ProjectFile) ([]discovery.ProjectFile, []diagnostic.Diagnostic) {
	out := append([]discovery.ProjectFile(nil), base...)
	indexes := make(map[string]int, len(out))
	looseIndexes := make(map[string]discoveryLooseIndex, len(out))
	for i, item := range out {
		addDiscoveryIndex(indexes, looseIndexes, projectDiscoveryKey(item.Project), i)
	}
	var diags []diagnostic.Diagnostic
	for _, item := range overlay {
		key := projectDiscoveryKey(item.Project)
		if index, ok := indexes[key]; ok {
			if sameProjectDiscoveryObject(out[index], item) {
				continue
			}
			replacement, diag := resolveDiscoveryConflict("AppProject", key, out[index].Tier, out[index].Path, out[index].DocumentIndex, item.Tier, item.Path, item.DocumentIndex)
			if diag != nil {
				diags = append(diags, *diag)
			}
			if replacement {
				out[index] = item
			}
			continue
		}
		if index, existingKey, ok := namespaceDefaultedConflict(indexes, looseIndexes, key); ok {
			replacement, diag := resolveNamespaceDefaultedDiscoveryConflict("AppProject", existingKey, key, out[index].Tier, out[index].Path, out[index].DocumentIndex, item.Tier, item.Path, item.DocumentIndex)
			if diag != nil {
				diags = append(diags, *diag)
			}
			if replacement {
				delete(indexes, existingKey)
				removeDiscoveryLooseIndex(looseIndexes, existingKey)
				out[index] = item
				addDiscoveryIndex(indexes, looseIndexes, key, index)
			}
			continue
		}
		addDiscoveryIndex(indexes, looseIndexes, key, len(out))
		out = append(out, item)
	}
	return out, diags
}

func mergeSettingsCandidates(base, overlay []discovery.SettingsCandidate) ([]discovery.SettingsCandidate, []diagnostic.Diagnostic) {
	out := append([]discovery.SettingsCandidate(nil), base...)
	indexes := make(map[string]int, len(out))
	looseIndexes := make(map[string]discoveryLooseIndex, len(out))
	for i, item := range out {
		addDiscoveryIndex(indexes, looseIndexes, settingsDiscoveryKey(item), i)
	}
	var diags []diagnostic.Diagnostic
	for _, item := range overlay {
		key := settingsDiscoveryKey(item)
		if index, ok := indexes[key]; ok {
			if sameSettingsDiscoveryObject(out[index], item) {
				continue
			}
			replacement, diag := resolveDiscoveryConflict(settingsObjectKind(item.Kind), key, out[index].Tier, out[index].Path, out[index].DocumentIndex, item.Tier, item.Path, item.DocumentIndex)
			if diag != nil {
				diags = append(diags, *diag)
			}
			if replacement {
				out[index] = item
			}
			continue
		}
		if index, existingKey, ok := namespaceDefaultedConflict(indexes, looseIndexes, key); ok {
			replacement, diag := resolveNamespaceDefaultedDiscoveryConflict(settingsObjectKind(item.Kind), existingKey, key, out[index].Tier, out[index].Path, out[index].DocumentIndex, item.Tier, item.Path, item.DocumentIndex)
			if diag != nil {
				diags = append(diags, *diag)
			}
			if replacement {
				delete(indexes, existingKey)
				removeDiscoveryLooseIndex(looseIndexes, existingKey)
				out[index] = item
				addDiscoveryIndex(indexes, looseIndexes, key, index)
			}
			continue
		}
		addDiscoveryIndex(indexes, looseIndexes, key, len(out))
		out = append(out, item)
	}
	return out, diags
}

type discoveryLooseIndex struct {
	key       string
	index     int
	ambiguous bool
}

func addDiscoveryIndex(indexes map[string]int, looseIndexes map[string]discoveryLooseIndex, key string, index int) {
	indexes[key] = index
	looseKey, ok := looseDiscoveryKey(key)
	if !ok {
		return
	}
	if existing, ok := looseIndexes[looseKey]; ok && existing.key != key {
		looseIndexes[looseKey] = discoveryLooseIndex{ambiguous: true}
		return
	}
	looseIndexes[looseKey] = discoveryLooseIndex{key: key, index: index}
}

func removeDiscoveryLooseIndex(looseIndexes map[string]discoveryLooseIndex, key string) {
	looseKey, ok := looseDiscoveryKey(key)
	if !ok {
		return
	}
	existing, ok := looseIndexes[looseKey]
	if ok && existing.key == key && !existing.ambiguous {
		delete(looseIndexes, looseKey)
	}
}

func namespaceDefaultedConflict(indexes map[string]int, looseIndexes map[string]discoveryLooseIndex, key string) (int, string, bool) {
	if _, ok := indexes[key]; ok {
		return 0, "", false
	}
	looseKey, ok := looseDiscoveryKey(key)
	if !ok {
		return 0, "", false
	}
	existing, ok := looseIndexes[looseKey]
	if !ok || existing.ambiguous {
		return 0, "", false
	}
	existingNamespace, ok := discoveryKeyNamespace(existing.key)
	if !ok {
		return 0, "", false
	}
	incomingNamespace, ok := discoveryKeyNamespace(key)
	if !ok || existingNamespace == incomingNamespace {
		return 0, "", false
	}
	if existingNamespace == "" || incomingNamespace == "" {
		return existing.index, existing.key, true
	}
	return 0, "", false
}

func looseDiscoveryKey(key string) (string, bool) {
	parts := strings.Split(key, "\x00")
	if len(parts) != 4 || parts[0] == "" || parts[1] == "" || parts[3] == "" {
		return "", false
	}
	return parts[0] + "\x00" + parts[1] + "\x00" + parts[3], true
}

func discoveryKeyNamespace(key string) (string, bool) {
	parts := strings.Split(key, "\x00")
	if len(parts) != 4 {
		return "", false
	}
	return parts[2], true
}

func resolveDiscoveryConflict(kind, key string, existingTier discovery.SourceTier, existingPath string, existingDocument int, incomingTier discovery.SourceTier, incomingPath string, incomingDocument int) (bool, *diagnostic.Diagnostic) {
	if existingTier == incomingTier && existingPath == incomingPath && existingDocument == incomingDocument {
		return true, nil
	}
	replace := discoveryTierPriority(incomingTier) < discoveryTierPriority(existingTier)
	winnerPath := existingPath
	ignoredPath := incomingPath
	if replace {
		winnerPath = incomingPath
		ignoredPath = existingPath
	}
	message := fmt.Sprintf("duplicate %s %s from %s ignored; %s takes precedence", kind, displayDiscoveryKey(key), ignoredPath, winnerPath)
	diag := diagnostic.Diagnostic{
		Severity: diagnostic.SeverityWarning,
		Category: "discovery",
		Message:  message,
		Provenance: diagnostic.Provenance{
			Path: ignoredPath,
		},
	}
	diag.Code = diagnostic.StableCode(diag)
	return replace, &diag
}

func discoveryTierPriority(tier discovery.SourceTier) int {
	switch tier {
	case discovery.SourceTierExplicitRendered:
		return 0
	case discovery.SourceTierStatic:
		return 1
	case discovery.SourceTierRenderedFleet:
		return 2
	default:
		return 1
	}
}

func resolveNamespaceDefaultedDiscoveryConflict(kind, existingKey, incomingKey string, existingTier discovery.SourceTier, existingPath string, existingDocument int, incomingTier discovery.SourceTier, incomingPath string, incomingDocument int) (bool, *diagnostic.Diagnostic) {
	existingNamespace, existingOK := discoveryKeyNamespace(existingKey)
	incomingNamespace, incomingOK := discoveryKeyNamespace(incomingKey)
	if !existingOK || !incomingOK || existingNamespace == incomingNamespace || (existingNamespace != "" && incomingNamespace != "") {
		return resolveDiscoveryConflict(kind, incomingKey, existingTier, existingPath, existingDocument, incomingTier, incomingPath, incomingDocument)
	}
	replace := incomingNamespace != ""
	return replace, nil
}

func sameApplicationDiscoveryObject(left, right discovery.ApplicationFile) bool {
	return reflect.DeepEqual(left.Application, right.Application)
}

func sameApplicationSetDiscoveryObject(left, right discovery.ApplicationSetFile) bool {
	return reflect.DeepEqual(left.ApplicationSet, right.ApplicationSet)
}

func sameProjectDiscoveryObject(left, right discovery.ProjectFile) bool {
	return reflect.DeepEqual(left.Project, right.Project)
}

func sameSettingsDiscoveryObject(left, right discovery.SettingsCandidate) bool {
	if left.Kind != right.Kind {
		return false
	}
	if left.Object != nil || right.Object != nil {
		if left.Object == nil || right.Object == nil {
			return false
		}
		return reflect.DeepEqual(left.Object.Object, right.Object.Object)
	}
	return left.APIVersion == right.APIVersion &&
		left.Namespace == right.Namespace &&
		left.Name == right.Name
}
