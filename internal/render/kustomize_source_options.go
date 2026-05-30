package render

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/diagnostic"
	"sigs.k8s.io/kustomize/api/types"
	"sigs.k8s.io/kustomize/kyaml/resid"
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

//nolint:gocyclo // Mirrors Argo CD's source-level Kustomize override surface explicitly.
func applySourceKustomizeOptions(kustomization *types.Kustomization, dir, boundaryRoot string, opts RenderOptions) error {
	kustomize := opts.Kustomize
	if kustomize == nil {
		return nil
	}
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
			return err
		}
		kustomization.Images = upsertImages(kustomization.Images, images)
	}
	if len(kustomize.Replicas) != 0 {
		replicas, err := sourceKustomizeReplicas(kustomize.Replicas)
		if err != nil {
			return err
		}
		kustomization.Replicas = upsertReplicas(kustomization.Replicas, replicas)
	}
	if len(kustomize.CommonLabels) != 0 {
		labels := envsubstStringMap(kustomize.CommonLabels, opts.ArgoEnv)
		if err := applySourceKustomizeLabels(kustomization, labels, kustomize); err != nil {
			return err
		}
	}
	if len(kustomize.CommonAnnotations) != 0 {
		annotations := cloneStringMap(kustomize.CommonAnnotations)
		if kustomize.CommonAnnotationsEnvsubst {
			annotations = envsubstStringMap(annotations, opts.ArgoEnv)
		}
		if err := mergeStringMap(kustomization.CommonAnnotations, annotations, kustomize.ForceCommonAnnotations, "common annotation"); err != nil {
			return err
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
		components, err := sourceKustomizeComponents(dir, boundaryRoot, kustomize.Components, kustomize.IgnoreMissingComponents)
		if err != nil {
			return err
		}
		kustomization.Components = append(kustomization.Components, components...)
	}
	return nil
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
		ResId: resid.ResId{
			Gvk: resid.Gvk{
				Group:   target.Group,
				Version: target.Version,
				Kind:    target.Kind,
			},
			Name:      target.Name,
			Namespace: target.Namespace,
		},
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

func sourceKustomizeComponents(dir, boundaryRoot string, components []string, ignoreMissing bool) ([]string, error) {
	out := make([]string, 0, len(components))
	for _, component := range components {
		if ignoreMissing {
			exists, err := sourceKustomizeComponentExists(dir, boundaryRoot, component)
			if err != nil {
				return nil, err
			}
			if !exists {
				continue
			}
		}
		out = append(out, component)
	}
	return out, nil
}

func sourceKustomizeComponentExists(dir, boundaryRoot, ref string) (bool, error) {
	if isRemoteKustomizeRef(ref) {
		return true, nil
	}
	if filepath.IsAbs(ref) {
		return false, fmt.Errorf("kustomize components %q must be relative", ref)
	}
	path := filepath.Join(dir, filepath.FromSlash(ref))
	if err := rejectPathOutsideBoundary("kustomize components", path, boundaryRoot); err != nil {
		return false, err
	}
	if _, err := os.Lstat(path); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
