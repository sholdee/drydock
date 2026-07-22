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

// mergeDiscoveryItems merges overlay into base, resolving key collisions via
// the tier-priority conflict rules. kind is per-item because settings
// candidates carry their object kind in the item itself.
func mergeDiscoveryItems[T any](
	base, overlay []T,
	key func(T) string,
	kind func(T) string,
	same func(T, T) bool,
	source func(T) (discovery.SourceTier, string, int),
) ([]T, []diagnostic.Diagnostic) {
	out := append([]T(nil), base...)
	indexes := make(map[string]int, len(out))
	looseIndexes := make(map[string]discoveryLooseIndex, len(out))
	for i, item := range out {
		addDiscoveryIndex(indexes, looseIndexes, key(item), i)
	}
	var diags []diagnostic.Diagnostic
	for _, item := range overlay {
		itemKey := key(item)
		incomingTier, incomingPath, incomingDocument := source(item)
		if index, ok := indexes[itemKey]; ok {
			if same(out[index], item) {
				continue
			}
			existingTier, existingPath, existingDocument := source(out[index])
			replacement, diag := resolveDiscoveryConflict(kind(item), itemKey, existingTier, existingPath, existingDocument, incomingTier, incomingPath, incomingDocument)
			if diag != nil {
				diags = append(diags, *diag)
			}
			if replacement {
				out[index] = item
			}
			continue
		}
		if index, existingKey, ok := namespaceDefaultedConflict(indexes, looseIndexes, itemKey); ok {
			existingTier, existingPath, existingDocument := source(out[index])
			replacement, diag := resolveNamespaceDefaultedDiscoveryConflict(kind(item), existingKey, itemKey, existingTier, existingPath, existingDocument, incomingTier, incomingPath, incomingDocument)
			if diag != nil {
				diags = append(diags, *diag)
			}
			if replacement {
				delete(indexes, existingKey)
				removeDiscoveryLooseIndex(looseIndexes, existingKey)
				out[index] = item
				addDiscoveryIndex(indexes, looseIndexes, itemKey, index)
			}
			continue
		}
		addDiscoveryIndex(indexes, looseIndexes, itemKey, len(out))
		out = append(out, item)
	}
	return out, diags
}

func mergeApplications(base, overlay []discovery.ApplicationFile) ([]discovery.ApplicationFile, []diagnostic.Diagnostic) {
	return mergeDiscoveryItems(base, overlay,
		func(item discovery.ApplicationFile) string { return applicationDiscoveryKey(item.Application) },
		func(discovery.ApplicationFile) string { return "Application" },
		sameApplicationDiscoveryObject,
		func(item discovery.ApplicationFile) (discovery.SourceTier, string, int) {
			return item.Tier, item.Path, item.DocumentIndex
		})
}

func mergeApplicationSets(base, overlay []discovery.ApplicationSetFile) ([]discovery.ApplicationSetFile, []diagnostic.Diagnostic) {
	return mergeDiscoveryItems(base, overlay,
		func(item discovery.ApplicationSetFile) string { return applicationSetDiscoveryKey(item.ApplicationSet) },
		func(discovery.ApplicationSetFile) string { return "ApplicationSet" },
		sameApplicationSetDiscoveryObject,
		func(item discovery.ApplicationSetFile) (discovery.SourceTier, string, int) {
			return item.Tier, item.Path, item.DocumentIndex
		})
}

func mergeProjects(base, overlay []discovery.ProjectFile) ([]discovery.ProjectFile, []diagnostic.Diagnostic) {
	return mergeDiscoveryItems(base, overlay,
		func(item discovery.ProjectFile) string { return projectDiscoveryKey(item.Project) },
		func(discovery.ProjectFile) string { return "AppProject" },
		sameProjectDiscoveryObject,
		func(item discovery.ProjectFile) (discovery.SourceTier, string, int) {
			return item.Tier, item.Path, item.DocumentIndex
		})
}

func mergeSettingsCandidates(base, overlay []discovery.SettingsCandidate) ([]discovery.SettingsCandidate, []diagnostic.Diagnostic) {
	return mergeDiscoveryItems(base, overlay,
		settingsDiscoveryKey,
		func(item discovery.SettingsCandidate) string { return settingsObjectKind(item.Kind) },
		sameSettingsDiscoveryObject,
		func(item discovery.SettingsCandidate) (discovery.SourceTier, string, int) {
			return item.Tier, item.Path, item.DocumentIndex
		})
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
	case discovery.SourceTierPolicyBootstrap:
		return 2
	case discovery.SourceTierRenderedFleet:
		return 3
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
	existingPriority := discoveryTierPriority(existingTier)
	incomingPriority := discoveryTierPriority(incomingTier)
	if incomingPriority != existingPriority {
		return incomingPriority < existingPriority, nil
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
