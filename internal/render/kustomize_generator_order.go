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

// kustomizeGenerationStep is one step of the generators of a
// kustomization, in the order that kustomize runs them: the
// configMapGenerator entries, the secretGenerator entries, the helmCharts,
// and then the documents of the generators: entries in list order
// (KustTarget.runGenerators and configureBuiltinGenerators,
// api/internal/target/kusttarget.go:267-296 and
// kusttarget_configplugin.go:36-40 at kustomize/api v0.21.3). kustomize
// adds the output of each step with resWrangler.appendReplaceOrMerge.
type kustomizeGenerationStep struct {
	// merge is the ID that a step with behavior merge or replace changes.
	merge *resid.ResId
	// creates are the IDs that the step adds.
	creates []resid.ResId
	// behavior is the behavior of the added output, for the error text.
	behavior string
	// moved reports that drydock adds the output of the step as resources
	// of the kustomization, before krusty runs any generator: the output of
	// helmCharts, of KSOPS, and the configurations of skipped functions.
	moved bool
}

// kustomizeGeneratorStep returns the step of one configMapGenerator or
// secretGenerator entry. Its output has the name and the namespace of the
// entry. The name suffix comes later.
func kustomizeGeneratorStep(kind string, args types.GeneratorArgs) kustomizeGenerationStep {
	id := resid.NewResIdWithNamespace(resid.NewGvk("", "v1", kind), args.Name, args.Namespace)
	behavior := types.NewGenerationBehavior(args.Behavior)
	if behavior == types.BehaviorMerge || behavior == types.BehaviorReplace {
		return kustomizeGenerationStep{merge: &id, behavior: behavior.String()}
	}
	return kustomizeGenerationStep{creates: []resid.ResId{id}, behavior: behavior.String()}
}

// kustomizeFieldGeneratorSteps returns the steps of the configMapGenerator
// and secretGenerator entries.
func kustomizeFieldGeneratorSteps(kustomization *types.Kustomization) []kustomizeGenerationStep {
	steps := make([]kustomizeGenerationStep, 0, len(kustomization.ConfigMapGenerator)+len(kustomization.SecretGenerator))
	for _, generator := range kustomization.ConfigMapGenerator {
		steps = append(steps, kustomizeGeneratorStep("ConfigMap", generator.GeneratorArgs))
	}
	for _, generator := range kustomization.SecretGenerator {
		steps = append(steps, kustomizeGeneratorStep("Secret", generator.GeneratorArgs))
	}
	return steps
}

// checkKustomizeGeneratorOrder fails a kustomization as kustomize fails it
// when a step with behavior merge or replace names an ID that only a later
// step adds, and drydock moved that later output to the resources, where
// krusty would find it.
//
// kustomize fails such a merge with "does not exist; cannot merge or
// replace", because the ID does not exist yet. When the resources already
// hold the ID, the merge works, and the later output fails with "exists;
// can not use behavior". To tell the two cases, drydock builds the
// resources of the kustomization on their own, only for such a merge, so
// other builds do not pay for it. If that build fails, the real build
// reports the error.
//
// The build of the resources is a finished build: it gives the IDs after
// name suffixes and without the configurations marked local-config, where
// kustomize compares every earlier ID too. Both build fail then, but the
// text can differ.
func checkKustomizeGeneratorOrder(dir string, graphIndex int, resources []string, steps []kustomizeGenerationStep) error {
	if !kustomizeMergeNamesLaterMovedOutput(steps) {
		return nil
	}
	var existing []resid.ResId
	if len(resources) != 0 {
		ids, ok := kustomizeAccumulatedResourceIDs(dir, graphIndex, resources)
		if !ok {
			return nil
		}
		existing = ids
	}
	for _, step := range steps {
		if step.merge != nil {
			if !slices.ContainsFunc(existing, step.merge.Equals) {
				return fmt.Errorf("merging from generator %s %q: id %#v does not exist; cannot merge or replace", step.merge.Kind+"Generator", step.merge.Name, *step.merge)
			}
			continue
		}
		for _, id := range step.creates {
			if slices.ContainsFunc(existing, id.Equals) {
				return fmt.Errorf("merging from generator: id %#v exists; can not use behavior: '%s', behavior must be merge or replace", id, step.behavior)
			}
			existing = append(existing, id)
		}
	}
	return nil
}

// kustomizeMergeNamesLaterMovedOutput reports whether a merge step names
// an ID that a later moved step adds.
func kustomizeMergeNamesLaterMovedOutput(steps []kustomizeGenerationStep) bool {
	for i, step := range steps {
		if step.merge == nil {
			continue
		}
		for _, later := range steps[i+1:] {
			if later.moved && slices.ContainsFunc(later.creates, step.merge.Equals) {
				return true
			}
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
