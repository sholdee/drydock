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
// the source kustomization. dir and boundaryRoot are the source directory and
// repository root in the render workspace. originDir and originRoot are the
// same paths in the original repository: the workspace does not hold the
// components that ignoreMissingComponents drops, so the dropped-component
// warning looks for them there. The returned diagnostics report options that
// Argo CD v3.6.1 accepts but does not fully apply.
//
//nolint:gocyclo // Mirrors Argo CD's source-level Kustomize override surface explicitly.
func applySourceKustomizeOptions(kustomization *types.Kustomization, dir, boundaryRoot, originDir, originRoot string, opts RenderOptions) ([]diagnostic.Diagnostic, error) {
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
		components, dropped, err := sourceKustomizeComponents(dir, boundaryRoot, originDir, originRoot, kustomize.Components, kustomize.IgnoreMissingComponents)
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
// (util/kustomize/kustomize.go:345-372): the repo-server leaves its component
// loop with `break component` (kustomize.go:366) after the first component
// that holds a kustomization file. Components before it that fail the check
// are skipped, and the first valid one is added. Every later component is
// dropped: the repo-server never reads, checks or copies it, so drydock does
// not either, and a broken later component cannot fail the render.
//
// dropped names the later components that hold a kustomization file, so the
// caller can warn about the resources they would have added. This lookup is
// drydock's own and only feeds the warning. It runs in the original
// repository (originDir, originRoot), because the render workspace does not
// hold the dropped components. It looks only for a regular kustomization
// file, through an os.Root on originRoot, so it reads nothing outside the
// repository. dropped names each directory once, keyed by the cleaned
// repository-relative path that the repo-server resolves
// (filepath.Rel(repoRoot, filepath.Join(path, c)) at kustomize.go:357), and
// leaves out a later entry that resolves to the kept component, such as a
// repeat of it or the same path with extra slashes: no resources are missing
// for it.
//
// This is believed to be an upstream bug: argoproj/argo-cd#26576 (merge
// d631fcdfb) turned an earlier `continue` into `break component`. Revisit
// this when upstream fixes it.
func sourceKustomizeComponents(dir, boundaryRoot, originDir, originRoot string, components []string, ignoreMissing bool) (kept, dropped []string, err error) {
	if !ignoreMissing {
		return append(make([]string, 0, len(components)), components...), nil, nil
	}
	selection, err := selectIgnoreMissingComponents(dir, boundaryRoot, components)
	if err != nil {
		return nil, nil, err
	}
	if !selection.found {
		return []string{}, nil, nil
	}
	dropped, err = droppedIgnoreMissingComponents(originDir, originRoot, selection.keptRel, selection.later)
	if err != nil {
		return nil, nil, err
	}
	return []string{selection.kept}, dropped, nil
}

// ignoreMissingComponentsSelection is the outcome of the Argo CD v3.6.1
// ignoreMissingComponents loop (util/kustomize/kustomize.go:355-372) over
// spec.source.kustomize.components.
type ignoreMissingComponentsSelection struct {
	// found reports whether a component holds a kustomization file.
	found bool
	// kept is the first component that holds a kustomization file, the only
	// one the repo-server adds; keptRel is its repository-relative
	// directory.
	kept    string
	keptRel string
	// skipped holds the check outcome of each component before kept.
	skipped []ignoreMissingComponent
	// later holds the components after kept, which the loop never reaches
	// (`break component` at kustomize.go:366).
	later []string
}

// selectIgnoreMissingComponents runs the ignoreMissingComponents check on
// each component in order and stops at the first valid one, as the
// repo-server does. Components after it are returned unchecked.
func selectIgnoreMissingComponents(dir, boundaryRoot string, components []string) (ignoreMissingComponentsSelection, error) {
	var selection ignoreMissingComponentsSelection
	for i, component := range components {
		decision, err := ignoreMissingComponentDecision(dir, boundaryRoot, component)
		if err != nil {
			return ignoreMissingComponentsSelection{}, err
		}
		if !decision.keep {
			selection.skipped = append(selection.skipped, decision)
			continue
		}
		selection.found = true
		selection.kept = component
		selection.keptRel = decision.rel
		selection.later = components[i+1:]
		break
	}
	return selection, nil
}

// laterIgnoreMissingComponentDir returns the directory a component after
// the kept one names, and its cleaned path relative to boundaryRoot, the
// path the repo-server would resolve (kustomize.go:357). ok is false for a
// remote ref, a ref escaping the repository, and a ref entering a .git
// directory: drydock never looks there for the warning, so those refs are
// not render inputs.
func laterIgnoreMissingComponentDir(dir, boundaryRoot, ref string) (componentDir, rel string, ok bool) {
	if isRemoteKustomizeRef(ref) {
		return "", "", false
	}
	componentDir = filepath.Clean(filepath.Join(dir, filepath.FromSlash(ref)))
	rel, inside := relInsideBoundary(filepath.Clean(boundaryRoot), componentDir)
	if !inside || pathsafety.RelEntersGit(rel) {
		return "", "", false
	}
	return componentDir, rel, true
}

// droppedIgnoreMissingComponents returns the components in later whose
// directory holds a regular kustomization file, once per directory, without
// the kept component's directory.
func droppedIgnoreMissingComponents(dir, boundaryRoot, keptRel string, later []string) ([]string, error) {
	var dropped []string
	seen := map[string]struct{}{keptRel: {}}
	for _, ref := range later {
		_, rel, ok := laterIgnoreMissingComponentDir(dir, boundaryRoot, ref)
		if !ok {
			continue
		}
		if _, dup := seen[rel]; dup {
			continue
		}
		seen[rel] = struct{}{}
		found, err := componentHasKustomizationFile(filepath.Clean(boundaryRoot), rel)
		if err != nil {
			return nil, err
		}
		if found {
			dropped = append(dropped, ref)
		}
	}
	return dropped, nil
}

// laterIgnoreMissingComponentInputs returns the files the dropped-component
// warning looks at: each kustomization file name in the directory of each
// in-repository component after the kept one. They are optional render
// inputs: the digest records their existence and bytes, which covers the
// warning, without reading the rest of a component the render drops. When
// the ref names an existing path that is not a directory, that path itself
// is the input instead: the digest records its type, so it turning into a
// directory changes the key.
func laterIgnoreMissingComponentInputs(dir, boundaryRoot string, later []string) []string {
	var inputs []string
	for _, ref := range later {
		componentDir, _, ok := laterIgnoreMissingComponentDir(dir, boundaryRoot, ref)
		if !ok {
			continue
		}
		if info, err := os.Lstat(componentDir); err == nil && !info.IsDir() {
			inputs = append(inputs, componentDir)
			continue
		}
		for _, name := range kustomizationFileNames {
			inputs = append(inputs, filepath.Join(componentDir, name))
		}
	}
	return inputs
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
	if !found {
		return decision, nil
	}
	if filepath.IsAbs(ref) {
		return ignoreMissingComponent{}, fmt.Errorf("kustomize components %q must be relative", ref)
	}
	decision.keep = true
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
