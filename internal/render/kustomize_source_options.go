package render

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/pathsafety"
	"sigs.k8s.io/kustomize/api/types"
	sigsyaml "sigs.k8s.io/yaml"
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
// the source kustomization, in the order and with the checks of the
// `kustomize edit` commands that Argo CD v3.6.1 runs before `kustomize
// build` (util/kustomize/kustomize.go:174-401): namePrefix, nameSuffix,
// images, replicas, commonLabels, commonAnnotations, namespace, patches,
// then components. kustomizationFile is the source kustomization file, and
// content is its text. dir and boundaryRoot are the source directory and
// repository root in the render workspace. originDir and originRoot are the
// same paths in the original repository: the component globs, and the
// dropped-component warning, look there, because the workspace holds only
// the files the render reads. The returned diagnostics report options that
// Argo CD v3.6.1 accepts but does not fully apply.
//
//nolint:gocyclo // Mirrors Argo CD's source-level Kustomize override surface explicitly.
func applySourceKustomizeOptions(kustomization *types.Kustomization, kustomizationFile string, content []byte, dir, boundaryRoot, originDir, originRoot string, opts RenderOptions) ([]diagnostic.Diagnostic, error) {
	kustomize := opts.Kustomize
	if kustomize == nil {
		return nil, nil
	}
	var diags []diagnostic.Diagnostic
	// edited reports whether an edit command wrote the kustomization file
	// before the patches are added.
	edited := false
	if kustomize.NamePrefix != "" {
		kustomization.NamePrefix = kustomize.NamePrefix
		edited = true
	}
	if kustomize.NameSuffix != "" {
		kustomization.NameSuffix = kustomize.NameSuffix
		edited = true
	}
	if len(kustomize.Images) != 0 {
		// The build environment reaches each image (kustomize.go:202-205).
		args := make([]string, 0, len(kustomize.Images))
		for _, image := range kustomize.Images {
			args = append(args, opts.ArgoEnv.Envsubst(string(image)))
		}
		written, err := editSetKustomizeImages(kustomization, args)
		if err != nil {
			return nil, err
		}
		edited = edited || written
	}
	if len(kustomize.Replicas) != 0 {
		args := make([]string, 0, len(kustomize.Replicas))
		for _, replica := range kustomize.Replicas {
			count, err := replica.GetIntCount()
			if err != nil {
				return nil, err
			}
			args = append(args, fmt.Sprintf("%s=%d", replica.Name, count))
		}
		written, err := editSetKustomizeReplicas(kustomization, args)
		if err != nil {
			return nil, err
		}
		edited = edited || written
	}
	if len(kustomize.CommonLabels) != 0 {
		// The build environment always reaches the label values
		// (kustomize.go:247-250).
		labels := envsubstStringMap(kustomize.CommonLabels, opts.ArgoEnv)
		written, err := editAddKustomizeLabels(kustomization, labels, kustomizeEditMetadataOptions{
			force:            kustomize.ForceCommonLabels,
			withoutSelector:  kustomize.LabelWithoutSelector,
			includeTemplates: kustomize.LabelIncludeTemplates,
		})
		if err != nil {
			return nil, err
		}
		edited = edited || written
	}
	if len(kustomize.CommonAnnotations) != 0 {
		annotations := kustomize.CommonAnnotations
		if kustomize.CommonAnnotationsEnvsubst {
			annotations = envsubstStringMap(annotations, opts.ArgoEnv)
		}
		written, err := editAddKustomizeAnnotations(kustomization, annotations, kustomize.ForceCommonAnnotations)
		if err != nil {
			return nil, err
		}
		edited = edited || written
	}
	if kustomize.Namespace != "" {
		kustomization.Namespace = kustomize.Namespace
		edited = true
	}
	// kustomize reads the patches only in the build, or in the next edit
	// command, so a patch error comes after the component globs.
	var patchErr error
	if len(kustomize.Patches) != 0 {
		if !edited {
			if err := sourceKustomizationPatchesField(content); err != nil {
				return nil, err
			}
		}
		patches, err := decodeSourceKustomizePatches(kustomize.Patches)
		if err != nil {
			patchErr = err
		} else {
			kustomization.Patches = append(kustomization.Patches, patches...)
		}
	}
	if len(kustomize.Components) != 0 {
		components, dropped, err := sourceKustomizeComponents(dir, boundaryRoot, originDir, originRoot, kustomize.Components, kustomize.IgnoreMissingComponents)
		if err != nil {
			return nil, err
		}
		if len(components) != 0 {
			if err := editAddKustomizeComponents(kustomization, filepath.Base(kustomizationFile), originDir, originRoot, components); err != nil {
				return nil, err
			}
		}
		if len(dropped) != 0 {
			diags = append(diags, ignoreMissingComponentsDroppedDiagnostic(components[0], dropped))
		}
	}
	if patchErr != nil {
		return nil, patchErr
	}
	return diags, nil
}

// sourceKustomizationPatchesField mirrors how Argo CD v3.6.1 reads the
// patches field of the kustomization file before it appends the source
// patches (util/kustomize/kustomize.go:286-312): the file is read as YAML,
// and a patches field that is there must be a list. A patches field that
// kustomize decodes as no patches, such as "patches:" with no value, fails.
// Any other value that is not a list already fails the kustomize decode.
func sourceKustomizationPatchesField(content []byte) error {
	var kustomization any
	if err := sigsyaml.Unmarshal(content, &kustomization); err != nil {
		return fmt.Errorf("failed to unmarshal kustomization.yaml: %w", err)
	}
	fields, ok := kustomization.(map[string]any)
	if !ok {
		return fmt.Errorf("expected kustomization.yaml to be type map[string]any, but got %T", fields)
	}
	patches, ok := fields["patches"]
	if !ok {
		return nil
	}
	if _, ok := patches.([]any); !ok {
		return fmt.Errorf("expected 'patches' field in kustomization.yaml to be []any, but got %T", patches)
	}
	return nil
}

// decodeSourceKustomizePatches returns the source patches as kustomize
// reads them from the kustomization file that Argo CD v3.6.1 writes
// (util/kustomize/kustomize.go:286-312). kustomize decodes the file as JSON
// with unknown fields disallowed (types.Kustomization.Unmarshal,
// api/types/kustomization.go:340-356). So a patch option other than
// allowNameChange and allowKindChange fails, and the field names match
// without regard to letter case.
func decodeSourceKustomizePatches(patches argoappv1.KustomizePatches) ([]types.Patch, error) {
	data, err := json.Marshal(patches)
	if err != nil {
		return nil, fmt.Errorf("encode kustomize source patches: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var out []types.Patch
	if err := decoder.Decode(&out); err != nil {
		return nil, fmt.Errorf("invalid Kustomization: %w", err)
	}
	return out, nil
}

func envsubstStringMap(in map[string]string, env argoappv1.Env) map[string]string {
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = env.Envsubst(value)
	}
	return out
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
// fails, as drydock never reads absolute paths. That error comes with the
// decision's rel and probe set, so a caller can still own the directory.
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
		return decision, fmt.Errorf("kustomize components %q must be relative", ref)
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
