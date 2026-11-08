package config

import (
	"fmt"
	"sort"
	"strings"

	"github.com/sholdee/drydock/internal/diagnostic"
)

const (
	kustomizeVersionKeyPrefix = "kustomize.version"
	kustomizePathKeyPrefix    = "kustomize.path"
)

// EffectiveKustomizeBuildOptions returns the kustomize.buildOptions words
// that the repo-server passes to kustomize build. Argo CD splits the value
// on white space with strings.Fields and keeps no quoting
// (util/kustomize/kustomize.go:433 at v3.6.1). Without a found argocd-cm,
// drydock cannot tell the flags, and it assumes "--enable-helm", so that
// helmCharts render; the renderer reads them the same way
// (render.KustomizeCallerArgoCDAssumed).
func (s ArgoSettings) EffectiveKustomizeBuildOptions() []string {
	if !s.ArgoCDConfigMapFound {
		return []string{"--enable-helm"}
	}
	out := make([]string, 0, len(s.KustomizeBuildOptions)+1)
	for _, option := range s.KustomizeBuildOptions {
		out = append(out, option.Value)
	}
	// A value that already holds "--enable-helm" decides the flag itself:
	// a later --enable-helm=false wins over one put first. Leaving the
	// value as it is keeps the render and its cache key the same as
	// without KustomizeHelmAssumed.
	if s.KustomizeHelmAssumed && !strings.Contains(strings.Join(out, " "), "--enable-helm") {
		out = append([]string{"--enable-helm"}, out...)
	}
	return out
}

// WithKustomizeHelmAssumed returns the settings for a render that reads the
// Argo CD settings an install creates: kustomize builds get --enable-helm
// before kustomize.buildOptions, unless the value names --enable-helm. The
// settings that drydock found so far can lack keys that
// the install adds, for example with a patch of argocd-cm, and the cluster
// already has them. Without this, an install that renders its chart with
// helmCharts could never give drydock its own kustomize.buildOptions.
func (s ArgoSettings) WithKustomizeHelmAssumed() ArgoSettings {
	s.KustomizeHelmAssumed = true
	return s
}

// KustomizeVersionRegistered reports whether argocd-cm registers the
// kustomize version that spec.source.kustomize.version names. Argo CD
// v3.6.1 fails the source with "kustomize version <name> is not
// registered" when it does not (GetKustomizeBinaryPath,
// util/settings/settings.go:300-321). Without a found argocd-cm, drydock
// cannot tell, so every version counts as registered.
func (s ArgoSettings) KustomizeVersionRegistered(name string) bool {
	if !s.ArgoCDConfigMapFound {
		return true
	}
	_, ok := s.KustomizeVersions[name]
	return ok
}

// KustomizeVersionNames returns the registered kustomize version names,
// sorted.
func (s ArgoSettings) KustomizeVersionNames() []string {
	names := make([]string, 0, len(s.KustomizeVersions))
	for name := range s.KustomizeVersions {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// applyKustomizeVersionKeys reads kustomize.version.<name> and
// kustomize.path.<name> as GetKustomizeSettings does: it matches the key
// prefix without the dot and takes the name after the next character
// (addKustomizeVersion). A name that two keys register fails the settings
// of every Application: "failed to add kustomize version from <key>:
// found duplicate kustomize version: <name>". Argo CD reads the keys in
// map order, so the key that it names can differ. drydock names the later
// key in sorted order.
func applyKustomizeVersionKeys(settings *ArgoSettings, values map[string]string, path, basePointer string) []diagnostic.Diagnostic {
	keys := make([]string, 0)
	for key := range values {
		for _, prefix := range []string{kustomizeVersionKeyPrefix, kustomizePathKeyPrefix} {
			// A key equal to the prefix makes Argo CD slice past its end.
			if strings.HasPrefix(key, prefix) && len(key) > len(prefix) {
				keys = append(keys, key)
			}
		}
	}
	sort.Strings(keys)
	var diags []diagnostic.Diagnostic
	for _, key := range keys {
		prefix := kustomizeVersionKeyPrefix
		if strings.HasPrefix(key, kustomizePathKeyPrefix) {
			prefix = kustomizePathKeyPrefix
		}
		name := key[len(prefix)+1:]
		provenance := diagnostic.Provenance{Path: path, Pointer: basePointer + "." + key}
		if _, ok := settings.KustomizeVersions[name]; ok {
			diags = append(diags, diagnostic.Diagnostic{
				Severity:   diagnostic.SeverityError,
				Category:   "settings",
				Message:    fmt.Sprintf("failed to add kustomize version from %q: found duplicate kustomize version: %s", key, name),
				Provenance: provenance,
			})
			continue
		}
		if settings.KustomizeVersions == nil {
			settings.KustomizeVersions = map[string]Value[string]{}
		}
		settings.KustomizeVersions[name] = Value[string]{Value: values[key], Provenance: provenance}
	}
	return diags
}

// mergeKustomizeVersions adds the kustomize versions of candidate. The
// same name with another binary path conflicts.
func mergeKustomizeVersions(merged *ArgoSettings, candidate ArgoSettings) []diagnostic.Diagnostic {
	var diags []diagnostic.Diagnostic
	for _, name := range candidate.KustomizeVersionNames() {
		value := candidate.KustomizeVersions[name]
		if existing, ok := merged.KustomizeVersions[name]; ok {
			if existing.Value != value.Value {
				diags = append(diags, conflictDiagnostic(
					fmt.Sprintf("conflicting kustomize version %s paths %q and %q", name, existing.Value, value.Value),
					value.Provenance,
				))
			}
			continue
		}
		if merged.KustomizeVersions == nil {
			merged.KustomizeVersions = map[string]Value[string]{}
		}
		merged.KustomizeVersions[name] = value
	}
	return diags
}
