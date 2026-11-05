package appset

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	appsetutils "github.com/argoproj/argo-cd/v3/applicationset/utils"
	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/google/uuid"
	"github.com/sholdee/drydock/internal/diagnostic"
)

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
		repositoryID, err := scmRepositoryIDParam(generator, input.RepositoryID)
		if err != nil {
			return nil, []diagnostic.Diagnostic{scmRepositoryIDInvalidDiagnostic(manifestPath, input, err)}, nil
		}
		params, err := scmRepositoryParams(input, repositoryID, generator.Values, useGoTemplate, goTemplateOptions)
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
		found := slices.ContainsFunc(input.Labels, labelRE.MatchString)
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

// scmRepositoryIDParam converts the fixture repositoryID string to the Go
// value that the Argo CD v3.6.1 SCM provider stores in
// scm_provider.Repository.RepositoryId (an any,
// applicationset/services/scm_provider/types.go:16). The scm_provider
// generator puts that value in the repository_id param unchanged in both
// template modes (applicationset/generators/scm_provider.go:278). The type
// changes the output: Go templates print the number or UUID, while
// fasttemplate only replaces string params and leaves {{repository_id}}
// literal for the integer and UUID providers.
//
// An empty fixture value becomes the zero value of the provider type, as if
// the provider API left the ID out.
func scmRepositoryIDParam(generator *argoappv1.SCMProviderGenerator, id string) (any, error) {
	switch {
	case generator.Github != nil:
		// githubRepo.ID is *int64 (scm_provider/github.go:105; go-github
		// v69 Repository.ID).
		if id == "" {
			return (*int64)(nil), nil
		}
		value, err := parseSCMRepositoryInt(id, 64)
		if err != nil {
			return nil, err
		}
		return &value, nil
	case generator.Gitlab != nil:
		// gitlabRepo.ID is int64 (scm_provider/gitlab.go:138;
		// gitlab.com/gitlab-org/api/client-go v1.46.0 Project.ID).
		if id == "" {
			return int64(0), nil
		}
		return parseSCMRepositoryInt(id, 64)
	case generator.Gitea != nil, generator.BitbucketServer != nil:
		// Gitea converts its int64 ID with int(repo.ID)
		// (scm_provider/gitea.go:129). Bitbucket Server uses
		// bitbucketRepo.ID, an int (scm_provider/bitbucket_server.go:107;
		// go-bitbucket-v1 Repository.ID).
		if id == "" {
			return 0, nil
		}
		value, err := parseSCMRepositoryInt(id, strconv.IntSize)
		return int(value), err
	case generator.AzureDevOps != nil:
		// *azureRepo.Id is a uuid.UUID (scm_provider/azure_devops.go:95).
		if id == "" {
			return uuid.Nil, nil
		}
		value, err := uuid.Parse(id)
		if err != nil {
			return nil, fmt.Errorf("must be a UUID: %w", err)
		}
		return value, nil
	default:
		// Bitbucket Cloud uses the repository UUID string
		// (scm_provider/bitbucket_cloud.go:118) and AWS CodeCommit uses
		// aws.ToString of the repository ID (scm_provider/aws_codecommit.go:120).
		return id, nil
	}
}

func parseSCMRepositoryInt(id string, bitSize int) (int64, error) {
	value, err := strconv.ParseInt(id, 10, bitSize)
	if err != nil {
		return 0, fmt.Errorf("must be a decimal integer: %w", err)
	}
	return value, nil
}

func scmRepositoryIDInvalidDiagnostic(manifestPath string, input SCMRepositoryInput, err error) diagnostic.Diagnostic {
	path := input.FixturePath
	if path == "" {
		path = manifestPath
	}
	return providerFixtureInvalidDiagnostic(path, fmt.Sprintf("provider fixture invalid: scmProvider %s repository %s/%s repositoryID %q %v; Argo CD v3.6.1 passes this provider's repository ID with a typed Go value", input.Provider, input.Organization, input.Repository, input.RepositoryID, err))
}

func scmRepositoryParams(input SCMRepositoryInput, repositoryID any, values map[string]string, useGoTemplate bool, goTemplateOptions []string) (map[string]any, error) {
	params := map[string]any{
		"organization":     input.Organization,
		"repository":       input.Repository,
		"repository_id":    repositoryID,
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
