package render

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"sigs.k8s.io/kustomize/api/krusty"
	"sigs.k8s.io/kustomize/api/types"
	"sigs.k8s.io/kustomize/kyaml/filesys"
	"sigs.k8s.io/kustomize/kyaml/kio"
	"sigs.k8s.io/kustomize/kyaml/resid"
)

// checkKustomizeGeneratorOrder fails a kustomization as kustomize fails it
// when a configMapGenerator or secretGenerator entry with behavior merge or
// replace names a resource that only the helmCharts or the generators: of
// the same kustomization make.
//
// kustomize adds the resources of a kustomization, then the output of its
// generators in this order: configMapGenerator, secretGenerator, helmCharts,
// and then generators: (KustTarget.runGenerators and
// configureBuiltinGenerators, api/internal/target/kusttarget.go:267-296 and
// kusttarget_configplugin.go:36-40 at kustomize/api v0.21.3). Each output
// is added with resWrangler.appendReplaceOrMerge. So a merge into a
// resource that helmCharts make fails with "does not exist; cannot merge or
// replace", because that resource does not exist yet. drydock adds the
// helmCharts and KSOPS output to the resources, where the merge finds it.
//
// When such a merge names a resource that the resources already hold, the
// merge works, and the later output with the same ID fails with "exists;
// can not use behavior: 'unspecified'". To tell the two cases, drydock
// builds the resources of the kustomization on their own, without
// transformers, as kustomize holds them when it runs the generators. It
// does so only for a merge that names a later output, so other builds do
// not pay for it. If that build fails, the real build reports the error.
func checkKustomizeGeneratorOrder(dir string, graphIndex int, kustomization *types.Kustomization, resources, laterOutputs []string) error {
	if len(laterOutputs) == 0 {
		return nil
	}
	merges := kustomizeMergeGeneratorIDs(kustomization)
	if len(merges) == 0 {
		return nil
	}
	later, ok := kustomizeResourceFileIDs(dir, laterOutputs)
	if !ok {
		// The real build reads the same files and reports the error.
		return nil
	}
	if !kustomizeIDsOverlap(merges, later) {
		return nil
	}
	var earlier []resid.ResId
	if len(resources) != 0 {
		ids, ok := kustomizeAccumulatedResourceIDs(dir, graphIndex, resources)
		if !ok {
			return nil
		}
		earlier = ids
	}
	for _, id := range merges {
		if !kustomizeIDsOverlap([]resid.ResId{id}, earlier) {
			return fmt.Errorf("merging from generator %s %q: id %#v does not exist; cannot merge or replace", id.Kind+"Generator", id.Name, id)
		}
	}
	existing := append(append([]resid.ResId(nil), earlier...), kustomizeGeneratorIDs(kustomization)...)
	for _, id := range later {
		if kustomizeIDsOverlap([]resid.ResId{id}, existing) {
			return fmt.Errorf("merging from generator: id %#v exists; can not use behavior: 'unspecified', behavior must be merge or replace", id)
		}
	}
	return nil
}

// kustomizeMergeGeneratorIDs returns the IDs that the configMapGenerator
// and secretGenerator entries with behavior merge or replace make, in the
// order that kustomize runs them. The ID of generated output has the name
// of the entry and its namespace. The name suffix comes later.
func kustomizeMergeGeneratorIDs(kustomization *types.Kustomization) []resid.ResId {
	var out []resid.ResId
	for _, generator := range kustomization.ConfigMapGenerator {
		if isKustomizeMergeBehavior(generator.Behavior) {
			out = append(out, kustomizeGeneratedID("ConfigMap", generator.GeneratorArgs))
		}
	}
	for _, generator := range kustomization.SecretGenerator {
		if isKustomizeMergeBehavior(generator.Behavior) {
			out = append(out, kustomizeGeneratedID("Secret", generator.GeneratorArgs))
		}
	}
	return out
}

// kustomizeGeneratorIDs returns the IDs of every configMapGenerator and
// secretGenerator entry.
func kustomizeGeneratorIDs(kustomization *types.Kustomization) []resid.ResId {
	out := make([]resid.ResId, 0, len(kustomization.ConfigMapGenerator)+len(kustomization.SecretGenerator))
	for _, generator := range kustomization.ConfigMapGenerator {
		out = append(out, kustomizeGeneratedID("ConfigMap", generator.GeneratorArgs))
	}
	for _, generator := range kustomization.SecretGenerator {
		out = append(out, kustomizeGeneratedID("Secret", generator.GeneratorArgs))
	}
	return out
}

func isKustomizeMergeBehavior(behavior string) bool {
	parsed := types.NewGenerationBehavior(behavior)
	return parsed == types.BehaviorMerge || parsed == types.BehaviorReplace
}

func kustomizeGeneratedID(kind string, args types.GeneratorArgs) resid.ResId {
	return resid.NewResIdWithNamespace(resid.NewGvk("", "v1", kind), args.Name, args.Namespace)
}

func kustomizeIDsOverlap(left, right []resid.ResId) bool {
	for _, id := range left {
		if slices.ContainsFunc(right, id.Equals) {
			return true
		}
	}
	return false
}

// kustomizeResourceFileIDs reads the IDs of the resources in generated
// files, relative to dir.
func kustomizeResourceFileIDs(dir string, files []string) ([]resid.ResId, bool) {
	var out []resid.ResId
	for _, file := range files {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(file)))
		if err != nil {
			return nil, false
		}
		nodes, err := (&kio.ByteReader{Reader: bytes.NewReader(data), OmitReaderAnnotations: true}).Read()
		if err != nil {
			return nil, false
		}
		for _, node := range nodes {
			out = append(out, resid.FromRNode(node))
		}
	}
	return out, true
}

// kustomizeAccumulatedResourceIDs builds the resources of a kustomization
// on their own and returns their IDs.
func kustomizeAccumulatedResourceIDs(dir string, graphIndex int, resources []string) ([]resid.ResId, bool) {
	probeRel := filepath.Join(".drydock", "probes", fmt.Sprintf("%03d", graphIndex))
	probeDir := filepath.Join(dir, probeRel)
	defer func() { _ = os.RemoveAll(probeDir) }()
	var content strings.Builder
	content.WriteString("resources:\n")
	for _, resource := range resources {
		fmt.Fprintf(&content, "  - %q\n", filepath.ToSlash(filepath.Join("..", "..", "..", resource)))
	}
	if err := writeGeneratedKustomizeWorkspaceFile(dir, filepath.ToSlash(filepath.Join(probeRel, "kustomization.yaml")), []byte(content.String())); err != nil {
		return nil, false
	}
	options := krusty.MakeDefaultOptions()
	options.LoadRestrictions = types.LoadRestrictionsNone
	resMap, err := krusty.MakeKustomizer(options).Run(filesys.MakeFsOnDisk(), probeDir)
	if err != nil {
		return nil, false
	}
	out := make([]resid.ResId, 0, resMap.Size())
	for _, resource := range resMap.Resources() {
		out = append(out, resource.CurId())
	}
	return out, true
}
