package drydock

import "github.com/sholdee/drydock/internal/appset"

// ApplicationSetProviderData supplies explicit offline data for provider-backed
// ApplicationSet generators.
type ApplicationSetProviderData struct {
	Clusters         []ApplicationSetProviderCluster
	ClusterDecisions []ApplicationSetProviderClusterDecision
	SCMRepositories  []ApplicationSetProviderSCMRepository
	PullRequests     []ApplicationSetProviderPullRequest
	Plugins          []ApplicationSetProviderPlugin
}

// ApplicationSetProviderCluster mirrors one cluster fixture entry.
type ApplicationSetProviderCluster struct {
	Name        string
	Server      string
	Project     string
	Labels      map[string]string
	Annotations map[string]string
	Values      map[string]string
}

// ApplicationSetProviderClusterDecision mirrors one cluster decision fixture entry.
type ApplicationSetProviderClusterDecision struct {
	ConfigMapRef  string
	ResourceName  string
	Labels        map[string]string
	MatchKey      string
	StatusListKey string
	Decisions     []map[string]any
	Values        map[string]string
}

// ApplicationSetProviderSCMRepository mirrors one SCM repository fixture entry.
type ApplicationSetProviderSCMRepository struct {
	Provider     string
	Organization string
	Project      string
	Region       string
	Repository   string
	RepositoryID string
	Branch       string
	SHA          string
	URL          string
	Tags         map[string]string
	Labels       []string
	Paths        []string
	Values       map[string]string
}

// ApplicationSetProviderPullRequest mirrors one pull request fixture entry.
type ApplicationSetProviderPullRequest struct {
	Provider     string
	Organization string
	Project      string
	Repository   string
	Number       int
	Title        string
	Branch       string
	TargetBranch string
	HeadSHA      string
	Author       string
	State        string
	Labels       []string
	Values       map[string]string
}

// ApplicationSetProviderPlugin mirrors one plugin fixture entry.
type ApplicationSetProviderPlugin struct {
	ConfigMapRef string
	Outputs      []map[string]any
	Values       map[string]string
}

func applicationSetProviderDataToInternal(data ApplicationSetProviderData) appset.ProviderData {
	out := appset.ProviderData{
		Clusters:         make([]appset.ClusterInput, 0, len(data.Clusters)),
		ClusterDecisions: make([]appset.ClusterDecisionInput, 0, len(data.ClusterDecisions)),
		SCMRepositories:  make([]appset.SCMRepositoryInput, 0, len(data.SCMRepositories)),
		PullRequests:     make([]appset.PullRequestInput, 0, len(data.PullRequests)),
		Plugins:          make([]appset.PluginInput, 0, len(data.Plugins)),
	}
	for _, item := range data.Clusters {
		out.Clusters = append(out.Clusters, appset.ClusterInput{
			Name:        item.Name,
			Server:      item.Server,
			Project:     item.Project,
			Labels:      cloneStringMap(item.Labels),
			Annotations: cloneStringMap(item.Annotations),
			Values:      cloneStringMap(item.Values),
		})
	}
	for _, item := range data.ClusterDecisions {
		out.ClusterDecisions = append(out.ClusterDecisions, appset.ClusterDecisionInput{
			ConfigMapRef:  item.ConfigMapRef,
			ResourceName:  item.ResourceName,
			Labels:        cloneStringMap(item.Labels),
			MatchKey:      item.MatchKey,
			StatusListKey: item.StatusListKey,
			Decisions:     cloneAnyMaps(item.Decisions),
			Values:        cloneStringMap(item.Values),
		})
	}
	for _, item := range data.SCMRepositories {
		out.SCMRepositories = append(out.SCMRepositories, appset.SCMRepositoryInput{
			Provider:     item.Provider,
			Organization: item.Organization,
			Project:      item.Project,
			Region:       item.Region,
			Repository:   item.Repository,
			RepositoryID: item.RepositoryID,
			Branch:       item.Branch,
			SHA:          item.SHA,
			URL:          item.URL,
			Tags:         cloneStringMap(item.Tags),
			Labels:       append([]string(nil), item.Labels...),
			Paths:        append([]string(nil), item.Paths...),
			Values:       cloneStringMap(item.Values),
		})
	}
	for _, item := range data.PullRequests {
		out.PullRequests = append(out.PullRequests, appset.PullRequestInput{
			Provider:     item.Provider,
			Organization: item.Organization,
			Project:      item.Project,
			Repository:   item.Repository,
			Number:       item.Number,
			Title:        item.Title,
			Branch:       item.Branch,
			TargetBranch: item.TargetBranch,
			HeadSHA:      item.HeadSHA,
			Author:       item.Author,
			State:        item.State,
			Labels:       append([]string(nil), item.Labels...),
			Values:       cloneStringMap(item.Values),
		})
	}
	for _, item := range data.Plugins {
		out.Plugins = append(out.Plugins, appset.PluginInput{
			ConfigMapRef: item.ConfigMapRef,
			Outputs:      pluginOutputsToInternal(item.Outputs),
			Values:       cloneStringMap(item.Values),
		})
	}
	return out
}

func pluginOutputsToInternal(input []map[string]any) []any {
	if input == nil {
		return nil
	}
	out := make([]any, 0, len(input))
	for _, item := range input {
		out = append(out, cloneMap(item))
	}
	return out
}
