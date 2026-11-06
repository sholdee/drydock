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
		collector: &kustomizeInputCollector{repoRoot: filepath.Clean(repoRoot), bestEffort: true},
		env:       env,
		seen:      map[string]struct{}{},
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
	return out, nil
}

type kustomizeHelmValuesCollector struct {
	collector *kustomizeInputCollector
	env       argoappv1.Env
	seen      map[string]struct{}
	out       KustomizeHelmValuesInputs
}

func (c *kustomizeHelmValuesCollector) addNode(node kustomizeGraphNode) {
	chartHome := kustomizationChartHome(node.Kustomization)
	for _, helmChart := range node.Kustomization.HelmCharts {
		c.out.ChartNames = append(c.out.ChartNames, helmChart.Name)
		chartPath := localKustomizeHelmChartPath(node.Dir, chartHome, helmChart)
		if rejectPathOutsideBoundary("kustomize helmCharts.name", chartPath, c.collector.repoRoot) == nil {
			for _, rel := range HelmChartValuesFiles(c.collector.repoRoot, chartPath) {
				c.addValuesFile(filepath.Join(c.collector.repoRoot, filepath.FromSlash(rel)))
			}
		}
		for _, rel := range c.helmChartValueFileReads(node, helmChart) {
			c.addValuesFile(filepath.Join(c.collector.repoRoot, filepath.FromSlash(rel)))
		}
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
// writeKustomizeHelmGeneratedValuesFile read them. valuesFile is read only
// when shouldLoadHelmValueFiles allows it: valuesMerge replace with
// valuesInline skips it. additionalValuesFiles are always read. Both go
// through the Helm value file loader, relative to the kustomization
// directory, with globs and env substitution (HelmValueFileReads).
func (c *kustomizeHelmValuesCollector) helmChartValueFileReads(node kustomizeGraphNode, helmChart types.HelmChart) []string {
	var files []string
	if helmChart.ValuesFile != "" {
		if load, err := shouldLoadHelmValueFiles(helmChart.ValuesMerge, helmChart.ValuesInline); err == nil && load {
			files = append(files, helmChart.ValuesFile)
		}
	}
	files = append(files, helmChart.AdditionalValuesFiles...)
	if len(files) == 0 {
		return nil
	}
	nodeRel, err := relativeManifestPath(c.collector.repoRoot, node.Dir)
	if err != nil {
		return nil
	}
	nodeRel = filepath.ToSlash(nodeRel)
	return HelmValueFileReads(c.collector.repoRoot, ResolvedSource{RepoRoot: c.collector.repoRoot, Path: nodeRel}, RenderOptions{
		ValueFiles:             files,
		ValueFilesBaseDir:      nodeRel,
		ValueFilesBoundaryRoot: ".",
		ArgoEnv:                c.env,
	}, false)
}
