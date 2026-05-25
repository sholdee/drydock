package appset

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"

	appsetutils "github.com/argoproj/argo-cd/v3/applicationset/utils"
	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/diagnostic"
)

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
