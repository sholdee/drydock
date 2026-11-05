package project

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/chart"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/remote"
)

const (
	projectDiagnosticCategory    = "project"
	repositoryDiagnosticCategory = "repository"
)

func ValidateApplications(apps []argoappv1.Application, projects []argoappv1.AppProject, settings config.ArgoSettings) []diagnostic.Diagnostic {
	if len(apps) == 0 {
		return nil
	}

	index := ByName(projects)
	hasLocalProjects := len(projects) > 0
	diags := make([]diagnostic.Diagnostic, 0)
	for _, app := range apps {
		projectName := applicationProject(app)
		proj, ok := index[projectName]
		if !ok {
			switch {
			case projectName == argoappv1.DefaultAppProjectName:
				proj = implicitDefaultProject()
			case !hasLocalProjects:
				// The repository declares no AppProject, so the one this
				// Application names lives only on the cluster. Validate
				// against the permissive implicit default so settings-derived
				// checks still apply, and record that in a deferred
				// diagnostic rather than reporting denials in the name of a
				// project the Application never referenced.
				proj = implicitDefaultProject()
				diags = append(diags, unresolvedProjectWarning(app, projectName))
			default:
				diags = append(diags, projectWarning(app, fmt.Sprintf("Application %s references missing AppProject %q", applicationName(app), projectName)))
				continue
			}
		}
		proj = effectiveProject(proj, settings)

		diags = append(diags, validateSources(app, proj)...)
		diags = append(diags, validateDestination(app, proj, settings)...)
		diags = append(diags, validateSourceNamespace(app, proj)...)
		diags = append(diags, rbacMetadataDiagnostics(proj)...)
		if hasLocalProjects {
			diags = append(diags, repositoryMetadataDiagnostics(app, settings)...)
		}
	}

	return dedupeDiagnostics(diags)
}

// ByName indexes projects the way validation looks an Application's
// AppProject up: by name alone, the last one winning, skipping any without a
// name.
func ByName(projects []argoappv1.AppProject) map[string]argoappv1.AppProject {
	index := make(map[string]argoappv1.AppProject, len(projects))
	for _, proj := range projects {
		if proj.Name == "" {
			continue
		}
		index[proj.Name] = proj
	}
	return index
}

// effectiveProject mirrors Argo CD util/argo GetAppProjectByName: every
// repository Secret scoped to the project adds its URL to sourceRepos, and
// every cluster Secret scoped to it adds an implicit destination for its
// server, in every namespace or in each namespace the Secret lists. Entries
// are appended to copies of the project's lists, in a deterministic order.
// argocd-cm globalProjects (GetAppVirtualProject) are not merged.
func effectiveProject(proj argoappv1.AppProject, settings config.ArgoSettings) argoappv1.AppProject {
	repos := make([]string, 0)
	for key, repo := range settings.HelmRepositories {
		if repo.Project != proj.Name {
			continue
		}
		if repoURL := projectSourceRepo(key, repo); repoURL != "" {
			repos = append(repos, repoURL)
		}
	}
	sort.Strings(repos)
	proj.Spec.SourceRepos = slices.Concat(proj.Spec.SourceRepos, repos)
	proj.Spec.Destinations = slices.Concat(proj.Spec.Destinations, projectScopedClusterDestinations(proj.Name, settings))
	return proj
}

// projectScopedClusterDestinations returns the destinations Argo CD adds to
// a project for its project-scoped cluster Secrets, ordered by server.
func projectScopedClusterDestinations(projectName string, settings config.ArgoSettings) []argoappv1.ApplicationDestination {
	clusters, _ := projectScopedClusters(projectName, settings)
	destinations := make([]argoappv1.ApplicationDestination, 0, len(clusters))
	for _, cluster := range clusters {
		if len(cluster.Namespaces) == 0 {
			destinations = append(destinations, argoappv1.ApplicationDestination{Server: cluster.Server, Namespace: "*"})
			continue
		}
		for _, namespace := range cluster.Namespaces {
			destinations = append(destinations, argoappv1.ApplicationDestination{Server: cluster.Server, Namespace: namespace})
		}
	}
	return destinations
}

func applicationProject(app argoappv1.Application) string {
	return app.Spec.GetProject()
}

// implicitDefaultProject stands in for an AppProject drydock cannot see: the
// auto-created default project, or whatever project an Application names when
// the repository declares none. It is permissive on every axis, source
// namespaces included, because the real project's spec is unknown offline and
// a stand-in must never deny what the cluster might permit.
func implicitDefaultProject() argoappv1.AppProject {
	return argoappv1.AppProject{
		Name: argoappv1.DefaultAppProjectName,
		Spec: argoappv1.AppProjectSpec{
			SourceRepos:      []string{"*"},
			SourceNamespaces: []string{"*"},
			Destinations: []argoappv1.ApplicationDestination{{
				Name:      "*",
				Server:    "*",
				Namespace: "*",
			}},
			ClusterResourceWhitelist: []argoappv1.ClusterResourceRestrictionItem{{
				Group: "*",
				Kind:  "*",
			}},
		},
	}
}

func validateSources(app argoappv1.Application, proj argoappv1.AppProject) []diagnostic.Diagnostic {
	var diags []diagnostic.Diagnostic
	for _, source := range app.Spec.GetSources() {
		if proj.IsSourcePermitted(source) {
			continue
		}
		diags = append(diags, projectWarning(app, fmt.Sprintf("Application %s source repository %q is not permitted by AppProject %q", applicationName(app), displayRepoURL(source.RepoURL), proj.Name)))
	}
	return diags
}

func validateDestination(app argoappv1.Application, proj argoappv1.AppProject, settings config.ArgoSettings) []diagnostic.Diagnostic {
	diags := make([]diagnostic.Diagnostic, 0, 2)
	dest := app.Spec.Destination
	if destinationInClusterDisabled(dest, settings) {
		return append(diags, inClusterDisabledWarning(app))
	}
	destCluster, resolution := destinationCluster(dest, settings)
	projectClusters, projectClusterMetadataAvailable := projectScopedClusters(proj.Name, settings)
	projectScopedCheckAvailable := projectClusterMetadataAvailable || resolution == destinationFromClusterSecret
	if proj.Spec.PermitOnlyProjectScopedClusters && !projectScopedCheckAvailable {
		diags = append(diags, projectWarning(app, fmt.Sprintf("AppProject %q enables permitOnlyProjectScopedClusters; project-scoped cluster Secrets enforcement is deferred offline", proj.Name)))
		proj.Spec.PermitOnlyProjectScopedClusters = false
	}
	projectScopedCheckApplied := proj.Spec.PermitOnlyProjectScopedClusters

	if offline, ok := unresolvedOfflineDestination(dest, resolution); ok && !projectScopedCheckApplied {
		switch offlineDestinationPermitted(proj, offline, dest.Namespace) {
		case tristateTrue:
		case tristateFalse:
			diags = append(diags, destinationDeniedWarning(app, proj))
		case tristateUnknown:
			diags = append(diags, projectWarning(app, unresolvedDestinationDeferredMessage(app, offline)))
		}
		return diags
	}

	permitted, err := proj.IsDestinationPermitted(destCluster, dest.Namespace, func(project string) ([]*argoappv1.Cluster, error) {
		if project != proj.Name {
			return nil, nil
		}
		return projectClusters, nil
	})
	if err != nil {
		diags = append(diags, projectWarning(app, fmt.Sprintf("Application %s destination could not be validated against AppProject %q: %v", applicationName(app), proj.Name, err)))
		return diags
	}
	if !permitted {
		diags = append(diags, destinationDeniedWarning(app, proj))
	}
	return diags
}

func destinationDeniedWarning(app argoappv1.Application, proj argoappv1.AppProject) diagnostic.Diagnostic {
	return projectWarning(app, fmt.Sprintf("Application %s destination is not permitted by AppProject %q", applicationName(app), proj.Name))
}

// destinationInClusterDisabled reports whether dest is Argo CD's in-cluster
// address while argocd-cm cluster.inClusterEnabled is "false". util/db
// GetCluster rejects that server as disabled before it looks at any cluster
// Secret, and GetClusterServersByName drops it from name lookups, so Argo CD
// rejects the Application whatever its AppProject permits. A name-only
// destination counts when the repository's cluster Secret with that name is
// at the in-cluster address.
func destinationInClusterDisabled(dest argoappv1.ApplicationDestination, settings config.ArgoSettings) bool {
	if settings.InClusterEnabled.Value {
		return false
	}
	name := strings.TrimSpace(dest.Name)
	server := NormalizeClusterServer(dest.Server)
	switch {
	case name == "" && server != "":
		return server == argoappv1.KubernetesInternalAPIServerAddr
	case server == "" && name != "":
		cluster, ok := clusterSettingsByName(name, settings)
		return ok && NormalizeClusterServer(cluster.Server) == argoappv1.KubernetesInternalAPIServerAddr
	default:
		return false
	}
}

func inClusterDisabledWarning(app argoappv1.Application) diagnostic.Diagnostic {
	return projectWarning(app, fmt.Sprintf("Application %s destination is not permitted: cluster %q is disabled by argocd-cm cluster.inClusterEnabled", applicationName(app), argoappv1.KubernetesInternalAPIServerAddr))
}

// destinationResolution is how drydock resolved an Application destination's
// cluster offline.
type destinationResolution int

const (
	// destinationUnresolved: no discovered cluster Secret describes the
	// destination, so its name or server may be unknown.
	destinationUnresolved destinationResolution = iota
	// destinationFromClusterSecret: a discovered cluster Secret describes it.
	destinationFromClusterSecret
	// destinationLocalCluster: a server-only destination at
	// https://kubernetes.default.svc, or a name-only destination named
	// in-cluster, with no cluster Secret, which Argo CD resolves to its
	// built-in in-cluster cluster unless argocd-cm cluster.inClusterEnabled
	// is "false" (util/db GetCluster and GetClusterServersByName).
	destinationLocalCluster
)

func destinationCluster(dest argoappv1.ApplicationDestination, settings config.ArgoSettings) (*argoappv1.Cluster, destinationResolution) {
	name := strings.TrimSpace(dest.Name)
	server := NormalizeClusterServer(dest.Server)
	if server != "" {
		if cluster, ok := settings.Clusters[server]; ok {
			return argoClusterFromSettings(cluster, name), destinationFromClusterSecret
		}
		if name == "" && server == argoappv1.KubernetesInternalAPIServerAddr && settings.InClusterEnabled.Value {
			return &argoappv1.Cluster{Name: argoappv1.KubernetesInClusterName, Server: server}, destinationLocalCluster
		}
		return &argoappv1.Cluster{Name: name, Server: server}, destinationUnresolved
	}
	if name != "" {
		if cluster, ok := clusterSettingsByName(name, settings); ok {
			return argoClusterFromSettings(cluster, name), destinationFromClusterSecret
		}
		// util/db GetClusterServersByName: with no cluster Secret named
		// in-cluster, the name is the in-cluster address, which GetCluster
		// then resolves through a Secret at that address or to the built-in
		// cluster.
		if name == argoappv1.KubernetesInClusterName && settings.InClusterEnabled.Value && !clusterNameDeclared(name, settings) {
			if cluster, ok := settings.Clusters[argoappv1.KubernetesInternalAPIServerAddr]; ok {
				return argoClusterFromSettings(cluster, ""), destinationFromClusterSecret
			}
			return &argoappv1.Cluster{Name: argoappv1.KubernetesInClusterName, Server: argoappv1.KubernetesInternalAPIServerAddr}, destinationLocalCluster
		}
	}
	return &argoappv1.Cluster{Name: name, Server: server}, destinationUnresolved
}

// clusterNameDeclared reports whether any discovered cluster Secret has name.
func clusterNameDeclared(name string, settings config.ArgoSettings) bool {
	for _, cluster := range settings.Clusters {
		if clusterDisplayName(cluster) == name {
			return true
		}
	}
	return false
}

func clusterSettingsByName(name string, settings config.ArgoSettings) (config.ClusterSettings, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return config.ClusterSettings{}, false
	}
	var matched config.ClusterSettings
	found := false
	for _, cluster := range settings.Clusters {
		if clusterDisplayName(cluster) != name {
			continue
		}
		if found {
			return config.ClusterSettings{}, false
		}
		matched = cluster
		found = true
	}
	return matched, found
}

func projectScopedClusters(projectName string, settings config.ArgoSettings) ([]*argoappv1.Cluster, bool) {
	servers := make([]string, 0, len(settings.Clusters))
	for server := range settings.Clusters {
		servers = append(servers, server)
	}
	sort.Strings(servers)

	clusters := make([]*argoappv1.Cluster, 0)
	for _, server := range servers {
		cluster := settings.Clusters[server]
		if cluster.Project != projectName {
			continue
		}
		clusters = append(clusters, argoClusterFromSettings(cluster, ""))
	}
	return clusters, len(clusters) > 0
}

func argoClusterFromSettings(cluster config.ClusterSettings, preferredName string) *argoappv1.Cluster {
	name := strings.TrimSpace(preferredName)
	if name == "" {
		name = clusterDisplayName(cluster)
	}
	return &argoappv1.Cluster{
		Name:             name,
		Server:           cluster.Server,
		Namespaces:       append([]string(nil), cluster.Namespaces...),
		ClusterResources: cluster.ClusterResources,
		Project:          cluster.Project,
	}
}

func clusterDisplayName(cluster config.ClusterSettings) string {
	if strings.TrimSpace(cluster.Name) != "" {
		return strings.TrimSpace(cluster.Name)
	}
	return cluster.Server
}

// NormalizeClusterServer normalizes a cluster server the way validation
// looks a destination server up among the cluster Secrets.
func NormalizeClusterServer(raw string) string {
	return strings.TrimRight(strings.TrimSpace(raw), "/")
}

func validateSourceNamespace(app argoappv1.Application, proj argoappv1.AppProject) []diagnostic.Diagnostic {
	if proj.IsAppNamespacePermitted(&app, controllerNamespace(proj)) {
		return nil
	}
	return []diagnostic.Diagnostic{projectWarning(app, fmt.Sprintf("Application %s source namespace %q is not permitted by AppProject %q", applicationName(app), app.Namespace, proj.Name))}
}

func controllerNamespace(proj argoappv1.AppProject) string {
	if proj.Namespace != "" {
		return proj.Namespace
	}
	return "argocd"
}

func rbacMetadataDiagnostics(proj argoappv1.AppProject) []diagnostic.Diagnostic {
	if len(proj.Spec.Roles) == 0 {
		return nil
	}
	return []diagnostic.Diagnostic{{
		Severity: diagnostic.SeverityWarning,
		Category: projectDiagnosticCategory,
		Message:  fmt.Sprintf("AppProject %q defines RBAC roles; offline validation reports role presence but does not simulate authorization", proj.Name),
	}}
}

func repositoryMetadataDiagnostics(app argoappv1.Application, settings config.ArgoSettings) []diagnostic.Diagnostic {
	diags := make([]diagnostic.Diagnostic, 0)
	projectName := applicationProject(app)
	for _, source := range app.Spec.GetSources() {
		repoURL := strings.TrimSpace(source.RepoURL)
		if repoURL == "" {
			continue
		}
		repo, ok := repositorySettingsForURL(repoURL, settings)
		if !ok {
			continue
		}
		if repo.Project != "" && repo.Project != projectName {
			diags = append(diags, repositoryWarning(app, fmt.Sprintf("Application %s repository metadata for %q is scoped to project %q, not AppProject %q", applicationName(app), displayRepoURL(repoURL), repo.Project, projectName)))
		}
	}
	return diags
}

func repositorySettingsForURL(repoURL string, settings config.ArgoSettings) (config.RepositorySettings, bool) {
	if repo, ok := settings.HelmRepositories[repoURL]; ok {
		return repo, true
	}
	for key, repo := range settings.HelmRepositories {
		if NewRepositoryMatch(key, repo).matchesURL(repoURL) {
			return repo, true
		}
	}
	return config.RepositorySettings{}, false
}

func normalizeGitURL(raw string) (string, bool) {
	normalized, err := remote.NormalizeGitRepoURL(raw)
	if err != nil {
		return "", false
	}
	return normalized, true
}

func normalizeOCIURL(raw string) (string, bool) {
	normalized, err := chart.NormalizeRepository(raw, chart.RepositoryOCI)
	if err != nil {
		return "", false
	}
	return normalized, true
}

func displayRepoURL(raw string) string {
	if redacted := remote.RedactGitRepoURL(raw); redacted != "[invalid-url]" {
		return redacted
	}
	return redactOCIRepoURL(raw)
}

func redactOCIRepoURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	normalized, err := chart.NormalizeRepository(trimmed, chart.RepositoryOCI)
	if err != nil {
		return "[invalid-url]"
	}
	redacted := remote.RedactURL(normalized)
	if redacted == "[invalid-url]" {
		return redacted
	}
	if !strings.Contains(trimmed, "://") {
		return strings.TrimPrefix(redacted, "oci://")
	}
	return redacted
}

func projectWarning(app argoappv1.Application, message string) diagnostic.Diagnostic {
	return warning(projectDiagnosticCategory, message, app)
}

// unresolvedProjectWarning reports an Application whose AppProject is not in
// the repository while the repository declares no AppProject at all. It is a
// deferred diagnostic: nothing is wrong with the repository, drydock simply
// cannot see the project, so actionable mode hides it.
func unresolvedProjectWarning(app argoappv1.Application, projectName string) diagnostic.Diagnostic {
	diag := projectWarning(app, fmt.Sprintf("Application %s references AppProject %q, which is not declared in the repository; validating against the implicit default project", applicationName(app), projectName))
	diag.Code = diagnostic.CodeProjectUnresolved
	return diag
}

func repositoryWarning(app argoappv1.Application, message string) diagnostic.Diagnostic {
	return warning(repositoryDiagnosticCategory, message, app)
}

func warning(category, message string, app argoappv1.Application) diagnostic.Diagnostic {
	return diagnostic.Diagnostic{
		Severity: diagnostic.SeverityWarning,
		Category: category,
		Message:  message,
		Provenance: diagnostic.Provenance{
			Path: applicationName(app),
		},
	}
}

func applicationName(app argoappv1.Application) string {
	if app.Namespace == "" {
		return app.Name
	}
	return app.Namespace + "/" + app.Name
}

func dedupeDiagnostics(diags []diagnostic.Diagnostic) []diagnostic.Diagnostic {
	if len(diags) == 0 {
		return nil
	}
	sort.SliceStable(diags, func(i, j int) bool {
		if diags[i].Category != diags[j].Category {
			return diags[i].Category < diags[j].Category
		}
		return diags[i].Message < diags[j].Message
	})

	out := make([]diagnostic.Diagnostic, 0, len(diags))
	seen := make(map[string]struct{}, len(diags))
	for _, diag := range diags {
		key := diag.Category + "\x00" + diag.Message
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, diag)
	}
	return out
}
