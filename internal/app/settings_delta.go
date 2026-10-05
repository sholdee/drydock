package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/project"
)

// settingsDelta is how the Argo CD settings of the two sides of a
// changed-only diff differ, classed by what each change reaches
// (classifySettingsChange). The zero value reaches nothing.
type settingsDelta struct {
	// renderAll reports a change that reaches every Application: a setting a
	// render or a diff reads (renderAllSettingsSignature), or a cluster entry
	// validation cannot find by server.
	renderAll bool
	// repositories holds every repository entry that was added, removed, or
	// modified, from both sides, keyed by its settings key.
	repositories map[string][]project.RepositoryMatch
	// clusterServers holds the normalized server of every cluster entry that
	// was added, removed, or modified, from both sides; clusterNames holds
	// the names validation can find those entries by: the name, and the
	// server it falls back to.
	clusterServers, clusterNames map[string]struct{}
	// projects names the AppProjects a changed cluster entry reaches as a
	// whole (projectScopedClusters).
	projects map[string]struct{}
}

// classifySettingsChange classes how left and right differ:
//
//   - render-affecting settings (tracking, the instance label, Kustomize
//     build options, Helm value file schemes, CMP definitions and discovery,
//     resource exclusions and inclusions, compare options, and the resource
//     customizations a diff or a health check reads) and the OCI Helm
//     repositories set reach every render or diff: renderAll;
//   - a repository or cluster entry reaches only the Applications that use
//     it, through project validation (settingsDelta.selects);
//   - command parameters, provenance, resource.ignoreResourceUpdatesEnabled,
//     and the ignoreResourceUpdates and actions customizations reach no
//     build, diff, or test output: nothing.
//
// A field the classes do not name is render-affecting: the signatures
// compare every field the settings serialize, and an entry counts as changed
// when any field but its provenance differs.
func classifySettingsChange(left, right config.ArgoSettings) (settingsDelta, error) {
	leftSig, err := changedOnlySettingsSignature(left)
	if err != nil {
		return settingsDelta{}, err
	}
	rightSig, err := changedOnlySettingsSignature(right)
	if err != nil {
		return settingsDelta{}, err
	}
	if leftSig == rightSig {
		return settingsDelta{}, nil
	}
	leftRenderSig, err := renderAllSettingsSignature(left)
	if err != nil {
		return settingsDelta{}, err
	}
	rightRenderSig, err := renderAllSettingsSignature(right)
	if err != nil {
		return settingsDelta{}, err
	}
	if leftRenderSig != rightRenderSig {
		return settingsDelta{renderAll: true}, nil
	}
	delta := settingsDelta{
		repositories:   map[string][]project.RepositoryMatch{},
		clusterServers: map[string]struct{}{},
		clusterNames:   map[string]struct{}{},
		projects:       map[string]struct{}{},
	}
	delta.addRepositories(left.HelmRepositories, right.HelmRepositories)
	if !delta.addClusters(left.Clusters, right.Clusters) {
		return settingsDelta{renderAll: true}, nil
	}
	return delta, nil
}

// renderAllSettingsSignature is changedOnlySettingsSignature over the
// settings whose change reaches every Application. It leaves out the
// repository and cluster entries, which classifySettingsChange scopes, and
// the settings no build, diff, or test output reads. It keeps the one thing
// repository entries change in every render: the OCI Helm repositories
// (ociChartRepositoriesFromSettings) that chart sources, Helm dependencies,
// and Kustomize helmCharts anywhere resolve their repository kind against.
func renderAllSettingsSignature(settings config.ArgoSettings) (string, error) {
	ociRepositories := slices.Sorted(maps.Keys(ociChartRepositoriesFromSettings(settings)))
	settings.HelmRepositories = nil
	settings.Clusters = nil
	settings.IgnoreResourceUpdatesEnabled = config.Value[bool]{}
	settings.ResourceCustomizations = renderAllResourceCustomizations(settings.ResourceCustomizations)
	sig, err := changedOnlySettingsSignature(settings)
	if err != nil {
		return "", err
	}
	data, err := json.Marshal([]any{sig, ociRepositories})
	if err != nil {
		return "", fmt.Errorf("fingerprint render-affecting Argo CD settings: %w", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

// renderAllResourceCustomizations returns a copy of customizations without
// what no build, diff, or test output reads: ignoreResourceUpdates and
// actions (only diag summarizes them) and provenance. A customization left
// with nothing is dropped, as if it were never declared.
func renderAllResourceCustomizations(customizations map[string]config.ResourceCustomization) map[string]config.ResourceCustomization {
	out := make(map[string]config.ResourceCustomization, len(customizations))
	for key, customization := range customizations {
		customization.IgnoreResourceUpdates = config.OverrideIgnoreDifferences{}
		customization.Actions = config.ResourceActionsSummary{}
		customization.Provenance = config.Provenance{}
		if reflect.ValueOf(customization).IsZero() {
			continue
		}
		out[key] = customization
	}
	return out
}

// addRepositories records every repository entry that differs between left
// and right in anything but provenance, with both sides' values.
func (d *settingsDelta) addRepositories(left, right map[string]config.RepositorySettings) {
	for _, key := range unionKeys(left, right) {
		leftRepo, inLeft := left[key]
		rightRepo, inRight := right[key]
		leftRepo.Provenance, rightRepo.Provenance = config.Provenance{}, config.Provenance{}
		if inLeft == inRight && reflect.DeepEqual(leftRepo, rightRepo) {
			continue
		}
		if inLeft {
			d.repositories[key] = append(d.repositories[key], project.NewRepositoryMatch(key, leftRepo))
		}
		if inRight {
			d.repositories[key] = append(d.repositories[key], project.NewRepositoryMatch(key, rightRepo))
		}
	}
}

// addClusters records every cluster entry that differs between left and
// right in anything but provenance, with both sides' values. It returns
// false when such an entry has no server: validation cannot find it by
// server, so no destination match scopes it.
func (d *settingsDelta) addClusters(left, right map[string]config.ClusterSettings) bool {
	for _, key := range unionKeys(left, right) {
		leftCluster, inLeft := left[key]
		rightCluster, inRight := right[key]
		leftCluster.Provenance, rightCluster.Provenance = config.Provenance{}, config.Provenance{}
		if inLeft == inRight && reflect.DeepEqual(leftCluster, rightCluster) {
			continue
		}
		if inLeft && !d.addCluster(key, leftCluster) {
			return false
		}
		if inRight && !d.addCluster(key, rightCluster) {
			return false
		}
	}
	return true
}

func (d *settingsDelta) addCluster(key string, cluster config.ClusterSettings) bool {
	servers := []string{project.NormalizeClusterServer(key), project.NormalizeClusterServer(cluster.Server)}
	if slices.Contains(servers, "") {
		return false
	}
	for _, server := range servers {
		d.clusterServers[server] = struct{}{}
		d.clusterNames[server] = struct{}{}
	}
	if name := strings.TrimSpace(cluster.Name); name != "" {
		d.clusterNames[name] = struct{}{}
	}
	if cluster.Project != "" {
		d.projects[cluster.Project] = struct{}{}
	}
	return true
}

func unionKeys[V any](left, right map[string]V) []string {
	keys := slices.Collect(maps.Keys(left))
	for key := range right {
		if _, ok := left[key]; !ok {
			keys = append(keys, key)
		}
	}
	slices.Sort(keys)
	return keys
}

// scoped reports whether a changed repository or cluster entry can reach an
// Application.
func (d settingsDelta) scoped() bool {
	return len(d.repositories) > 0 || len(d.clusterServers) > 0
}

// selects reports whether a changed repository or cluster entry reaches
// application through project validation: a source (spec.source or
// spec.sources, $ref sources included) uses a changed repository, or its
// project is one a changed project-scoped repository reaches
// (project.RepositoryMatch); its destination names or points at a changed
// cluster; or its project is one a changed project-scoped cluster reaches.
func (d settingsDelta) selects(application argoappv1.Application) bool {
	if !d.scoped() {
		return false
	}
	if _, ok := d.projects[application.Spec.GetProject()]; ok {
		return true
	}
	destination := application.Spec.Destination
	if _, ok := d.clusterServers[project.NormalizeClusterServer(destination.Server)]; ok {
		return true
	}
	if _, ok := d.clusterNames[strings.TrimSpace(destination.Name)]; ok {
		return true
	}
	for _, matches := range d.repositories {
		for _, match := range matches {
			if match.Reaches(application) {
				return true
			}
		}
	}
	return false
}

// withSettingsSelection adds to each side's selection every Application, on
// either side, that delta selects, and every Application those render
// (selectRenderedDescendants). It returns how many Applications delta
// selects directly.
func withSettingsSelection(left, right selectionSide, leftSelected, rightSelected []argoappv1.Application, delta settingsDelta) ([]argoappv1.Application, []argoappv1.Application, int) {
	if !delta.scoped() {
		return leftSelected, rightSelected, 0
	}
	keys := map[string]struct{}{}
	for _, inputs := range [][]ApplicationSelectionInput{left.inputs, right.inputs} {
		for _, input := range inputs {
			if delta.selects(input.Application) {
				keys[applicationKey(input.Application)] = struct{}{}
			}
		}
	}
	reached := len(keys)
	if reached == 0 {
		return leftSelected, rightSelected, 0
	}
	selectRenderedDescendants(keys, left.inputs, right.inputs)
	for _, application := range slices.Concat(leftSelected, rightSelected) {
		keys[applicationKey(application)] = struct{}{}
	}
	return selectedByKey(left.inputs, keys), selectedByKey(right.inputs, keys), reached
}
