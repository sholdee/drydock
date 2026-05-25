package appset

import (
	"errors"
	"fmt"
	"maps"
	"regexp"
	"strconv"
	"strings"

	appsetutils "github.com/argoproj/argo-cd/v3/applicationset/utils"
	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/diagnostic"
	"k8s.io/apimachinery/pkg/labels"
)

func evaluateClustersGenerator(ctx generatorContext, generator argoappv1.ApplicationSetGenerator) ([]generatorParamSet, []diagnostic.Diagnostic, bool, error) {
	if !ctx.Options.Provider.Supplied() {
		return nil, unsupportedGeneratorDiagnostic(ctx.ManifestPath), false, nil
	}
	template, err := mergeGeneratorTemplate(ctx.BaseTemplate, generator.Clusters.Template)
	if err != nil {
		return nil, nil, true, err
	}
	paramSets, diags, err := clusterGeneratorParamSets(ctx.ManifestPath, generator.Clusters, ctx.Options.Provider.Data.Clusters, ctx.AppSet.Spec.GoTemplate, ctx.AppSet.Spec.GoTemplateOptions)
	if err != nil {
		return nil, diags, true, err
	}
	paramSets = setGeneratorTemplate(paramSets, template)
	paramSets, selectorDiags, err := applyProviderGeneratorSelector(ctx.ManifestPath, "clusters", generator.Selector, paramSets)
	diags = append(diags, selectorDiags...)
	return paramSets, diags, true, err
}

func evaluateClusterDecisionResourceGenerator(ctx generatorContext, generator argoappv1.ApplicationSetGenerator) ([]generatorParamSet, []diagnostic.Diagnostic, bool, error) {
	if !ctx.Options.Provider.Supplied() {
		return nil, unsupportedGeneratorDiagnostic(ctx.ManifestPath), false, nil
	}
	template, err := mergeGeneratorTemplate(ctx.BaseTemplate, generator.ClusterDecisionResource.Template)
	if err != nil {
		return nil, nil, true, err
	}
	paramSets, diags, err := clusterDecisionResourceParamSets(ctx.ManifestPath, generator.ClusterDecisionResource, ctx.Options.Provider.Data, ctx.AppSet.Spec.GoTemplate, ctx.AppSet.Spec.GoTemplateOptions)
	if err != nil {
		return nil, diags, true, err
	}
	paramSets = setGeneratorTemplate(paramSets, template)
	paramSets, selectorDiags, err := applyProviderGeneratorSelector(ctx.ManifestPath, "clusterDecisionResource", generator.Selector, paramSets)
	diags = append(diags, selectorDiags...)
	return paramSets, diags, true, err
}

func evaluateSCMProviderGenerator(ctx generatorContext, generator argoappv1.ApplicationSetGenerator) ([]generatorParamSet, []diagnostic.Diagnostic, bool, error) {
	if !ctx.Options.Provider.Supplied() {
		return nil, unsupportedGeneratorDiagnostic(ctx.ManifestPath), false, nil
	}
	template, err := mergeGeneratorTemplate(ctx.BaseTemplate, generator.SCMProvider.Template)
	if err != nil {
		return nil, nil, true, err
	}
	paramSets, diags, err := scmProviderParamSets(ctx.ManifestPath, generator.SCMProvider, ctx.Options.Provider.Data.SCMRepositories, ctx.AppSet.Spec.GoTemplate, ctx.AppSet.Spec.GoTemplateOptions)
	if err != nil {
		return nil, diags, true, err
	}
	paramSets = setGeneratorTemplate(paramSets, template)
	paramSets, selectorDiags, err := applyProviderGeneratorSelector(ctx.ManifestPath, "scmProvider", generator.Selector, paramSets)
	diags = append(diags, selectorDiags...)
	return paramSets, diags, true, err
}

func evaluatePullRequestGenerator(ctx generatorContext, generator argoappv1.ApplicationSetGenerator) ([]generatorParamSet, []diagnostic.Diagnostic, bool, error) {
	if !ctx.Options.Provider.Supplied() {
		return nil, unsupportedGeneratorDiagnostic(ctx.ManifestPath), false, nil
	}
	template, err := mergeGeneratorTemplate(ctx.BaseTemplate, generator.PullRequest.Template)
	if err != nil {
		return nil, nil, true, err
	}
	paramSets, diags, err := pullRequestParamSets(ctx.ManifestPath, generator.PullRequest, ctx.Options.Provider.Data.PullRequests, ctx.AppSet.Spec.GoTemplate, ctx.AppSet.Spec.GoTemplateOptions)
	if err != nil {
		return nil, diags, true, err
	}
	paramSets = setGeneratorTemplate(paramSets, template)
	paramSets, selectorDiags, err := applyProviderGeneratorSelector(ctx.ManifestPath, "pullRequest", generator.Selector, paramSets)
	diags = append(diags, selectorDiags...)
	return paramSets, diags, true, err
}

func evaluatePluginGenerator(ctx generatorContext, generator argoappv1.ApplicationSetGenerator) ([]generatorParamSet, []diagnostic.Diagnostic, bool, error) {
	if !ctx.Options.Provider.Supplied() {
		return nil, unsupportedGeneratorDiagnostic(ctx.ManifestPath), false, nil
	}
	template, err := mergeGeneratorTemplate(ctx.BaseTemplate, generator.Plugin.Template)
	if err != nil {
		return nil, nil, true, err
	}
	paramSets, diags, err := pluginParamSets(ctx.ManifestPath, generator.Plugin, ctx.Options.Provider.Data.Plugins, ctx.AppSet.Spec.GoTemplate, ctx.AppSet.Spec.GoTemplateOptions)
	if err != nil {
		return nil, diags, true, err
	}
	paramSets = setGeneratorTemplate(paramSets, template)
	paramSets, selectorDiags, err := applyProviderGeneratorSelector(ctx.ManifestPath, "plugin", generator.Selector, paramSets)
	diags = append(diags, selectorDiags...)
	return paramSets, diags, true, err
}

func clusterGeneratorParamSets(manifestPath string, clusters *argoappv1.ClusterGenerator, inputs []ClusterInput, useGoTemplate bool, goTemplateOptions []string) ([]generatorParamSet, []diagnostic.Diagnostic, error) {
	if len(inputs) == 0 {
		return nil, []diagnostic.Diagnostic{providerNoMatchDiagnostic(manifestPath, "clusters")}, nil
	}

	selector, err := appsetutils.LabelSelectorAsSelector(&clusters.Selector)
	if err != nil {
		return nil, []diagnostic.Diagnostic{providerUnsupportedFilterDiagnostic(manifestPath, fmt.Sprintf("clusters selector: %v", err))}, nil
	}

	var out []generatorParamSet
	for _, cluster := range inputs {
		if !selector.Matches(labels.Set(cluster.Labels)) {
			continue
		}
		params, err := clusterParams(cluster, clusters.Values, useGoTemplate, goTemplateOptions)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, generatorParamSet{
			Params:    params,
			Generator: "clusters",
		})
	}
	if len(out) == 0 {
		return nil, []diagnostic.Diagnostic{providerNoMatchDiagnostic(manifestPath, "clusters")}, nil
	}
	if clusters.FlatList {
		clusterList := make([]any, 0, len(out))
		for _, paramSet := range out {
			clusterList = append(clusterList, paramSet.Params)
		}
		return []generatorParamSet{{
			Params:    map[string]any{"clusters": clusterList},
			Generator: "clusters",
		}}, nil, nil
	}
	return out, nil, nil
}

func clusterParams(cluster ClusterInput, values map[string]string, useGoTemplate bool, goTemplateOptions []string) (map[string]any, error) {
	project := cluster.Project
	params := map[string]any{
		"name":           cluster.Name,
		"nameNormalized": appsetutils.SanitizeName(cluster.Name),
		"server":         cluster.Server,
		"project":        project,
	}

	if useGoTemplate {
		params["metadata"] = map[string]any{
			"labels":      stringMapAny(cluster.Labels),
			"annotations": stringMapAny(cluster.Annotations),
		}
	} else {
		for key, value := range cluster.Labels {
			params["metadata.labels."+key] = value
		}
		for key, value := range cluster.Annotations {
			params["metadata.annotations."+key] = value
		}
	}

	if err := appendTemplatedValues(params, values, useGoTemplate, goTemplateOptions); err != nil {
		return nil, err
	}
	return params, nil
}

func clusterDecisionResourceParamSets(manifestPath string, generator *argoappv1.DuckTypeGenerator, data ProviderData, useGoTemplate bool, goTemplateOptions []string) ([]generatorParamSet, []diagnostic.Diagnostic, error) {
	if len(data.ClusterDecisions) == 0 {
		return nil, []diagnostic.Diagnostic{providerNoMatchDiagnostic(manifestPath, "clusterDecisionResource")}, nil
	}
	if err := validateClusterDecisionResourceGenerator(generator); err != nil {
		return nil, []diagnostic.Diagnostic{providerUnsupportedFilterDiagnostic(manifestPath, err.Error())}, nil
	}

	clustersByName := clusterInputsByName(data.Clusters)
	out, diag, err := clusterDecisionResourceInputParamSets(manifestPath, generator, data.ClusterDecisions, clustersByName, useGoTemplate, goTemplateOptions)
	if diag != nil {
		return nil, []diagnostic.Diagnostic{*diag}, nil
	}
	if err != nil {
		return nil, nil, err
	}
	if len(out) == 0 {
		return nil, []diagnostic.Diagnostic{providerNoMatchDiagnostic(manifestPath, "clusterDecisionResource")}, nil
	}
	return out, nil, nil
}

func validateClusterDecisionResourceGenerator(generator *argoappv1.DuckTypeGenerator) error {
	hasLabelSelector := len(generator.LabelSelector.MatchLabels) > 0 || len(generator.LabelSelector.MatchExpressions) > 0
	switch {
	case generator.Name == "" && !hasLabelSelector:
		return errors.New("clusterDecisionResource must set exactly one of name or labelSelector with provider fixtures")
	case generator.Name != "" && hasLabelSelector:
		return errors.New("clusterDecisionResource cannot combine name and labelSelector with provider fixtures")
	default:
		return nil
	}
}

func clusterInputsByName(clusters []ClusterInput) map[string]ClusterInput {
	clustersByName := map[string]ClusterInput{}
	for _, cluster := range clusters {
		clustersByName[cluster.Name] = cluster
	}
	return clustersByName
}

func clusterDecisionResourceInputParamSets(manifestPath string, generator *argoappv1.DuckTypeGenerator, inputs []ClusterDecisionInput, clustersByName map[string]ClusterInput, useGoTemplate bool, goTemplateOptions []string) ([]generatorParamSet, *diagnostic.Diagnostic, error) {
	var out []generatorParamSet
	for _, input := range inputs {
		matched, err := clusterDecisionResourceMatches(generator, input)
		if err != nil {
			diag := providerUnsupportedFilterDiagnostic(manifestPath, err.Error())
			return nil, &diag, nil
		}
		if !matched {
			continue
		}
		paramSets, err := clusterDecisionResourceDecisionParamSets(generator, input, clustersByName, useGoTemplate, goTemplateOptions)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, paramSets...)
	}
	return out, nil, nil
}

func clusterDecisionResourceDecisionParamSets(generator *argoappv1.DuckTypeGenerator, input ClusterDecisionInput, clustersByName map[string]ClusterInput, useGoTemplate bool, goTemplateOptions []string) ([]generatorParamSet, error) {
	if input.StatusListKey != defaultClusterDecisionStatusListKey || input.MatchKey == "" {
		return nil, nil
	}
	out := make([]generatorParamSet, 0, len(input.Decisions))
	for _, decision := range input.Decisions {
		paramSet, ok, err := clusterDecisionResourceDecisionParamSet(generator, input.MatchKey, decision, clustersByName, useGoTemplate, goTemplateOptions)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, paramSet)
		}
	}
	return out, nil
}

func clusterDecisionResourceDecisionParamSet(generator *argoappv1.DuckTypeGenerator, matchKey string, decision map[string]any, clustersByName map[string]ClusterInput, useGoTemplate bool, goTemplateOptions []string) (generatorParamSet, bool, error) {
	matchValue, ok := decision[matchKey]
	if !ok || fmt.Sprint(matchValue) == "" {
		return generatorParamSet{}, false, nil
	}
	cluster, ok := clustersByName[fmt.Sprint(matchValue)]
	if !ok {
		return generatorParamSet{}, false, nil
	}
	params := map[string]any{
		"name":   cluster.Name,
		"server": cluster.Server,
	}
	for key, value := range decision {
		params[key] = fmt.Sprint(value)
	}
	if err := appendTemplatedValues(params, generator.Values, useGoTemplate, goTemplateOptions); err != nil {
		return generatorParamSet{}, false, err
	}
	return generatorParamSet{
		Params:    params,
		Generator: "clusterDecisionResource",
	}, true, nil
}

func clusterDecisionResourceMatches(generator *argoappv1.DuckTypeGenerator, input ClusterDecisionInput) (bool, error) {
	if generator.ConfigMapRef != input.ConfigMapRef {
		return false, nil
	}
	if generator.Name != "" && generator.Name != input.ResourceName {
		return false, nil
	}
	if len(generator.LabelSelector.MatchLabels) == 0 && len(generator.LabelSelector.MatchExpressions) == 0 {
		return true, nil
	}
	selector, err := appsetutils.LabelSelectorAsSelector(&generator.LabelSelector)
	if err != nil {
		return false, fmt.Errorf("clusterDecisionResource labelSelector: %w", err)
	}
	return selector.Matches(labels.Set(input.Labels)), nil
}

func scmProviderParamSets(manifestPath string, generator *argoappv1.SCMProviderGenerator, inputs []SCMRepositoryInput, useGoTemplate bool, goTemplateOptions []string) ([]generatorParamSet, []diagnostic.Diagnostic, error) {
	if len(inputs) == 0 {
		return nil, []diagnostic.Diagnostic{providerNoMatchDiagnostic(manifestPath, "scmProvider")}, nil
	}
	if err := scmProviderOfflinePreflight(generator); err != nil {
		return nil, []diagnostic.Diagnostic{providerUnsupportedFilterDiagnostic(manifestPath, fmt.Sprintf("scmProvider: %v", err))}, nil
	}

	var scoped []SCMRepositoryInput
	for _, input := range inputs {
		if scmProviderMatchesScope(generator, input) {
			scoped = append(scoped, input)
		}
	}
	if len(scoped) == 0 {
		return nil, []diagnostic.Diagnostic{providerNoMatchDiagnostic(manifestPath, "scmProvider")}, nil
	}

	var out []generatorParamSet
	for _, input := range scoped {
		matches, err := scmRepositoryMatchesProviderFilters(input, generator)
		if err != nil {
			return nil, []diagnostic.Diagnostic{providerUnsupportedFilterDiagnostic(manifestPath, fmt.Sprintf("scmProvider: %v", err))}, nil
		}
		if !matches {
			continue
		}
		matches, err = scmRepositoryMatchesFilters(input, generator.Filters)
		if err != nil {
			return nil, []diagnostic.Diagnostic{providerUnsupportedFilterDiagnostic(manifestPath, fmt.Sprintf("scmProvider: %v", err))}, nil
		}
		if !matches {
			continue
		}
		params, err := scmRepositoryParams(input, generator.Values, useGoTemplate, goTemplateOptions)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, generatorParamSet{
			Params:    params,
			Generator: "scmProvider",
		})
	}
	if len(out) == 0 {
		return nil, []diagnostic.Diagnostic{providerNoMatchDiagnostic(manifestPath, "scmProvider")}, nil
	}
	return out, nil, nil
}

func scmProviderMatchesScope(generator *argoappv1.SCMProviderGenerator, input SCMRepositoryInput) bool {
	switch {
	case generator.Github != nil:
		return scmProviderMatchesGitHubScope(generator, input)
	case generator.Gitlab != nil:
		return scmProviderMatchesGitLabScope(generator, input)
	case generator.Gitea != nil:
		return scmProviderMatchesGiteaScope(generator, input)
	case generator.Bitbucket != nil:
		return scmProviderMatchesBitbucketScope(generator, input)
	case generator.BitbucketServer != nil:
		return scmProviderMatchesBitbucketServerScope(generator, input)
	case generator.AzureDevOps != nil:
		return scmProviderMatchesAzureDevOpsScope(generator, input)
	case generator.AWSCodeCommit != nil:
		return scmProviderMatchesAWSCodeCommitScope(generator, input)
	default:
		return false
	}
}

func scmProviderMatchesGitHubScope(generator *argoappv1.SCMProviderGenerator, input SCMRepositoryInput) bool {
	return providerMatches(input.Provider, "github") && scopeEqual(input.Organization, generator.Github.Organization)
}

func scmProviderMatchesGitLabScope(generator *argoappv1.SCMProviderGenerator, input SCMRepositoryInput) bool {
	return providerMatches(input.Provider, "gitlab") && gitLabGroupScopeEqual(input.Organization, generator.Gitlab.Group, generator.Gitlab.IncludeSubgroups)
}

func scmProviderMatchesGiteaScope(generator *argoappv1.SCMProviderGenerator, input SCMRepositoryInput) bool {
	return providerMatches(input.Provider, "gitea") && scopeEqual(input.Organization, generator.Gitea.Owner)
}

func scmProviderMatchesBitbucketScope(generator *argoappv1.SCMProviderGenerator, input SCMRepositoryInput) bool {
	return providerMatches(input.Provider, "bitbucket") && scopeEqual(input.Organization, generator.Bitbucket.Owner)
}

func scmProviderMatchesBitbucketServerScope(generator *argoappv1.SCMProviderGenerator, input SCMRepositoryInput) bool {
	return providerMatches(input.Provider, "bitbucketServer") && scopeEqual(providerProject(input), generator.BitbucketServer.Project)
}

func scmProviderMatchesAzureDevOpsScope(generator *argoappv1.SCMProviderGenerator, input SCMRepositoryInput) bool {
	return providerMatches(input.Provider, "azureDevOps") &&
		scopeEqual(input.Organization, generator.AzureDevOps.Organization) &&
		scopeEqual(providerProject(input), generator.AzureDevOps.TeamProject)
}

func scmProviderMatchesAWSCodeCommitScope(generator *argoappv1.SCMProviderGenerator, input SCMRepositoryInput) bool {
	return providerMatches(input.Provider, "awsCodeCommit") && optionalScopeEqual(input.Region, generator.AWSCodeCommit.Region)
}

func scmRepositoryMatchesProviderFilters(input SCMRepositoryInput, generator *argoappv1.SCMProviderGenerator) (bool, error) {
	if generator.Gitlab != nil && generator.Gitlab.Topic != "" {
		if input.Labels == nil {
			return false, errors.New("gitlab.topic requires fixture labels")
		}
		if !stringSliceContains(input.Labels, generator.Gitlab.Topic) {
			return false, nil
		}
	}
	if generator.AWSCodeCommit != nil && len(generator.AWSCodeCommit.TagFilters) != 0 {
		if input.Tags == nil {
			return false, errors.New("awsCodeCommit.tagFilters require fixture tags")
		}
		if !scmRepositoryMatchesAWSTagFilters(input.Tags, generator.AWSCodeCommit.TagFilters) {
			return false, nil
		}
	}
	return true, nil
}

func scmProviderOfflinePreflight(generator *argoappv1.SCMProviderGenerator) error {
	if generator.AWSCodeCommit != nil && generator.AWSCodeCommit.Region == "" {
		return errors.New("awsCodeCommit.region must be explicit for offline fixture matching")
	}
	return nil
}

func scmRepositoryMatchesAWSTagFilters(tags map[string]string, filters []*argoappv1.TagFilter) bool {
	grouped := map[string][]string{}
	for _, filter := range filters {
		if filter == nil {
			continue
		}
		grouped[filter.Key] = append(grouped[filter.Key], filter.Value)
	}
	for key, values := range grouped {
		actual, ok := tags[key]
		if !ok {
			return false
		}
		values = nonEmptyStrings(values)
		if len(values) == 0 {
			continue
		}
		if !stringSliceContains(values, actual) {
			return false
		}
	}
	return true
}

func nonEmptyStrings(values []string) []string {
	out := values[:0]
	for _, value := range values {
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func scmRepositoryMatchesFilters(input SCMRepositoryInput, filters []argoappv1.SCMProviderGeneratorFilter) (bool, error) {
	if len(filters) == 0 {
		return true, nil
	}

	repoFilters := make([]argoappv1.SCMProviderGeneratorFilter, 0, len(filters))
	branchFilters := make([]argoappv1.SCMProviderGeneratorFilter, 0, len(filters))
	for _, filter := range filters {
		switch scmFilterType(filter) {
		case "repo":
			repoFilters = append(repoFilters, filter)
		case "branch":
			branchFilters = append(branchFilters, filter)
		}
	}

	if len(repoFilters) != 0 {
		matchedRepo := false
		for _, filter := range repoFilters {
			matches, err := scmRepositoryMatchesFilter(input, filter)
			if err != nil {
				return false, err
			}
			if matches {
				matchedRepo = true
				break
			}
		}
		if !matchedRepo {
			return false, nil
		}
	}

	if len(branchFilters) == 0 {
		return true, nil
	}

	for _, filter := range branchFilters {
		matches, err := scmRepositoryMatchesFilter(input, filter)
		if err != nil {
			return false, err
		}
		if matches {
			return true, nil
		}
	}

	return false, nil
}

func scmFilterType(filter argoappv1.SCMProviderGeneratorFilter) string {
	switch {
	case filter.BranchMatch != nil || len(filter.PathsExist) != 0 || len(filter.PathsDoNotExist) != 0:
		return "branch"
	case filter.RepositoryMatch != nil || filter.LabelMatch != nil:
		return "repo"
	default:
		return ""
	}
}

func scmRepositoryMatchesFilter(input SCMRepositoryInput, filter argoappv1.SCMProviderGeneratorFilter) (bool, error) {
	checks := []func(SCMRepositoryInput, argoappv1.SCMProviderGeneratorFilter) (bool, error){
		scmRepositoryMatchesRepositoryFilter,
		scmRepositoryMatchesBranchFilter,
		scmRepositoryMatchesLabelFilter,
		scmRepositoryMatchesPathsExistFilter,
		scmRepositoryMatchesPathsDoNotExistFilter,
	}
	for _, check := range checks {
		matches, err := check(input, filter)
		if err != nil || !matches {
			return matches, err
		}
	}
	return true, nil
}

func scmRepositoryMatchesRepositoryFilter(input SCMRepositoryInput, filter argoappv1.SCMProviderGeneratorFilter) (bool, error) {
	if filter.RepositoryMatch != nil {
		matches, err := regexp.MatchString(*filter.RepositoryMatch, input.Repository)
		if err != nil {
			return false, fmt.Errorf("repositoryMatch %q: %w", *filter.RepositoryMatch, err)
		}
		if !matches {
			return false, nil
		}
	}
	return true, nil
}

func scmRepositoryMatchesBranchFilter(input SCMRepositoryInput, filter argoappv1.SCMProviderGeneratorFilter) (bool, error) {
	if filter.BranchMatch != nil {
		matches, err := regexp.MatchString(*filter.BranchMatch, input.Branch)
		if err != nil {
			return false, fmt.Errorf("branchMatch %q: %w", *filter.BranchMatch, err)
		}
		if !matches {
			return false, nil
		}
	}
	return true, nil
}

func scmRepositoryMatchesLabelFilter(input SCMRepositoryInput, filter argoappv1.SCMProviderGeneratorFilter) (bool, error) {
	if filter.LabelMatch != nil {
		if input.Labels == nil {
			return false, errors.New("labelMatch requires fixture labels")
		}
		labelRE, err := regexp.Compile(*filter.LabelMatch)
		if err != nil {
			return false, fmt.Errorf("labelMatch %q: %w", *filter.LabelMatch, err)
		}
		found := false
		for _, label := range input.Labels {
			if labelRE.MatchString(label) {
				found = true
				break
			}
		}
		if !found {
			return false, nil
		}
	}
	return true, nil
}

func scmRepositoryMatchesPathsExistFilter(input SCMRepositoryInput, filter argoappv1.SCMProviderGeneratorFilter) (bool, error) {
	if len(filter.PathsExist) != 0 {
		if input.Paths == nil {
			return false, errors.New("pathsExist requires fixture paths")
		}
		for _, requiredPath := range filter.PathsExist {
			if !stringSliceContains(input.Paths, strings.TrimRight(requiredPath, "/")) {
				return false, nil
			}
		}
	}
	return true, nil
}

func scmRepositoryMatchesPathsDoNotExistFilter(input SCMRepositoryInput, filter argoappv1.SCMProviderGeneratorFilter) (bool, error) {
	if len(filter.PathsDoNotExist) != 0 {
		if input.Paths == nil {
			return false, errors.New("pathsDoNotExist requires fixture paths")
		}
		for _, rejectedPath := range filter.PathsDoNotExist {
			if stringSliceContains(input.Paths, strings.TrimRight(rejectedPath, "/")) {
				return false, nil
			}
		}
	}
	return true, nil
}

func scmRepositoryParams(input SCMRepositoryInput, values map[string]string, useGoTemplate bool, goTemplateOptions []string) (map[string]any, error) {
	params := map[string]any{
		"organization":     input.Organization,
		"repository":       input.Repository,
		"repository_id":    input.RepositoryID,
		"url":              input.URL,
		"branch":           input.Branch,
		"branchNormalized": appsetutils.SanitizeName(input.Branch),
		"sha":              input.SHA,
		"short_sha":        shortString(input.SHA, 8),
		"short_sha_7":      shortString(input.SHA, 7),
		"labels":           strings.Join(input.Labels, ","),
	}
	if err := appendTemplatedValues(params, values, useGoTemplate, goTemplateOptions); err != nil {
		return nil, err
	}
	return params, nil
}

func pullRequestParamSets(manifestPath string, generator *argoappv1.PullRequestGenerator, inputs []PullRequestInput, useGoTemplate bool, goTemplateOptions []string) ([]generatorParamSet, []diagnostic.Diagnostic, error) {
	if len(inputs) == 0 {
		return nil, []diagnostic.Diagnostic{providerNoMatchDiagnostic(manifestPath, "pullRequest")}, nil
	}

	var scoped []PullRequestInput
	for _, input := range inputs {
		if pullRequestMatchesScope(generator, input) {
			scoped = append(scoped, input)
		}
	}
	if len(scoped) == 0 {
		return nil, []diagnostic.Diagnostic{providerNoMatchDiagnostic(manifestPath, "pullRequest")}, nil
	}

	requiredLabels := pullRequestRequiredLabels(generator)
	var out []generatorParamSet
	for _, input := range scoped {
		matches, err := pullRequestMatchesProviderState(input, generator)
		if err != nil {
			return nil, []diagnostic.Diagnostic{providerUnsupportedFilterDiagnostic(manifestPath, fmt.Sprintf("pullRequest: %v", err))}, nil
		}
		if !matches {
			continue
		}
		matches, err = pullRequestMatchesProviderLabels(input, requiredLabels)
		if err != nil {
			return nil, []diagnostic.Diagnostic{providerUnsupportedFilterDiagnostic(manifestPath, fmt.Sprintf("pullRequest: %v", err))}, nil
		}
		if !matches {
			continue
		}
		matches, err = pullRequestMatchesFilters(input, generator.Filters)
		if err != nil {
			return nil, []diagnostic.Diagnostic{providerUnsupportedFilterDiagnostic(manifestPath, fmt.Sprintf("pullRequest: %v", err))}, nil
		}
		if !matches {
			continue
		}
		params, err := pullRequestParams(input, generator.Values, useGoTemplate, goTemplateOptions)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, generatorParamSet{
			Params:    params,
			Generator: "pullRequest",
		})
	}
	if len(out) == 0 {
		return nil, []diagnostic.Diagnostic{providerNoMatchDiagnostic(manifestPath, "pullRequest")}, nil
	}
	return out, nil, nil
}

func pullRequestMatchesScope(generator *argoappv1.PullRequestGenerator, input PullRequestInput) bool {
	switch {
	case generator.Github != nil:
		return pullRequestMatchesGitHubScope(generator, input)
	case generator.GitLab != nil:
		return pullRequestMatchesGitLabScope(generator, input)
	case generator.Gitea != nil:
		return pullRequestMatchesGiteaScope(generator, input)
	case generator.Bitbucket != nil:
		return pullRequestMatchesBitbucketScope(generator, input)
	case generator.BitbucketServer != nil:
		return pullRequestMatchesBitbucketServerScope(generator, input)
	case generator.AzureDevOps != nil:
		return pullRequestMatchesAzureDevOpsScope(generator, input)
	default:
		return false
	}
}

func pullRequestMatchesGitHubScope(generator *argoappv1.PullRequestGenerator, input PullRequestInput) bool {
	return providerMatches(input.Provider, "github") &&
		scopeEqual(input.Organization, generator.Github.Owner) &&
		scopeEqual(input.Repository, generator.Github.Repo)
}

func pullRequestMatchesGitLabScope(generator *argoappv1.PullRequestGenerator, input PullRequestInput) bool {
	org, repo := splitProviderProject(generator.GitLab.Project)
	return providerMatches(input.Provider, "gitlab") && repositoryProjectScopeEqual(input.Organization, input.Repository, org, repo, generator.GitLab.Project)
}

func pullRequestMatchesGiteaScope(generator *argoappv1.PullRequestGenerator, input PullRequestInput) bool {
	return providerMatches(input.Provider, "gitea") &&
		scopeEqual(input.Organization, generator.Gitea.Owner) &&
		scopeEqual(input.Repository, generator.Gitea.Repo)
}

func pullRequestMatchesBitbucketScope(generator *argoappv1.PullRequestGenerator, input PullRequestInput) bool {
	return providerMatches(input.Provider, "bitbucket") &&
		scopeEqual(input.Organization, generator.Bitbucket.Owner) &&
		scopeEqual(input.Repository, generator.Bitbucket.Repo)
}

func pullRequestMatchesBitbucketServerScope(generator *argoappv1.PullRequestGenerator, input PullRequestInput) bool {
	return providerMatches(input.Provider, "bitbucketServer") &&
		scopeEqual(providerProjectFromPR(input), generator.BitbucketServer.Project) &&
		scopeEqual(input.Repository, generator.BitbucketServer.Repo)
}

func pullRequestMatchesAzureDevOpsScope(generator *argoappv1.PullRequestGenerator, input PullRequestInput) bool {
	return providerMatches(input.Provider, "azureDevOps") &&
		scopeEqual(input.Organization, generator.AzureDevOps.Organization) &&
		scopeEqual(providerProjectFromPR(input), generator.AzureDevOps.Project) &&
		scopeEqual(input.Repository, generator.AzureDevOps.Repo)
}

func pullRequestRequiredLabels(generator *argoappv1.PullRequestGenerator) []string {
	switch {
	case generator.Github != nil:
		return generator.Github.Labels
	case generator.GitLab != nil:
		return generator.GitLab.Labels
	case generator.Gitea != nil:
		return generator.Gitea.Labels
	case generator.AzureDevOps != nil:
		return generator.AzureDevOps.Labels
	default:
		return nil
	}
}

func pullRequestMatchesProviderLabels(input PullRequestInput, required []string) (bool, error) {
	if len(required) == 0 {
		return true, nil
	}
	if input.Labels == nil {
		return false, errors.New("provider labels require fixture labels")
	}
	for _, label := range required {
		if !stringSliceContains(input.Labels, label) {
			return false, nil
		}
	}
	return true, nil
}

func pullRequestMatchesProviderState(input PullRequestInput, generator *argoappv1.PullRequestGenerator) (bool, error) {
	if generator.GitLab == nil || generator.GitLab.PullRequestState == "" {
		return true, nil
	}
	if input.State == "" {
		return false, errors.New("gitlab.pullRequestState requires fixture state")
	}
	return input.State == generator.GitLab.PullRequestState, nil
}

func pullRequestMatchesFilters(input PullRequestInput, filters []argoappv1.PullRequestGeneratorFilter) (bool, error) {
	if len(filters) == 0 {
		return true, nil
	}
	for _, filter := range filters {
		matches, err := pullRequestMatchesFilter(input, filter)
		if err != nil {
			return false, err
		}
		if matches {
			return true, nil
		}
	}
	return false, nil
}

func pullRequestMatchesFilter(input PullRequestInput, filter argoappv1.PullRequestGeneratorFilter) (bool, error) {
	if filter.BranchMatch != nil {
		matches, err := regexp.MatchString(*filter.BranchMatch, input.Branch)
		if err != nil {
			return false, fmt.Errorf("branchMatch %q: %w", *filter.BranchMatch, err)
		}
		if !matches {
			return false, nil
		}
	}
	if filter.TargetBranchMatch != nil {
		matches, err := regexp.MatchString(*filter.TargetBranchMatch, input.TargetBranch)
		if err != nil {
			return false, fmt.Errorf("targetBranchMatch %q: %w", *filter.TargetBranchMatch, err)
		}
		if !matches {
			return false, nil
		}
	}
	if filter.TitleMatch != nil {
		matches, err := regexp.MatchString(*filter.TitleMatch, input.Title)
		if err != nil {
			return false, fmt.Errorf("titleMatch %q: %w", *filter.TitleMatch, err)
		}
		if !matches {
			return false, nil
		}
	}
	return true, nil
}

func pullRequestParams(input PullRequestInput, values map[string]string, useGoTemplate bool, goTemplateOptions []string) (map[string]any, error) {
	params := map[string]any{
		"number":             strconv.Itoa(input.Number),
		"title":              input.Title,
		"branch":             input.Branch,
		"branch_slug":        appsetutils.SlugifyName(50, false, input.Branch),
		"target_branch":      input.TargetBranch,
		"target_branch_slug": appsetutils.SlugifyName(50, false, input.TargetBranch),
		"head_sha":           input.HeadSHA,
		"head_short_sha":     shortString(input.HeadSHA, 8),
		"head_short_sha_7":   shortString(input.HeadSHA, 7),
		"author":             input.Author,
	}
	if useGoTemplate {
		params["labels"] = append([]string(nil), input.Labels...)
	}
	if err := appendTemplatedValues(params, values, useGoTemplate, goTemplateOptions); err != nil {
		return nil, err
	}
	return params, nil
}

func pluginParamSets(manifestPath string, generator *argoappv1.PluginGenerator, inputs []PluginInput, useGoTemplate bool, goTemplateOptions []string) ([]generatorParamSet, []diagnostic.Diagnostic, error) {
	if len(inputs) == 0 {
		return nil, []diagnostic.Diagnostic{providerNoMatchDiagnostic(manifestPath, "plugin")}, nil
	}

	configMapName := generator.ConfigMapRef.Name
	matched := false
	var out []generatorParamSet
	for _, input := range inputs {
		if input.ConfigMapRef != configMapName {
			continue
		}
		matched = true
		for _, output := range input.Outputs {
			outputMap, ok := output.(map[string]any)
			if !ok || outputMap == nil {
				return nil, []diagnostic.Diagnostic{providerUnsupportedFilterDiagnostic(manifestPath, "plugin: fixture output must be a mapping object")}, nil
			}
			params, err := pluginParams(outputMap, generator.Input.Parameters, generator.Values, useGoTemplate, goTemplateOptions)
			if err != nil {
				return nil, nil, err
			}
			out = append(out, generatorParamSet{
				Params:    params,
				Generator: "plugin",
			})
		}
	}
	if len(out) == 0 {
		if matched {
			return nil, nil, nil
		}
		return nil, []diagnostic.Diagnostic{providerNoMatchDiagnostic(manifestPath, "plugin")}, nil
	}
	return out, nil, nil
}

func pluginParams(output map[string]any, inputParameters argoappv1.PluginParameters, values map[string]string, useGoTemplate bool, goTemplateOptions []string) (map[string]any, error) {
	params := map[string]any{}
	if useGoTemplate {
		maps.Copy(params, output)
	} else {
		flattenParams("", output, params)
	}
	params["generator"] = map[string]any{
		"input": map[string]argoappv1.PluginParameters{
			"parameters": inputParameters,
		},
	}
	if err := appendTemplatedValues(params, values, useGoTemplate, goTemplateOptions); err != nil {
		return nil, err
	}
	return params, nil
}

func providerMatches(actual, expected string) bool {
	return normalizeProvider(actual) == normalizeProvider(expected)
}

func normalizeProvider(value string) string {
	value = strings.ToLower(value)
	value = strings.ReplaceAll(value, "-", "")
	value = strings.ReplaceAll(value, "_", "")
	value = strings.ReplaceAll(value, " ", "")
	return value
}

func scopeEqual(actual, expected string) bool {
	return expected != "" && actual == expected
}

func optionalScopeEqual(actual, expected string) bool {
	return expected == "" || actual == expected
}

func repositoryProjectScopeEqual(actualOrg, actualRepo, scopeOrg, scopeRepo, fullProject string) bool {
	if scopeRepo == "" {
		return actualRepo == fullProject || actualOrg == fullProject
	}
	return actualOrg == scopeOrg && actualRepo == scopeRepo
}

func splitProviderProject(project string) (string, string) {
	i := strings.LastIndex(project, "/")
	if i < 0 {
		return "", project
	}
	return project[:i], project[i+1:]
}

func gitLabGroupScopeEqual(actual, expected string, includeSubgroups bool) bool {
	if expected == "" {
		return false
	}
	if actual == expected {
		return true
	}
	return includeSubgroups && strings.HasPrefix(actual, expected+"/")
}

func providerProject(input SCMRepositoryInput) string {
	if input.Project != "" {
		return input.Project
	}
	return input.Organization
}

func providerProjectFromPR(input PullRequestInput) string {
	if input.Project != "" {
		return input.Project
	}
	return input.Organization
}

func stringSliceContains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func shortString(value string, limit int) string {
	if len(value) < limit {
		return value
	}
	return value[:limit]
}

func stringMapAny(input map[string]string) map[string]any {
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}
