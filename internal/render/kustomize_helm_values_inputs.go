package render

import (
	"context"
	"os"
	"path/filepath"
	"sort"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"sigs.k8s.io/kustomize/api/types"
)

// KustomizeHelmValuesInputs is what the helmCharts entries of a Kustomize
// graph read as Helm values.
type KustomizeHelmValuesInputs struct {
	// Files lists the repository-relative local inputs of the graph, as
	// KustomizeSelectionPaths returns them, that are regular files. It leaves
	// out directories, such as a local chart or a directory resource: the
	// graph lists the files that Kustomize reads from them on their own, and
	// a chart directory holds templates, not manifests that Kustomize
	// applies.
	Files []string
	// ValuesFiles lists the repository-relative local Helm values files of
	// the helmCharts entries that exist: valuesFile, additionalValuesFiles,
	// and the values.yaml files of each local chart and its unpacked
	// subcharts, which Helm reads as the chart defaults
	// (HelmChartValuesFiles).
	ValuesFiles []string
	// KustomizeParsedValuesFiles lists the files in ValuesFiles that the
	// render reads with the Kustomize parser (ParseKustomizeHelmValuesFile):
	// a valuesFile that kustomize merges with valuesInline.
	// HelmParsedValuesFiles lists the files that the render reads with the
	// Helm parser (ParseHelmValuesFile). A file can be in both lists when two
	// entries read it.
	KustomizeParsedValuesFiles []string
	HelmParsedValuesFiles      []string
	// ValuesInline holds the valuesInline map of each helmCharts entry that
	// sets one.
	ValuesInline []map[string]any
	// ChartNames lists the name of each helmCharts entry.
	ChartNames []string
}

// KustomizeHelmValues returns the local inputs and the Helm values of the
// Kustomize graph rooted at sourcePath. It is best-effort in the same way as
// KustomizeSelectionPaths: a ref that is remote, missing, outside repoRoot,
// or symlinked is skipped. An error means the graph itself could not be
// read.
//
// env is the Argo CD build environment of the Application, which the Helm
// value file loader substitutes in value file paths.
func KustomizeHelmValues(ctx context.Context, repoRoot, sourcePath string, kustomize *argoappv1.ApplicationSourceKustomize, env argoappv1.Env) (KustomizeHelmValuesInputs, error) {
	paths, graph, err := kustomizeSelectionGraph(ctx, repoRoot, sourcePath, kustomize)
	if err != nil {
		return KustomizeHelmValuesInputs{}, err
	}
	values := kustomizeHelmValuesCollector{
		collector:     &kustomizeInputCollector{repoRoot: filepath.Clean(repoRoot), bestEffort: true},
		env:           env,
		seen:          map[string]struct{}{},
		helmSeen:      map[string]struct{}{},
		kustomizeSeen: map[string]struct{}{},
	}
	for _, rel := range paths {
		info, err := os.Lstat(filepath.Join(values.collector.repoRoot, filepath.FromSlash(rel)))
		if err == nil && info.Mode().IsRegular() {
			values.out.Files = append(values.out.Files, rel)
		}
	}
	for _, node := range graph {
		if err := ctx.Err(); err != nil {
			return KustomizeHelmValuesInputs{}, err
		}
		values.addNode(node)
	}
	out := values.out
	sort.Strings(out.ValuesFiles)
	sort.Strings(out.KustomizeParsedValuesFiles)
	sort.Strings(out.HelmParsedValuesFiles)
	return out, nil
}

type kustomizeHelmValuesCollector struct {
	collector *kustomizeInputCollector
	env       argoappv1.Env
	// helmSeen and kustomizeSeen hold the files already in
	// HelmParsedValuesFiles and KustomizeParsedValuesFiles.
	helmSeen      map[string]struct{}
	kustomizeSeen map[string]struct{}
	seen          map[string]struct{}
	out           KustomizeHelmValuesInputs
}

func (c *kustomizeHelmValuesCollector) addNode(node kustomizeGraphNode) {
	chartHome := kustomizationChartHome(node.Kustomization)
	for _, helmChart := range node.Kustomization.HelmCharts {
		c.out.ChartNames = append(c.out.ChartNames, helmChart.Name)
		chartPath := localKustomizeHelmChartPath(node.Dir, chartHome, helmChart)
		if rejectPathOutsideBoundary("kustomize helmCharts.name", chartPath, c.collector.repoRoot) == nil {
			c.addParsedValuesFiles(HelmChartValuesFiles(c.collector.repoRoot, chartPath), c.helmSeen, &c.out.HelmParsedValuesFiles)
		}
		helmParsed, kustomizeParsed := c.helmChartValueFileReads(node, helmChart)
		c.addParsedValuesFiles(helmParsed, c.helmSeen, &c.out.HelmParsedValuesFiles)
		c.addParsedValuesFiles(kustomizeParsed, c.kustomizeSeen, &c.out.KustomizeParsedValuesFiles)
		if len(helmChart.ValuesInline) > 0 {
			c.out.ValuesInline = append(c.out.ValuesInline, cloneValues(helmChart.ValuesInline))
		}
	}
}

// addValuesFile adds a local values file that exists as a regular file in
// the repository.
func (c *kustomizeHelmValuesCollector) addValuesFile(path string) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return
	}
	if err := rejectSymlinkedPath(c.collector.repoRoot, path); err != nil {
		return
	}
	rel, err := relativeManifestPath(c.collector.repoRoot, path)
	if err != nil {
		return
	}
	rel = filepath.ToSlash(rel)
	if _, ok := c.seen[rel]; ok {
		return
	}
	c.seen[rel] = struct{}{}
	c.out.ValuesFiles = append(c.out.ValuesFiles, rel)
}

// helmChartValueFileReads returns the local values files that the render of
// one helmCharts entry reads, as renderOptionsForKustomizeHelmChart and
// writeKustomizeHelmGeneratedValuesFile read them, split by the parser that
// reads them. With valuesInline, kustomize reads valuesFile with the
// Kustomize parser and merges it with valuesInline, and only when
// shouldLoadHelmValueFiles allows it: valuesMerge replace skips it. Without
// valuesInline, Helm reads valuesFile. Helm always reads
// additionalValuesFiles. Every file goes through the Helm value file loader
// paths, relative to the kustomization directory, with globs and env
// substitution (HelmValueFileReads).
func (c *kustomizeHelmValuesCollector) helmChartValueFileReads(node kustomizeGraphNode, helmChart types.HelmChart) (helmParsed, kustomizeParsed []string) {
	nodeRel, err := relativeManifestPath(c.collector.repoRoot, node.Dir)
	if err != nil {
		return nil, nil
	}
	nodeRel = filepath.ToSlash(nodeRel)
	reads := func(files []string) []string {
		if len(files) == 0 {
			return nil
		}
		return HelmValueFileReads(c.collector.repoRoot, ResolvedSource{RepoRoot: c.collector.repoRoot, Path: nodeRel}, RenderOptions{
			ValueFiles:             files,
			ValueFilesBaseDir:      nodeRel,
			ValueFilesBoundaryRoot: ".",
			ArgoEnv:                c.env,
		}, false)
	}
	primary := make([]string, 0, 1+len(helmChart.AdditionalValuesFiles))
	if helmChart.ValuesFile != "" {
		if load, err := shouldLoadHelmValueFiles(helmChart.ValuesMerge, helmChart.ValuesInline); err == nil && load {
			primary = append(primary, helmChart.ValuesFile)
		}
	}
	if len(helmChart.ValuesInline) != 0 {
		return reads(helmChart.AdditionalValuesFiles), reads(primary)
	}
	return reads(append(primary, helmChart.AdditionalValuesFiles...)), nil
}

// addParsedValuesFiles adds repository-relative values files, which
// HelmValueFileReads or HelmChartValuesFiles already found as regular local
// files, to ValuesFiles and, once each, to the parser list out.
func (c *kustomizeHelmValuesCollector) addParsedValuesFiles(rels []string, seen map[string]struct{}, out *[]string) {
	for _, rel := range rels {
		c.addValuesFile(filepath.Join(c.collector.repoRoot, filepath.FromSlash(rel)))
		if _, ok := seen[rel]; ok {
			continue
		}
		seen[rel] = struct{}{}
		*out = append(*out, rel)
	}
}
