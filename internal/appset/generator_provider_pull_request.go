package appset

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/gosimple/slug"
	"github.com/gosimple/unidecode"
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
	if err := compilePullRequestFilters(generator.Filters); err != nil {
		return nil, nil, &generatorParamsError{err: fmt.Errorf("error listing repos: %w", err)}
	}
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

// compilePullRequestFilters mirrors compileFilters in Argo CD v3.6.1
// (applicationset/services/pull_request/utils.go:11-37). ListPullRequests
// compiles every filter before it lists pull requests, so a regexp that
// does not compile fails the ApplicationSet whatever the provider returns.
func compilePullRequestFilters(filters []argoappv1.PullRequestGeneratorFilter) error {
	for _, filter := range filters {
		if filter.BranchMatch != nil {
			if _, err := regexp.Compile(*filter.BranchMatch); err != nil {
				return fmt.Errorf("error compiling BranchMatch regexp %q: %w", *filter.BranchMatch, err)
			}
		}
		if filter.TargetBranchMatch != nil {
			if _, err := regexp.Compile(*filter.TargetBranchMatch); err != nil {
				return fmt.Errorf("error compiling TargetBranchMatch regexp %q: %w", *filter.TargetBranchMatch, err)
			}
		}
		if filter.TitleMatch != nil {
			if _, err := regexp.Compile(*filter.TitleMatch); err != nil {
				return fmt.Errorf("error compiling TitleMatch regexp %q: %w", *filter.TitleMatch, err)
			}
		}
	}
	return nil
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
		"branch_slug":        pullRequestBranchSlug(input.Branch),
		"target_branch":      input.TargetBranch,
		"target_branch_slug": pullRequestBranchSlug(input.TargetBranch),
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

// pullRequestSlugMaxLength is the slug.MaxLength that the Argo CD v3.6.1
// pull request generator sets, to leave room in a 63-character DNS label
// (applicationset/generators/pull_request.go:84-88).
const pullRequestSlugMaxLength = 50

var (
	// pullRequestSlugCustomSub is the slug.CustomSub that the pull request
	// generator sets (pull_request.go:90-93).
	pullRequestSlugCustomSub = map[string]string{"_": "-"}
	// pullRequestSlugEnglishSub is the "en" language table of
	// github.com/gosimple/slug v1.15.0 (languages_substitution.go), which
	// slug.Make uses.
	pullRequestSlugEnglishSub     = map[rune]string{'&': "and", '@': "at"}
	pullRequestSlugNonAuthorized  = regexp.MustCompile("[^a-zA-Z0-9-_]")
	pullRequestSlugMultipleDashes = regexp.MustCompile("-+")
)

// pullRequestBranchSlug computes branch_slug and target_branch_slug as the
// Argo CD v3.6.1 pull request generator does: slug.Make from
// github.com/gosimple/slug v1.15.0 with MaxLength 50, "_" substituted by
// "-", and the library defaults for every other option, so smart
// truncation cuts after the last whole word and accented letters become
// plain ASCII ("fix/café-crème" gives "fix-cafe-creme"). It does not call
// slug.Make, because slug keeps its options in package variables that
// appsetutils.SlugifyName (the slugify template function) also sets.
//
// Argo CD sets MaxLength and CustomSub before each run but leaves
// EnableSmartTruncate as the last slugify call in the controller process
// set it. This uses the library default, true, which holds unless a
// template in the same controller calls slugify with smartTruncate false.
func pullRequestBranchSlug(branch string) string {
	out := strings.TrimSpace(branch)
	out = slug.Substitute(out, pullRequestSlugCustomSub)
	out = slug.SubstituteRune(out, pullRequestSlugEnglishSub)
	out = unidecode.Unidecode(out)
	out = strings.ToLower(out)
	out = pullRequestSlugNonAuthorized.ReplaceAllString(out, "-")
	out = pullRequestSlugMultipleDashes.ReplaceAllString(out, "-")
	out = strings.Trim(out, "-_")
	return pullRequestSlugSmartTruncate(out)
}

// pullRequestSlugSmartTruncate mirrors smartTruncate in
// github.com/gosimple/slug v1.15.0: a slug longer than the limit is cut at
// the last "-" at or before the limit, or at the limit when there is none.
func pullRequestSlugSmartTruncate(text string) string {
	if len(text) <= pullRequestSlugMaxLength {
		return text
	}
	for i := pullRequestSlugMaxLength; i >= 0; i-- {
		if text[i] == '-' {
			return text[:i]
		}
	}
	return text[:pullRequestSlugMaxLength]
}
