package render

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/pathsafety"
	"sigs.k8s.io/kustomize/api/types"
)

// KustomizeIgnoreMissingComponentsDroppedCode identifies the warning for a
// Kustomize source with ignoreMissingComponents whose components list holds
// more than one valid component. Argo CD v3.6.1 adds only the first one
// (util/kustomize/kustomize.go:346-372), so the later ones are dropped.
const KustomizeIgnoreMissingComponentsDroppedCode = "kustomize.ignore-missing-components-dropped"

func sourceKustomizeDiagnostics(opts RenderOptions) []diagnostic.Diagnostic {
	if opts.Kustomize == nil || opts.Kustomize.Version == "" {
		return nil
	}
	return []diagnostic.Diagnostic{{
		Severity: diagnostic.SeverityWarning,
		Category: "settings",
		Message:  fmt.Sprintf("source kustomize version %q parsed but not applied: drydock uses its embedded Go Kustomize library", opts.Kustomize.Version),
	}}
}

func hasKustomizeSourceMutations(opts RenderOptions) bool {
	kustomize := opts.Kustomize
	if kustomize == nil {
		return false
	}
	return kustomize.NamePrefix != "" ||
		kustomize.NameSuffix != "" ||
		kustomize.Namespace != "" ||
		len(kustomize.Images) != 0 ||
		len(kustomize.Replicas) != 0 ||
		len(kustomize.CommonLabels) != 0 ||
		len(kustomize.CommonAnnotations) != 0 ||
		len(kustomize.Patches) != 0 ||
		len(kustomize.Components) != 0
}

// applySourceKustomizeOptions merges the spec.source.kustomize options into
// the source kustomization. The returned diagnostics report options that
// Argo CD v3.6.1 accepts but does not fully apply.
//
//nolint:gocyclo // Mirrors Argo CD's source-level Kustomize override surface explicitly.
func applySourceKustomizeOptions(kustomization *types.Kustomization, dir, boundaryRoot string, opts RenderOptions) ([]diagnostic.Diagnostic, error) {
	kustomize := opts.Kustomize
	if kustomize == nil {
		return nil, nil
	}
	var diags []diagnostic.Diagnostic
	if kustomize.NamePrefix != "" {
		kustomization.NamePrefix = kustomize.NamePrefix
	}
	if kustomize.NameSuffix != "" {
		kustomization.NameSuffix = kustomize.NameSuffix
	}
	if kustomize.Namespace != "" {
		kustomization.Namespace = kustomize.Namespace
	}
	if len(kustomize.Images) != 0 {
		images, err := sourceKustomizeImages(kustomize.Images, opts.ArgoEnv)
		if err != nil {
			return nil, err
		}
		kustomization.Images = upsertImages(kustomization.Images, images)
	}
	if len(kustomize.Replicas) != 0 {
		replicas, err := sourceKustomizeReplicas(kustomize.Replicas)
		if err != nil {
			return nil, err
		}
		kustomization.Replicas = upsertReplicas(kustomization.Replicas, replicas)
	}
	if len(kustomize.CommonLabels) != 0 {
		labels := envsubstStringMap(kustomize.CommonLabels, opts.ArgoEnv)
		if err := applySourceKustomizeLabels(kustomization, labels, kustomize); err != nil {
			return nil, err
		}
	}
	if len(kustomize.CommonAnnotations) != 0 {
		annotations := cloneStringMap(kustomize.CommonAnnotations)
		if kustomize.CommonAnnotationsEnvsubst {
			annotations = envsubstStringMap(annotations, opts.ArgoEnv)
		}
		if err := mergeStringMap(kustomization.CommonAnnotations, annotations, kustomize.ForceCommonAnnotations, "common annotation"); err != nil {
			return nil, err
		}
		if kustomization.CommonAnnotations == nil {
			kustomization.CommonAnnotations = map[string]string{}
		}
		maps.Copy(kustomization.CommonAnnotations, annotations)
	}
	if len(kustomize.Patches) != 0 {
		kustomization.Patches = append(kustomization.Patches, sourceKustomizePatches(kustomize.Patches)...)
	}
	if len(kustomize.Components) != 0 {
		components, dropped, err := sourceKustomizeComponents(dir, boundaryRoot, kustomize.Components, kustomize.IgnoreMissingComponents)
		if err != nil {
			return nil, err
		}
		kustomization.Components = append(kustomization.Components, components...)
		if len(dropped) != 0 {
			diags = append(diags, ignoreMissingComponentsDroppedDiagnostic(components[0], dropped))
		}
	}
	return diags, nil
}

func sourceKustomizeImages(images argoappv1.KustomizeImages, env argoappv1.Env) ([]types.Image, error) {
	out := make([]types.Image, 0, len(images))
	for _, image := range images {
		parsed, err := parseKustomizeImageOverride(env.Envsubst(string(image)))
		if err != nil {
			return nil, err
		}
		out = append(out, parsed)
	}
	return out, nil
}

func parseKustomizeImageOverride(value string) (types.Image, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return types.Image{}, fmt.Errorf("kustomize image override must not be empty")
	}
	if name, replacement, ok := strings.Cut(value, "="); ok {
		name = strings.TrimSpace(name)
		replacement = strings.TrimSpace(replacement)
		if name == "" || replacement == "" {
			return types.Image{}, fmt.Errorf("invalid kustomize image override %q", value)
		}
		image := types.Image{Name: name}
		image.NewName, image.NewTag, image.Digest = splitImageReplacement(replacement)
		return image, nil
	}
	name, tag, digest := splitImageReplacement(value)
	image := types.Image{Name: name, NewTag: tag, Digest: digest}
	if image.Name == "" {
		return types.Image{}, fmt.Errorf("invalid kustomize image override %q", value)
	}
	return image, nil
}

func splitImageReplacement(value string) (name, tag, digest string) {
	if before, after, ok := strings.Cut(value, "@"); ok {
		name = before
		digest = after
		return name, tag, digest
	}
	tagIndex := strings.LastIndex(value, ":")
	slashIndex := strings.LastIndex(value, "/")
	if tagIndex > slashIndex {
		name = value[:tagIndex]
		tag = value[tagIndex+1:]
		return name, tag, digest
	}
	return value, "", ""
}

func upsertImages(existing, overrides []types.Image) []types.Image {
	out := append([]types.Image(nil), existing...)
	for _, override := range overrides {
		found := false
		for i := range out {
			if out[i].Name == override.Name {
				out[i] = override
				found = true
				break
			}
		}
		if !found {
			out = append(out, override)
		}
	}
	return out
}

func sourceKustomizeReplicas(replicas argoappv1.KustomizeReplicas) ([]types.Replica, error) {
	out := make([]types.Replica, 0, len(replicas))
	for _, replica := range replicas {
		count, err := replica.GetIntCount()
		if err != nil {
			return nil, err
		}
		out = append(out, types.Replica{Name: replica.Name, Count: int64(count)})
	}
	return out, nil
}

func upsertReplicas(existing, overrides []types.Replica) []types.Replica {
	out := append([]types.Replica(nil), existing...)
	for _, override := range overrides {
		found := false
		for i := range out {
			if out[i].Name == override.Name {
				out[i] = override
				found = true
				break
			}
		}
		if !found {
			out = append(out, override)
		}
	}
	return out
}

func applySourceKustomizeLabels(kustomization *types.Kustomization, labels map[string]string, kustomize *argoappv1.ApplicationSourceKustomize) error {
	if err := mergeStringMap(kustomization.CommonLabels, labels, kustomize.ForceCommonLabels, "common label"); err != nil { //nolint:staticcheck // CommonLabels remains part of Argo CD source override semantics.
		return err
	}
	for _, existing := range kustomization.Labels {
		if err := mergeStringMap(existing.Pairs, labels, kustomize.ForceCommonLabels, "common label"); err != nil {
			return err
		}
	}
	if kustomize.LabelWithoutSelector || kustomize.LabelIncludeTemplates {
		kustomization.Labels = append(kustomization.Labels, types.Label{
			Pairs:            labels,
			IncludeSelectors: !kustomize.LabelWithoutSelector,
			IncludeTemplates: kustomize.LabelIncludeTemplates,
		})
		return nil
	}
	if kustomization.CommonLabels == nil { //nolint:staticcheck // CommonLabels remains part of Argo CD source override semantics.
		kustomization.CommonLabels = map[string]string{} //nolint:staticcheck // CommonLabels remains part of Argo CD source override semantics.
	}
	maps.Copy(kustomization.CommonLabels, labels) //nolint:staticcheck // CommonLabels remains part of Argo CD source override semantics.
	return nil
}

func mergeStringMap(existing, incoming map[string]string, force bool, field string) error {
	if force {
		return nil
	}
	for key, value := range incoming {
		if existingValue, ok := existing[key]; ok && existingValue != value {
			return fmt.Errorf("kustomize %s %q already exists; set force to override", field, key)
		}
	}
	return nil
}

func envsubstStringMap(in map[string]string, env argoappv1.Env) map[string]string {
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = env.Envsubst(value)
	}
	return out
}

func cloneStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	maps.Copy(out, in)
	return out
}

func sourceKustomizePatches(patches argoappv1.KustomizePatches) []types.Patch {
	out := make([]types.Patch, 0, len(patches))
	for _, patch := range patches {
		out = append(out, types.Patch{
			Path:    patch.Path,
			Patch:   patch.Patch,
			Target:  sourceKustomizePatchTarget(patch.Target),
			Options: sourceKustomizePatchOptions(patch.Options),
		})
	}
	return out
}

func sourceKustomizePatchTarget(target *argoappv1.KustomizeSelector) *types.Selector {
	if target == nil {
		return nil
	}
	return &types.Selector{
		Group:              target.Group,
		Version:            target.Version,
		Kind:               target.Kind,
		Name:               target.Name,
		Namespace:          target.Namespace,
		AnnotationSelector: target.AnnotationSelector,
		LabelSelector:      target.LabelSelector,
	}
}

func sourceKustomizePatchOptions(options map[string]bool) *types.PatchArgs {
	if len(options) == 0 {
		return nil
	}
	return &types.PatchArgs{
		AllowNameChange: options["allowNameChange"],
		AllowKindChange: options["allowKindChange"],
	}
}

// sourceKustomizeComponents returns the spec.source.kustomize components
// that rendering adds. Without ignoreMissing every component is added.
//
// With ignoreMissing, drydock mirrors Argo CD v3.6.1
// (util/kustomize/kustomize.go:346-372): the repo-server leaves its component
// loop with `break component` after the first component that holds a
// kustomization file. Components before it that fail the check are skipped,
// the first valid one is added, and every later component is dropped without
// a check or a log line. dropped names the later components that hold a
// kustomization file, so the caller can warn about the resources they would
// have added. dropped leaves out a later entry that resolves to the same
// directory as the kept component, such as a repeat of it or the same path
// with extra slashes. The repo-server resolves both to one componentDir
// (filepath.Rel(repoRoot, filepath.Join(path, c)) at kustomize.go:357), so no
// resources are missing. This is believed to be an upstream bug: argoproj/argo-cd#26576
// (merge d631fcdfb) turned an earlier `continue` into `break component`.
// Revisit this when upstream fixes it.
func sourceKustomizeComponents(dir, boundaryRoot string, components []string, ignoreMissing bool) (kept, dropped []string, err error) {
	if !ignoreMissing {
		return append(make([]string, 0, len(components)), components...), nil, nil
	}
	kept = make([]string, 0, 1)
	var keptRel string
	for _, component := range components {
		if len(kept) != 0 {
			// The workspace step (sourceKustomizeWorkspaceAdditions) already
			// ran ignoreMissingComponentDecision on every component, so it
			// rejected a later absolute ref that holds a kustomization file.
			// This tolerant probe exists only for the warning.
			later, err := probeIgnoreMissingComponent(dir, boundaryRoot, component)
			if err != nil {
				return nil, nil, err
			}
			if later.keep && later.rel != keptRel {
				dropped = append(dropped, component)
			}
			continue
		}
		decision, err := ignoreMissingComponentDecision(dir, boundaryRoot, component)
		if err != nil {
			return nil, nil, err
		}
		if decision.keep {
			kept = append(kept, component)
			keptRel = decision.rel
		}
	}
	return kept, dropped, nil
}

// ignoreMissingComponentsDroppedDiagnostic warns that Argo CD v3.6.1 adds
// only the first valid component under ignoreMissingComponents, and names
// each later component that holds a kustomization file. It is not exempt
// from --strict: the dropped components' resources are missing from the
// render.
func ignoreMissingComponentsDroppedDiagnostic(first string, dropped []string) diagnostic.Diagnostic {
	quoted := make([]string, 0, len(dropped))
	for _, component := range dropped {
		quoted = append(quoted, strconv.Quote(component))
	}
	return diagnostic.Diagnostic{
		Code:     KustomizeIgnoreMissingComponentsDroppedCode,
		Severity: diagnostic.SeverityWarning,
		Category: "kustomize",
		Message: fmt.Sprintf("kustomize ignoreMissingComponents adds only the first valid component %q, as Argo CD v3.6.1 does; dropped later components that hold a kustomization file: %s",
			first, strings.Join(quoted, ", ")),
	}
}

// ignoreMissingComponent is the outcome of the ignoreMissingComponents check
// for one spec.source.kustomize.components entry.
type ignoreMissingComponent struct {
	// keep reports whether the component is added to the kustomization.
	keep bool
	// rel is the cleaned component directory relative to the repository
	// root, the path the repo-server checks. It is empty for a remote ref
	// and a ref escaping the repository.
	rel string
	// probe is the in-boundary directory the check looked at. A skipped
	// component's probe stays a render input (an optional digest path): a
	// kustomization file appearing there changes the render.
	probe string
}

// ignoreMissingComponentDecision mirrors Argo CD v3.6.1's
// ignoreMissingComponents check for one component
// (util/kustomize/kustomize.go:357-368): the ref is joined with the source
// directory and kept only when that directory holds a regular
// kustomization.yaml, kustomization.yml or Kustomization, looked up through
// an os.Root on the repository root so in-root symlinks are followed and
// nothing outside the root is read. As upstream, a remote ref and a ref
// escaping the repository are skipped, and an absolute ref is joined under
// the source directory rather than rejected. A kept absolute ref still
// fails, as drydock never reads absolute paths.
func ignoreMissingComponentDecision(dir, boundaryRoot, ref string) (ignoreMissingComponent, error) {
	decision, err := probeIgnoreMissingComponent(dir, boundaryRoot, ref)
	if err != nil {
		return ignoreMissingComponent{}, err
	}
	if decision.keep && filepath.IsAbs(ref) {
		return ignoreMissingComponent{}, fmt.Errorf("kustomize components %q must be relative", ref)
	}
	return decision, nil
}

// probeIgnoreMissingComponent runs the ignoreMissingComponents check for one
// component without rejecting an absolute ref: keep reports whether the
// repo-server would find a kustomization file for it.
func probeIgnoreMissingComponent(dir, boundaryRoot, ref string) (ignoreMissingComponent, error) {
	if isRemoteKustomizeRef(ref) {
		return ignoreMissingComponent{}, nil
	}
	boundaryRoot = filepath.Clean(boundaryRoot)
	componentDir := filepath.Clean(filepath.Join(dir, filepath.FromSlash(ref)))
	rel, inside := relInsideBoundary(boundaryRoot, componentDir)
	if !inside {
		return ignoreMissingComponent{}, nil
	}
	decision := ignoreMissingComponent{rel: rel}
	if !pathsafety.RelEntersGit(rel) {
		decision.probe = componentDir
	}
	found, err := componentHasKustomizationFile(boundaryRoot, rel)
	if err != nil {
		return ignoreMissingComponent{}, err
	}
	decision.keep = found
	return decision, nil
}

func relInsideBoundary(boundaryRoot, path string) (string, bool) {
	rel, err := filepath.Rel(boundaryRoot, path)
	if err != nil || pathsafety.RelEscapes(rel) {
		return "", false
	}
	return rel, true
}

// componentHasKustomizationFile reports whether rel, under boundaryRoot,
// holds a regular kustomization file. Like the repo-server, any Stat error
// (missing, escaping symlink, not a directory) means "not found".
func componentHasKustomizationFile(boundaryRoot, rel string) (bool, error) {
	root, err := os.OpenRoot(boundaryRoot)
	if err != nil {
		return false, fmt.Errorf("open repository root for kustomize components: %w", err)
	}
	defer func() { _ = root.Close() }()
	for _, name := range kustomizationFileNames {
		info, err := root.Stat(filepath.Join(rel, name))
		if err == nil && info.Mode().IsRegular() {
			return true, nil
		}
	}
	return false, nil
}
