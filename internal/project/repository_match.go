package project

import (
	"slices"
	"strings"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/config"
)

// RepositoryMatch is one repository entry, normalized once, as validation
// applies it: as the repository metadata of a source URL
// (repositorySettingsForURL) and as the sourceRepos entry it adds to its
// AppProject (effectiveProject).
type RepositoryMatch struct {
	key, url string
	// git and oci hold the entry's key and URL in the normalized forms
	// repository metadata lookups compare; oci only when it enables OCI.
	git, oci []string
	project  string
	// sourceRepo is the sourceRepos entry effectiveProject adds to project.
	sourceRepo string
}

// NewRepositoryMatch normalizes the repository entry key and repo.
func NewRepositoryMatch(key string, repo config.RepositorySettings) RepositoryMatch {
	match := RepositoryMatch{key: key, url: repo.URL, project: repo.Project, sourceRepo: projectSourceRepo(key, repo)}
	for _, raw := range []string{key, repo.URL} {
		if normalized, ok := normalizeGitURL(raw); ok {
			match.git = append(match.git, normalized)
		}
		if !repo.EnableOCI {
			continue
		}
		if normalized, ok := normalizeOCIURL(raw); ok {
			match.oci = append(match.oci, normalized)
		}
	}
	return match
}

// projectSourceRepo is the sourceRepos entry a project-scoped repository adds
// to its AppProject: its URL, or its key when the URL is empty.
func projectSourceRepo(key string, repo config.RepositorySettings) string {
	if repoURL := strings.TrimSpace(repo.URL); repoURL != "" {
		return repoURL
	}
	return strings.TrimSpace(key)
}

// matchesURL reports whether the entry is repository metadata for repoURL.
func (m RepositoryMatch) matchesURL(repoURL string) bool {
	if m.key == repoURL || m.url == repoURL {
		return true
	}
	if normalized, ok := normalizeGitURL(repoURL); ok && slices.Contains(m.git, normalized) {
		return true
	}
	if normalized, ok := normalizeOCIURL(repoURL); ok && slices.Contains(m.oci, normalized) {
		return true
	}
	return false
}

// Reaches reports whether validation applies the entry to app: as the
// repository metadata of one of its sources, or, when app is in the entry's
// project, through the sourceRepos entry it adds there, which Argo CD matches
// as a normalized glob. A deny pattern (!url) there reaches every source: it
// denies the sources it names and, through Argo CD's negation, permits the
// rest (AppProject.IsSourcePermitted). One scoped to the default project
// reaches every Application: with no AppProject declared, each validates
// against the implicit default project, whatever project it names.
func (m RepositoryMatch) Reaches(app argoappv1.Application) bool {
	sources := app.Spec.GetSources()
	for _, source := range sources {
		if repoURL := strings.TrimSpace(source.RepoURL); repoURL != "" && m.matchesURL(repoURL) {
			return true
		}
	}
	if m.project == "" || m.sourceRepo == "" {
		return false
	}
	if strings.HasPrefix(m.sourceRepo, "!") {
		return m.project == argoappv1.DefaultAppProjectName || applicationProject(app) == m.project
	}
	if applicationProject(app) != m.project {
		return false
	}
	proj := argoappv1.AppProject{Spec: argoappv1.AppProjectSpec{SourceRepos: []string{m.sourceRepo}}}
	return slices.ContainsFunc(sources, proj.IsSourcePermitted)
}
