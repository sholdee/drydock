package render

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/pathsafety"
	"sigs.k8s.io/kustomize/api/types"
	sigsyaml "sigs.k8s.io/yaml"
)

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
// content is its text. originDir and originRoot are the source directory
// and repository root in the original repository: the
// ignoreMissingComponents check and the component globs look there, because
// the render workspace holds only the files the render reads. A kept
// component whose name holds glob characters, such as a folder named "[a]",
// is not in the workspace: only its glob matches are.
//
//nolint:gocyclo // Mirrors Argo CD's source-level Kustomize override surface explicitly.
func applySourceKustomizeOptions(kustomization *types.Kustomization, kustomizationFile string, content []byte, originDir, originRoot string, opts RenderOptions) error {
	kustomize := opts.Kustomize
	if kustomize == nil {
		return nil
	}
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
			return err
		}
		edited = edited || written
	}
	if len(kustomize.Replicas) != 0 {
		args := make([]string, 0, len(kustomize.Replicas))
		for _, replica := range kustomize.Replicas {
			count, err := replica.GetIntCount()
			if err != nil {
				return err
			}
			args = append(args, fmt.Sprintf("%s=%d", replica.Name, count))
		}
		written, err := editSetKustomizeReplicas(kustomization, args)
		if err != nil {
			return err
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
			return err
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
			return err
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
				return err
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
		components, err := sourceKustomizeComponents(originDir, originRoot, kustomize.Components, kustomize.IgnoreMissingComponents)
		if err != nil {
			return err
		}
		if len(components) != 0 {
			if err := editAddKustomizeComponents(kustomization, filepath.Base(kustomizationFile), originDir, originRoot, components); err != nil {
				return err
			}
		}
	}
	return patchErr
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
// With ignoreMissing, drydock mirrors Argo CD v3.6.2
// (util/kustomize/kustomize.go:345-372): the repo-server checks every
// component in list order, adds each one that holds a kustomization file,
// and skips each one that fails the check. A repeated component goes to
// `kustomize edit add component` as often as it is listed, and that command
// drops an entry with the same text. Argo CD v3.6.1 left the loop after the
// first valid component.
//
// The check runs in the original repository (dir, boundaryRoot), as the
// repo-server runs it on its checkout. The render workspace holds the glob
// matches of a kept component, not always the kept folder itself.
func sourceKustomizeComponents(dir, boundaryRoot string, components []string, ignoreMissing bool) ([]string, error) {
	if !ignoreMissing {
		return append(make([]string, 0, len(components)), components...), nil
	}
	selection, err := selectIgnoreMissingComponents(dir, boundaryRoot, components)
	if err != nil {
		return nil, err
	}
	return selection.kept, nil
}

// ignoreMissingComponentsSelection is the outcome of the Argo CD v3.6.2
// ignoreMissingComponents loop (util/kustomize/kustomize.go:355-372) over
// spec.source.kustomize.components.
type ignoreMissingComponentsSelection struct {
	// kept holds each component that holds a kustomization file, as written
	// and in list order. It is never nil. keptRels holds the
	// repository-relative directory of each one.
	kept     []string
	keptRels []string
	// skipped holds the check outcome of each component that is not kept.
	skipped []ignoreMissingComponent
}

// selectIgnoreMissingComponents runs the ignoreMissingComponents check on
// every component in order, as the repo-server does.
func selectIgnoreMissingComponents(dir, boundaryRoot string, components []string) (ignoreMissingComponentsSelection, error) {
	selection := ignoreMissingComponentsSelection{kept: []string{}}
	for _, component := range components {
		decision, err := ignoreMissingComponentDecision(dir, boundaryRoot, component)
		if err != nil {
			return ignoreMissingComponentsSelection{}, err
		}
		if !decision.keep {
			selection.skipped = append(selection.skipped, decision)
			continue
		}
		selection.kept = append(selection.kept, component)
		selection.keptRels = append(selection.keptRels, decision.rel)
	}
	return selection, nil
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

// ignoreMissingComponentDecision mirrors Argo CD v3.6.2's
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
