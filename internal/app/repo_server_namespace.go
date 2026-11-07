package app

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/discovery"
	"github.com/sholdee/drydock/internal/project"
	"github.com/sholdee/drydock/internal/render"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	sigsyaml "sigs.k8s.io/yaml"
)

// repoServerCandidate is one repo-server Deployment that may run the helm
// of this Argo CD, with what drydock knows about it.
type repoServerCandidate struct {
	candidate discovery.SettingsCandidate
	// namespace is the pod namespace, and namespaceKnown reports whether
	// drydock could tell it.
	namespace      string
	namespaceKnown bool
	// group names the install: the Application that rendered it, or the
	// directory of the committed file.
	group string
	// withSettings reports that the same install holds an argocd-cm.
	withSettings bool
}

// repoServerHelmNamespace works out the namespace that helm uses in the
// repo-server of the Argo CD that reconciles the discovered Applications
// (config.RepoServerHelmNamespace).
//
// A repo-server Deployment counts when it comes from the same install as a
// discovered argocd-cm, or when it runs in the Argo CD controller
// namespace. drydock reads it from a render of an Application on this
// cluster and from a committed file, with the rules it uses for an
// argocd-cm: a committed file counts only when it also holds Argo CD
// objects or settings, and a committed file that a rendered install read
// is replaced by that render. A Deployment without containers is a patch
// and does not count.
//
// When several repo-servers count, drydock uses the first in this order:
// one from the same install as an argocd-cm, then a rendered one, then by
// path and document index. Another one that gives a different namespace
// adds a warning.
func repoServerHelmNamespace(root string, discovered discovery.Result, settings config.ArgoSettings) (*config.Value[string], []diagnostic.Diagnostic) {
	deployments, serviceAccounts, diags := collectRepoServerCandidates(root, discovered, settings)
	sortRepoServerCandidates(deployments)
	var out *config.Value[string]
	for _, item := range deployments {
		var accounts []*unstructured.Unstructured
		for _, account := range serviceAccounts {
			if account.namespace == item.namespace && account.group == item.group {
				accounts = append(accounts, account.candidate.Object)
			}
		}
		namespace, notes := config.RepoServerHelmNamespace(item.candidate.Object, accounts, item.namespace)
		provenance := diagnostic.Provenance{Path: filepath.ToSlash(item.candidate.Path)}
		name := repoServerDisplayName(item)
		if out != nil {
			if out.Value != namespace {
				diags = append(diags, repoServerWarning(provenance, fmt.Sprintf("the Argo CD repo-server %s gives the Helm namespace %q, but drydock uses %q from %s", name, namespace, out.Value, out.Provenance.Path)))
			}
			continue
		}
		out = &config.Value[string]{Value: namespace, Provenance: provenance}
		if !item.namespaceKnown {
			diags = append(diags, repoServerWarning(provenance, fmt.Sprintf("cannot tell the namespace of the Argo CD repo-server %s; drydock assumes the Argo CD namespace %q, so Helm sources and helmCharts without a namespace render in %q", name, item.namespace, namespace)))
		}
		for _, note := range notes {
			diags = append(diags, repoServerWarning(provenance, fmt.Sprintf("cannot tell the Helm namespace of the Argo CD repo-server %s offline: %s; drydock uses %q for Helm sources and helmCharts without a namespace", name, note, namespace)))
		}
	}
	return out, diags
}

// collectRepoServerCandidates returns the repo-server Deployments that
// count (see repoServerHelmNamespace) and the ServiceAccounts, each with
// its pod namespace and install.
func collectRepoServerCandidates(root string, discovered discovery.Result, settings config.ArgoSettings) ([]repoServerCandidate, []repoServerCandidate, []diagnostic.Diagnostic) {
	inputs := renderedSettingsInputs(discovered.SettingsCandidates)
	controllerNamespace := settings.ControllerNamespace("")
	settingsGroups := map[string]bool{}
	for _, candidate := range discovered.SettingsCandidates {
		if candidate.Kind == "argocd-cm" && !supersededBySettingsRender(candidate, inputs) {
			settingsGroups[repoServerGroup(candidate)] = true
		}
	}
	var diags []diagnostic.Diagnostic
	var deployments, serviceAccounts []repoServerCandidate
	for _, candidate := range discovered.SettingsCandidates {
		if !discovery.IsRepoServerKind(candidate.Kind) || candidate.Object == nil || supersededBySettingsRender(candidate, inputs) {
			continue
		}
		item := repoServerCandidate{candidate: candidate, group: repoServerGroup(candidate)}
		item.withSettings = settingsGroups[item.group]
		if candidate.Tier == discovery.SourceTierStatic {
			namespace, known, remote, nsDiags := staticRepoServerNamespace(root, candidate, discovered.Applications, settings)
			diags = append(diags, nsDiags...)
			if remote {
				continue
			}
			item.namespace, item.namespaceKnown = namespace, known
		} else {
			item.namespace = candidate.Object.GetNamespace()
			item.namespaceKnown = item.namespace != ""
		}
		// An object whose namespace drydock cannot tell runs in the Argo CD
		// namespace, the ServiceAccount as its Deployment.
		if !item.namespaceKnown {
			item.namespace = controllerNamespace
		}
		if candidate.Kind == discovery.RepoServerServiceAccountKind {
			serviceAccounts = append(serviceAccounts, item)
			continue
		}
		if containers, _, _ := unstructured.NestedSlice(candidate.Object.Object, "spec", "template", "spec", "containers"); len(containers) == 0 {
			continue
		}
		if item.withSettings || item.namespace == controllerNamespace {
			deployments = append(deployments, item)
		}
	}
	return deployments, serviceAccounts, diags
}

// sortRepoServerCandidates orders the repo-servers that count: from the
// same install as an argocd-cm first, then rendered before committed, then
// by path and document index.
func sortRepoServerCandidates(deployments []repoServerCandidate) {
	sort.SliceStable(deployments, func(i, j int) bool {
		left, right := deployments[i], deployments[j]
		if left.withSettings != right.withSettings {
			return left.withSettings
		}
		leftRendered := left.candidate.Tier != discovery.SourceTierStatic
		rightRendered := right.candidate.Tier != discovery.SourceTierStatic
		if leftRendered != rightRendered {
			return leftRendered
		}
		if left.candidate.Path != right.candidate.Path {
			return left.candidate.Path < right.candidate.Path
		}
		return left.candidate.DocumentIndex < right.candidate.DocumentIndex
	})
}

func repoServerWarning(provenance diagnostic.Provenance, message string) diagnostic.Diagnostic {
	return diagnostic.Diagnostic{
		Severity:   diagnostic.SeverityWarning,
		Category:   "settings",
		Message:    message,
		Provenance: provenance,
	}
}

func repoServerDisplayName(item repoServerCandidate) string {
	name := item.candidate.Object.GetName()
	if item.namespace != "" {
		name = item.namespace + "/" + name
	}
	return fmt.Sprintf("%s in %s", name, filepath.ToSlash(item.candidate.Path))
}

// repoServerGroup names the install that a candidate comes from: the
// Application that rendered it, or the directory of the committed file.
func repoServerGroup(candidate discovery.SettingsCandidate) string {
	if candidate.Origin != "" {
		return "rendered\x00" + candidate.Origin
	}
	return "static\x00" + path.Dir(filepath.ToSlash(candidate.Path))
}

// staticRepoServerNamespace works out the namespace of a committed
// repo-server object as kustomize and Argo CD place it: the namespace of
// the kustomization in its directory that lists it, which the kustomize
// NamespaceTransformer sets over the object's own, else its own namespace,
// else the destination namespace of the Applications on this cluster that
// render the file. A kustomize.namespace of such an Application wins over
// all of them, because Argo CD sets it on the kustomization. remote
// reports a file that only Applications on other clusters render: that
// repo-server runs another Argo CD.
func staticRepoServerNamespace(root string, candidate discovery.SettingsCandidate, applications []discovery.ApplicationFile, settings config.ArgoSettings) (namespace string, known, remote bool, diags []diagnostic.Diagnostic) {
	file := filepath.ToSlash(candidate.Path)
	renders := staticFileRenders(root, file, applications, settings)
	if renders.remoteOnly {
		return "", false, true, nil
	}
	if len(renders.overrides) > 0 {
		return pickStaticNamespace(file, renders.overrides, "kustomize.namespace values")
	}
	if namespace := kustomizationNamespaceFor(root, path.Dir(file), path.Base(file)); namespace != "" {
		return namespace, true, false, nil
	}
	if namespace := candidate.Object.GetNamespace(); namespace != "" {
		return namespace, true, false, nil
	}
	if len(renders.destinations) == 0 {
		return "", false, false, nil
	}
	return pickStaticNamespace(file, renders.destinations, "destination namespaces")
}

func pickStaticNamespace(file string, namespaces []string, what string) (string, bool, bool, []diagnostic.Diagnostic) {
	var diags []diagnostic.Diagnostic
	if len(namespaces) > 1 {
		diags = append(diags, repoServerWarning(diagnostic.Provenance{Path: file}, fmt.Sprintf("Applications render the Argo CD repo-server in %s with the %s %s; drydock uses %q", file, what, strings.Join(namespaces, ", "), namespaces[0])))
	}
	return namespaces[0], true, false, diags
}

// staticRenders is what the discovered Applications do with a committed
// file.
type staticRenders struct {
	// destinations and overrides are the destination namespaces and the
	// kustomize.namespace values of the Applications on this cluster that
	// render the file, sorted and without duplicates.
	destinations []string
	overrides    []string
	// remoteOnly reports that only Applications on other clusters render
	// the file.
	remoteOnly bool
}

func staticFileRenders(root, file string, applications []discovery.ApplicationFile, settings config.ArgoSettings) staticRenders {
	var out staticRenders
	inCluster, remote := 0, 0
	for _, appFile := range applications {
		if appFile.Tier == discovery.SourceTierExternalManifest {
			continue
		}
		for _, source := range applicationSources(appFile.Application) {
			if !sourceRendersCommittedFile(root, source, file, sourceTypeGatesFromSettings(settings)) {
				continue
			}
			server, ok := project.ResolveDestinationServer(appFile.Application.Spec.Destination, settings)
			if ok && server != argoappv1.KubernetesInternalAPIServerAddr {
				remote++
				break
			}
			inCluster++
			out.destinations = appendSortedUnique(out.destinations, appFile.Application.Spec.Destination.Namespace)
			if source.Kustomize != nil {
				out.overrides = appendSortedUnique(out.overrides, source.Kustomize.Namespace)
			}
			break
		}
	}
	out.remoteOnly = inCluster == 0 && remote > 0
	return out
}

func appendSortedUnique(values []string, value string) []string {
	if value == "" || slices.Contains(values, value) {
		return values
	}
	values = append(values, value)
	sort.Strings(values)
	return values
}

// sourceRendersCommittedFile reports whether source renders the committed
// file as it is. The source must render this repository: not an OCI
// artifact, not a chart from a chart repository, and a path in the tree
// (localProvider.sourceResolvesToRepoRoot). A Kustomize source renders the
// file when its graph reads it. A directory source renders a file directly
// in its path, or below it with directory.recurse. A Helm chart renders its
// templates, not a committed file, and a plugin is not known.
func sourceRendersCommittedFile(root string, source argoappv1.ApplicationSource, file string, gates sourceTypeGates) bool {
	sourcePath, ok := committedFileSourcePath(root, source, file)
	if !ok {
		return false
	}
	resolved := render.ResolvedSource{RepoRoot: root, Path: sourcePath}
	if explicitType, err := source.ExplicitType(); err == nil && explicitType != nil {
		resolved.ExplicitType = *explicitType
	}
	if resolved.ExplicitType == argoappv1.ApplicationSourceTypePlugin || source.Plugin != nil {
		return false
	}
	renderer, err := selectLocalRenderer(resolved, gates)
	if err != nil {
		return false
	}
	switch renderer.(type) {
	case render.KustomizeRenderer:
		paths, err := render.KustomizeSelectionPaths(context.Background(), root, sourcePath, source.Kustomize)
		return err == nil && slices.Contains(paths, file)
	case render.DirectoryRenderer:
		if path.Dir(file) == sourcePath || (sourcePath == "." && !strings.Contains(file, "/")) {
			return true
		}
		return source.Directory != nil && source.Directory.Recurse
	}
	return false
}

// committedFileSourcePath returns the clean local path of source when the
// source renders this repository and its path holds file.
func committedFileSourcePath(root string, source argoappv1.ApplicationSource, file string) (string, bool) {
	if source.Path == "" || !(localProvider{repoRoot: root}).sourceResolvesToRepoRoot(settingsInstallResolvedSource(source)) {
		return "", false
	}
	sourcePath, err := cleanLocalSourcePath(source.Path)
	if err != nil {
		return "", false
	}
	sourcePath = filepath.ToSlash(sourcePath)
	if sourcePath != "." && file != sourcePath && !strings.HasPrefix(file, sourcePath+"/") {
		return "", false
	}
	return sourcePath, true
}

// kustomizationNamespaceFor returns the namespace of the kustomization in
// dir when it lists file as a resource.
func kustomizationNamespaceFor(root, dir, file string) string {
	for _, name := range render.KustomizationFileNames {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(dir), name))
		if err != nil {
			continue
		}
		var kustomization struct {
			Namespace string   `json:"namespace"`
			Resources []string `json:"resources"`
		}
		if err := sigsyaml.Unmarshal(data, &kustomization); err != nil {
			return ""
		}
		for _, resource := range kustomization.Resources {
			if path.Clean(resource) == file {
				return kustomization.Namespace
			}
		}
		return ""
	}
	return ""
}
