package project

import (
	"fmt"
	"sort"
	"strings"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	argoglob "github.com/argoproj/argo-cd/v3/util/glob"
	"github.com/sholdee/drydock/internal/chart"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/remote"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	projectDiagnosticCategory    = "project"
	repositoryDiagnosticCategory = "repository"
)

func ValidateApplications(apps []argoappv1.Application, projects []argoappv1.AppProject, settings config.ArgoSettings) []diagnostic.Diagnostic {
	if len(apps) == 0 {
		return nil
	}

	index := projectIndex(projects)
	hasLocalProjects := len(projects) > 0
	diags := make([]diagnostic.Diagnostic, 0)
	for _, app := range apps {
		projectName := applicationProject(app)
		proj, ok := index[projectName]
		if !ok {
			if projectName == argoappv1.DefaultAppProjectName || !hasLocalProjects {
				proj = implicitDefaultProject()
			} else {
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

func projectIndex(projects []argoappv1.AppProject) map[string]argoappv1.AppProject {
	index := make(map[string]argoappv1.AppProject, len(projects))
	for _, proj := range projects {
		if proj.Name == "" {
			continue
		}
		index[proj.Name] = proj
	}
	return index
}

func effectiveProject(proj argoappv1.AppProject, settings config.ArgoSettings) argoappv1.AppProject {
	repos := make([]string, 0)
	for key, repo := range settings.HelmRepositories {
		if repo.Project != proj.Name {
			continue
		}
		repoURL := strings.TrimSpace(repo.URL)
		if repoURL == "" {
			repoURL = strings.TrimSpace(key)
		}
		if repoURL == "" {
			continue
		}
		repos = append(repos, repoURL)
	}
	sort.Strings(repos)
	proj.Spec.SourceRepos = append(proj.Spec.SourceRepos, repos...)
	return proj
}

func applicationProject(app argoappv1.Application) string {
	return app.Spec.GetProject()
}

func implicitDefaultProject() argoappv1.AppProject {
	return argoappv1.AppProject{
		ObjectMeta: metav1.ObjectMeta{Name: argoappv1.DefaultAppProjectName},
		Spec: argoappv1.AppProjectSpec{
			SourceRepos: []string{"*"},
			Destinations: []argoappv1.ApplicationDestination{{
				Name:      "*",
				Server:    "*",
				Namespace: "*",
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
	destCluster, destinationClusterKnown := destinationCluster(dest, settings)
	projectClusters, projectClusterMetadataAvailable := projectScopedClusters(proj.Name, settings)
	projectScopedCheckAvailable := projectClusterMetadataAvailable || destinationClusterKnown
	if proj.Spec.PermitOnlyProjectScopedClusters && !projectScopedCheckAvailable {
		diags = append(diags, projectWarning(app, fmt.Sprintf("AppProject %q enables permitOnlyProjectScopedClusters; project-scoped cluster Secrets enforcement is deferred offline", proj.Name)))
		proj.Spec.PermitOnlyProjectScopedClusters = false
	}
	projectScopedCheckApplied := proj.Spec.PermitOnlyProjectScopedClusters

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
		if projectScopedCheckApplied {
			diags = append(diags, projectWarning(app, fmt.Sprintf("Application %s destination is not permitted by AppProject %q", applicationName(app), proj.Name)))
			return diags
		}
		if nameOnlyDestinationExplicitlyDenied(dest, proj) {
			diags = append(diags, projectWarning(app, fmt.Sprintf("Application %s destination is not permitted by AppProject %q", applicationName(app), proj.Name)))
			return diags
		}
		if nameOnlyDestinationHasServerDenyPolicy(dest, proj) {
			diags = append(diags, projectWarning(app, fmt.Sprintf("Application %s destination name %q cannot be resolved against AppProject server policy offline", applicationName(app), dest.Name)))
			return diags
		}
		if nameOnlyDestinationHasServerScopedNamespaceDenyPolicy(dest, proj) {
			diags = append(diags, projectWarning(app, fmt.Sprintf("Application %s destination name %q cannot be resolved against AppProject server policy offline", applicationName(app), dest.Name)))
			return diags
		}
		if nameOnlyDestinationPermittedByWildcardServer(dest, proj) {
			return diags
		}
		if nameOnlyDestinationHasServerSpecificPolicy(dest, proj) {
			diags = append(diags, projectWarning(app, fmt.Sprintf("Application %s destination name %q cannot be resolved against AppProject server policy offline", applicationName(app), dest.Name)))
			return diags
		}
		diags = append(diags, projectWarning(app, fmt.Sprintf("Application %s destination is not permitted by AppProject %q", applicationName(app), proj.Name)))
	}
	return diags
}

func destinationCluster(dest argoappv1.ApplicationDestination, settings config.ArgoSettings) (*argoappv1.Cluster, bool) {
	name := strings.TrimSpace(dest.Name)
	server := normalizeClusterServer(strings.TrimSpace(dest.Server))
	if server != "" {
		if cluster, ok := settings.Clusters[server]; ok {
			return argoClusterFromSettings(cluster, name), true
		}
		return &argoappv1.Cluster{Name: name, Server: server}, false
	}
	if name != "" {
		if cluster, ok := clusterSettingsByName(name, settings); ok {
			return argoClusterFromSettings(cluster, name), true
		}
	}
	return &argoappv1.Cluster{Name: name, Server: server}, false
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

func normalizeClusterServer(raw string) string {
	return strings.TrimRight(strings.TrimSpace(raw), "/")
}

func nameOnlyDestinationExplicitlyDenied(dest argoappv1.ApplicationDestination, proj argoappv1.AppProject) bool {
	if !isNameOnlyDestination(dest) {
		return false
	}
	for _, allowed := range proj.Spec.Destinations {
		if destinationNameDenied(allowed.Name, dest.Name) {
			return true
		}
		if strings.TrimSpace(allowed.Server) == "*" && destinationNamespaceDenied(allowed.Namespace, dest.Namespace) {
			return true
		}
	}
	return false
}

func nameOnlyDestinationHasServerDenyPolicy(dest argoappv1.ApplicationDestination, proj argoappv1.AppProject) bool {
	if !isNameOnlyDestination(dest) {
		return false
	}
	for _, allowed := range proj.Spec.Destinations {
		server := strings.TrimSpace(allowed.Server)
		if !destinationDenyPattern(server) {
			continue
		}
		if destinationPatternMatch(allowed.Namespace, dest.Namespace) {
			return true
		}
	}
	return false
}

func nameOnlyDestinationHasServerScopedNamespaceDenyPolicy(dest argoappv1.ApplicationDestination, proj argoappv1.AppProject) bool {
	if !isNameOnlyDestination(dest) {
		return false
	}
	for _, allowed := range proj.Spec.Destinations {
		server := strings.TrimSpace(allowed.Server)
		if server == "" || server == "*" {
			continue
		}
		if destinationNamespaceDenied(allowed.Namespace, dest.Namespace) {
			return true
		}
	}
	return false
}

func nameOnlyDestinationPermittedByWildcardServer(dest argoappv1.ApplicationDestination, proj argoappv1.AppProject) bool {
	if !isNameOnlyDestination(dest) {
		return false
	}
	for _, allowed := range proj.Spec.Destinations {
		if strings.TrimSpace(allowed.Server) != "*" {
			continue
		}
		if destinationNamespaceAllowed(allowed.Namespace, dest.Namespace) {
			return true
		}
	}
	return false
}

func nameOnlyDestinationHasServerSpecificPolicy(dest argoappv1.ApplicationDestination, proj argoappv1.AppProject) bool {
	if !isNameOnlyDestination(dest) {
		return false
	}
	for _, allowed := range proj.Spec.Destinations {
		server := strings.TrimSpace(allowed.Server)
		if server == "" || server == "*" || destinationDenyPattern(server) {
			continue
		}
		if destinationNamespaceAllowed(allowed.Namespace, dest.Namespace) {
			return true
		}
	}
	return false
}

func isNameOnlyDestination(dest argoappv1.ApplicationDestination) bool {
	return strings.TrimSpace(dest.Name) != "" && strings.TrimSpace(dest.Server) == ""
}

func destinationNameDenied(pattern, name string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" || !destinationDenyPattern(pattern) {
		return false
	}
	return destinationGlobMatch(strings.TrimPrefix(pattern, "!"), name)
}

func destinationNamespaceDenied(pattern, namespace string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" || !destinationDenyPattern(pattern) {
		return false
	}
	return destinationGlobMatch(strings.TrimPrefix(pattern, "!"), namespace)
}

func destinationNamespaceAllowed(pattern, namespace string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" || destinationDenyPattern(pattern) {
		return false
	}
	return destinationGlobMatch(pattern, namespace)
}

func destinationDenyPattern(pattern string) bool {
	return strings.HasPrefix(pattern, "!")
}

func destinationGlobMatch(pattern, value string) bool {
	if pattern == "*" {
		return true
	}
	return argoglob.Match(pattern, value)
}

func destinationPatternMatch(pattern, value string) bool {
	pattern = strings.TrimSpace(pattern)
	if destinationDenyPattern(pattern) {
		return !destinationGlobMatch(strings.TrimPrefix(pattern, "!"), value)
	}
	return destinationGlobMatch(pattern, value)
}

func validateSourceNamespace(app argoappv1.Application, proj argoappv1.AppProject) []diagnostic.Diagnostic {
	if len(proj.Spec.SourceNamespaces) == 0 || proj.IsAppNamespacePermitted(&app, controllerNamespace(proj)) {
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

//nolint:gocyclo // Repository matching intentionally covers Argo CD's URL normalization variants in one place.
func repositorySettingsForURL(repoURL string, settings config.ArgoSettings) (config.RepositorySettings, bool) {
	if repo, ok := settings.HelmRepositories[repoURL]; ok {
		return repo, true
	}
	normalizedRepoURL, repoURLNormalized := normalizeGitURL(repoURL)
	normalizedOCIRepoURL, repoURLOCI := normalizeOCIURL(repoURL)
	for key, repo := range settings.HelmRepositories {
		if key == repoURL || repo.URL == repoURL {
			return repo, true
		}
		if repoURLNormalized {
			if normalizedKey, ok := normalizeGitURL(key); ok && normalizedKey == normalizedRepoURL {
				return repo, true
			}
			if normalizedURL, ok := normalizeGitURL(repo.URL); ok && normalizedURL == normalizedRepoURL {
				return repo, true
			}
		}
		if repoURLOCI && repo.EnableOCI {
			if normalizedKey, ok := normalizeOCIURL(key); ok && normalizedKey == normalizedOCIRepoURL {
				return repo, true
			}
			if normalizedURL, ok := normalizeOCIURL(repo.URL); ok && normalizedURL == normalizedOCIRepoURL {
				return repo, true
			}
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
