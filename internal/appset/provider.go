package appset

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/sholdee/drydock/internal/diagnostic"
	"go.yaml.in/yaml/v4"
)

type ProviderOptions struct {
	FixturePaths []string
	Data         ProviderData
}

type ProviderData struct {
	Clusters         []ClusterInput
	ClusterDecisions []ClusterDecisionInput
	SCMRepositories  []SCMRepositoryInput
	PullRequests     []PullRequestInput
	Plugins          []PluginInput
}

func (options ProviderOptions) Supplied() bool {
	return len(options.FixturePaths) > 0 || options.Data.Supplied()
}

func (data ProviderData) Supplied() bool {
	return len(data.Clusters) > 0 ||
		len(data.ClusterDecisions) > 0 ||
		len(data.SCMRepositories) > 0 ||
		len(data.PullRequests) > 0 ||
		len(data.Plugins) > 0
}

type ClusterInput struct {
	Name        string            `json:"name" yaml:"name"`
	Server      string            `json:"server" yaml:"server"`
	Project     string            `json:"project" yaml:"project"`
	Labels      map[string]string `json:"labels" yaml:"labels"`
	Annotations map[string]string `json:"annotations" yaml:"annotations"`
	Values      map[string]string `json:"values" yaml:"values"`
	FixturePath string            `json:"-" yaml:"-"`
}

type ClusterDecisionInput struct {
	ConfigMapRef  string            `json:"configMapRef" yaml:"configMapRef"`
	ResourceName  string            `json:"resourceName" yaml:"resourceName"`
	Labels        map[string]string `json:"labels" yaml:"labels"`
	MatchKey      string            `json:"matchKey" yaml:"matchKey"`
	StatusListKey string            `json:"statusListKey" yaml:"statusListKey"`
	Decisions     []map[string]any  `json:"decisions" yaml:"decisions"`
	Values        map[string]string `json:"values" yaml:"values"`
	FixturePath   string            `json:"-" yaml:"-"`
}

type SCMRepositoryInput struct {
	Provider     string            `json:"provider" yaml:"provider"`
	Organization string            `json:"organization" yaml:"organization"`
	Project      string            `json:"project" yaml:"project"`
	Region       string            `json:"region" yaml:"region"`
	Repository   string            `json:"repository" yaml:"repository"`
	RepositoryID string            `json:"repositoryID" yaml:"repositoryID"`
	Branch       string            `json:"branch" yaml:"branch"`
	SHA          string            `json:"sha" yaml:"sha"`
	URL          string            `json:"url" yaml:"url"`
	Tags         map[string]string `json:"tags" yaml:"tags"`
	Labels       []string          `json:"labels" yaml:"labels"`
	Paths        []string          `json:"paths" yaml:"paths"`
	Values       map[string]string `json:"values" yaml:"values"`
	FixturePath  string            `json:"-" yaml:"-"`
}

type PullRequestInput struct {
	Provider     string            `json:"provider" yaml:"provider"`
	Organization string            `json:"organization" yaml:"organization"`
	Project      string            `json:"project" yaml:"project"`
	Repository   string            `json:"repository" yaml:"repository"`
	Number       int               `json:"number" yaml:"number"`
	Title        string            `json:"title" yaml:"title"`
	Branch       string            `json:"branch" yaml:"branch"`
	TargetBranch string            `json:"targetBranch" yaml:"targetBranch"`
	HeadSHA      string            `json:"headSHA" yaml:"headSHA"`
	Author       string            `json:"author" yaml:"author"`
	State        string            `json:"state" yaml:"state"`
	Labels       []string          `json:"labels" yaml:"labels"`
	Values       map[string]string `json:"values" yaml:"values"`
	FixturePath  string            `json:"-" yaml:"-"`
}

type PluginInput struct {
	ConfigMapRef string            `json:"configMapRef" yaml:"configMapRef"`
	Outputs      []any             `json:"outputs" yaml:"outputs"`
	Values       map[string]string `json:"values" yaml:"values"`
	FixturePath  string            `json:"-" yaml:"-"`
}

type providerFixtureFile struct {
	Clusters         []ClusterInput         `json:"clusters" yaml:"clusters"`
	ClusterDecisions []ClusterDecisionInput `json:"clusterDecisions" yaml:"clusterDecisions"`
	SCMRepositories  []SCMRepositoryInput   `json:"scmRepositories" yaml:"scmRepositories"`
	PullRequests     []PullRequestInput     `json:"pullRequests" yaml:"pullRequests"`
	Plugins          []PluginInput          `json:"plugins" yaml:"plugins"`
}

func LoadProviderFixtures(paths []string) (ProviderData, []diagnostic.Diagnostic, error) {
	if len(paths) == 0 {
		return ProviderData{}, nil, nil
	}

	var values []ProviderData
	for _, path := range paths {
		if isURLLikeProviderFixturePath(path) {
			diags := []diagnostic.Diagnostic{providerFixtureInvalidDiagnostic(path, fmt.Sprintf("provider fixture invalid: URL-like fixture path %q is not supported", path))}
			return ProviderData{}, diags, errors.New("provider fixture path must be local")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			diags := []diagnostic.Diagnostic{providerFixtureInvalidDiagnostic(path, fmt.Sprintf("provider fixture invalid: read %s: %v", path, err))}
			return ProviderData{}, diags, fmt.Errorf("read provider fixture %s: %w", path, err)
		}
		fixture, err := decodeProviderFixture(path, data)
		if err != nil {
			diags := []diagnostic.Diagnostic{providerFixtureInvalidDiagnostic(path, fmt.Sprintf("provider fixture invalid: decode %s: %v", path, err))}
			return ProviderData{}, diags, fmt.Errorf("decode provider fixture %s: %w", path, err)
		}
		values = append(values, providerDataFromFixture(fixture, path))
	}
	return MergeProviderData(values...)
}

func MergeProviderData(values ...ProviderData) (ProviderData, []diagnostic.Diagnostic, error) {
	var merged ProviderData
	for _, value := range values {
		merged.Clusters = append(merged.Clusters, value.Clusters...)
		merged.ClusterDecisions = append(merged.ClusterDecisions, value.ClusterDecisions...)
		merged.SCMRepositories = append(merged.SCMRepositories, value.SCMRepositories...)
		merged.PullRequests = append(merged.PullRequests, value.PullRequests...)
		merged.Plugins = append(merged.Plugins, value.Plugins...)
	}
	if diags := duplicateProviderFixtureDiagnostics(merged); len(diags) != 0 {
		return ProviderData{}, diags, errors.New("duplicate provider fixture identities")
	}
	sortProviderData(&merged)
	return merged, nil, nil
}

func decodeProviderFixture(path string, data []byte) (providerFixtureFile, error) {
	var fixture providerFixtureFile
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&fixture); err != nil {
			return providerFixtureFile{}, err
		}
		var extra any
		if err := decoder.Decode(&extra); err == nil {
			return providerFixtureFile{}, errors.New("multiple JSON values are not supported")
		} else if !errors.Is(err, io.EOF) {
			return providerFixtureFile{}, err
		}
	case ".yaml", ".yml":
		decoder := yaml.NewDecoder(bytes.NewReader(data))
		decoder.KnownFields(true)
		if err := decoder.Decode(&fixture); err != nil {
			return providerFixtureFile{}, err
		}
		var extra providerFixtureFile
		if err := decoder.Decode(&extra); err == nil {
			return providerFixtureFile{}, errors.New("multiple YAML documents are not supported")
		} else if !errors.Is(err, io.EOF) {
			return providerFixtureFile{}, err
		}
	default:
		return providerFixtureFile{}, fmt.Errorf("provider fixture extension %q is not supported; use .yaml, .yml, or .json", filepath.Ext(path))
	}
	return fixture, nil
}

func providerDataFromFixture(fixture providerFixtureFile, path string) ProviderData {
	data := ProviderData{
		Clusters:         fixture.Clusters,
		ClusterDecisions: fixture.ClusterDecisions,
		SCMRepositories:  fixture.SCMRepositories,
		PullRequests:     fixture.PullRequests,
		Plugins:          fixture.Plugins,
	}
	for i := range data.Clusters {
		data.Clusters[i].FixturePath = path
	}
	for i := range data.ClusterDecisions {
		data.ClusterDecisions[i].FixturePath = path
	}
	for i := range data.SCMRepositories {
		data.SCMRepositories[i].FixturePath = path
	}
	for i := range data.PullRequests {
		data.PullRequests[i].FixturePath = path
	}
	for i := range data.Plugins {
		data.Plugins[i].FixturePath = path
	}
	return data
}

func duplicateProviderFixtureDiagnostics(data ProviderData) []diagnostic.Diagnostic {
	var diags []diagnostic.Diagnostic
	seen := map[string]string{}
	for _, cluster := range data.Clusters {
		key := clusterIdentity(cluster)
		if first, ok := seen[key]; ok {
			diags = append(diags, duplicateProviderFixtureDiagnostic("cluster", key, first, cluster.FixturePath))
			continue
		}
		seen[key] = cluster.FixturePath
	}
	seen = map[string]string{}
	for _, decision := range data.ClusterDecisions {
		key := clusterDecisionIdentity(decision)
		if first, ok := seen[key]; ok {
			diags = append(diags, duplicateProviderFixtureDiagnostic("cluster decision", key, first, decision.FixturePath))
			continue
		}
		seen[key] = decision.FixturePath
	}
	seen = map[string]string{}
	for _, repo := range data.SCMRepositories {
		key := scmRepositoryIdentity(repo)
		if first, ok := seen[key]; ok {
			diags = append(diags, duplicateProviderFixtureDiagnostic("SCM repository", key, first, repo.FixturePath))
			continue
		}
		seen[key] = repo.FixturePath
	}
	seen = map[string]string{}
	for _, pr := range data.PullRequests {
		key := pullRequestIdentity(pr)
		if first, ok := seen[key]; ok {
			diags = append(diags, duplicateProviderFixtureDiagnostic("pull request", key, first, pr.FixturePath))
			continue
		}
		seen[key] = pr.FixturePath
	}
	seen = map[string]string{}
	for _, plugin := range data.Plugins {
		for _, output := range plugin.Outputs {
			key := pluginOutputIdentity(plugin.ConfigMapRef, output)
			if first, ok := seen[key]; ok {
				diags = append(diags, duplicateProviderFixtureDiagnostic("plugin", key, first, plugin.FixturePath))
				continue
			}
			seen[key] = plugin.FixturePath
		}
	}
	return diags
}

func duplicateProviderFixtureDiagnostic(kind, key, firstPath, duplicatePath string) diagnostic.Diagnostic {
	message := fmt.Sprintf("provider fixture invalid: duplicate provider fixture %s identity %q", kind, key)
	if firstPath != "" {
		message += fmt.Sprintf(" already defined in %s", firstPath)
	}
	return providerFixtureInvalidDiagnostic(duplicatePath, message)
}

func sortProviderData(data *ProviderData) {
	sort.SliceStable(data.Clusters, func(i, j int) bool {
		return clusterIdentity(data.Clusters[i]) < clusterIdentity(data.Clusters[j])
	})
	sort.SliceStable(data.ClusterDecisions, func(i, j int) bool {
		return clusterDecisionIdentity(data.ClusterDecisions[i]) < clusterDecisionIdentity(data.ClusterDecisions[j])
	})
	sort.SliceStable(data.SCMRepositories, func(i, j int) bool {
		return scmRepositoryIdentity(data.SCMRepositories[i]) < scmRepositoryIdentity(data.SCMRepositories[j])
	})
	sort.SliceStable(data.PullRequests, func(i, j int) bool {
		return lessPullRequest(data.PullRequests[i], data.PullRequests[j])
	})
	sort.SliceStable(data.Plugins, func(i, j int) bool {
		return pluginIdentity(data.Plugins[i]) < pluginIdentity(data.Plugins[j])
	})
}

func clusterIdentity(cluster ClusterInput) string {
	return identity(cluster.Name, cluster.Server)
}

func clusterDecisionIdentity(decision ClusterDecisionInput) string {
	return identity(decision.ConfigMapRef, decision.ResourceName, canonicalJSON(decision.Decisions))
}

func scmRepositoryIdentity(repo SCMRepositoryInput) string {
	return identity(repo.Provider, repo.Organization, repo.Project, repo.Region, repo.Repository, repo.Branch, repo.URL)
}

func pullRequestIdentity(pr PullRequestInput) string {
	return identity(pr.Provider, pr.Organization, pr.Project, pr.Repository, strconv.Itoa(pr.Number))
}

func lessPullRequest(left, right PullRequestInput) bool {
	leftPrefix := identity(left.Provider, left.Organization, left.Project, left.Repository)
	rightPrefix := identity(right.Provider, right.Organization, right.Project, right.Repository)
	if leftPrefix != rightPrefix {
		return leftPrefix < rightPrefix
	}
	return left.Number < right.Number
}

func pluginIdentity(plugin PluginInput) string {
	return identity(plugin.ConfigMapRef, canonicalJSON(plugin.Outputs))
}

func pluginOutputIdentity(configMapRef string, output any) string {
	return identity(configMapRef, canonicalJSON(output))
}

func identity(parts ...string) string {
	return strings.Join(parts, "\x00")
}

func canonicalJSON(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%#v", value)
	}
	return string(data)
}

func providerFixtureInvalidDiagnostic(path, message string) diagnostic.Diagnostic {
	diag := diagnostic.Diagnostic{
		Severity: diagnostic.SeverityError,
		Category: "appset",
		Message:  message,
		Provenance: diagnostic.Provenance{
			Path: path,
		},
	}
	diag.Code = diagnostic.StableCode(diag)
	return diag
}

func isURLLikeProviderFixturePath(path string) bool {
	return strings.Contains(path, "://") || strings.HasPrefix(path, "//")
}
