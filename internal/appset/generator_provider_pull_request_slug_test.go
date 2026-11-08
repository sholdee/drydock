package appset

import (
	"strings"
	"testing"

	"github.com/gosimple/slug"
)

// upstreamPullRequestSlug runs slug.Make with the options that the Argo CD
// v3.6.1 pull request generator sets (applicationset/generators/pull_request.go:84-93)
// and the library defaults for the rest. slug keeps its options in package
// variables, so it saves and restores them. The test is not parallel.
func upstreamPullRequestSlug(t *testing.T, branch string) string {
	t.Helper()
	maxLength, smartTruncate, customSub, customRuneSub := slug.MaxLength, slug.EnableSmartTruncate, slug.CustomSub, slug.CustomRuneSub
	lowercase, multipleDashTrim, endsTrim, timestamp := slug.Lowercase, slug.DisableMultipleDashTrim, slug.DisableEndsTrim, slug.AppendTimestamp
	t.Cleanup(func() {
		slug.MaxLength, slug.EnableSmartTruncate, slug.CustomSub, slug.CustomRuneSub = maxLength, smartTruncate, customSub, customRuneSub
		slug.Lowercase, slug.DisableMultipleDashTrim, slug.DisableEndsTrim, slug.AppendTimestamp = lowercase, multipleDashTrim, endsTrim, timestamp
	})
	slug.MaxLength = 50
	slug.CustomSub = map[string]string{"_": "-"}
	slug.EnableSmartTruncate = true
	slug.CustomRuneSub = nil
	slug.Lowercase = true
	slug.DisableMultipleDashTrim = false
	slug.DisableEndsTrim = false
	slug.AppendTimestamp = false
	return slug.Make(branch)
}

func TestPullRequestBranchSlugMatchesArgoCD(t *testing.T) {
	for _, tc := range []struct {
		branch string
		want   string
	}{
		// From the Argo CD v3.6.1 pull request generator tests
		// (applicationset/generators/pull_request_test.go).
		{branch: "branch1", want: "branch1"},
		{branch: "master", want: "master"},
		{branch: "feat/areally+long_pull_request_name_to_test_argo_slugification_and_branch_name_shortening_feature", want: "feat-areally-long-pull-request-name-to-test-argo"},
		{branch: "feat/anotherreally+long_pull_request_name_to_test_argo_slugification_and_branch_name_shortening_feature", want: "feat-anotherreally-long-pull-request-name-to-test"},
		{branch: "a-very-short-sha", want: "a-very-short-sha"},
		{branch: "my-branch", want: "my-branch"},
		// Accents become ASCII letters instead of dashes.
		{branch: "fix/café-crème", want: "fix-cafe-creme"},
		// "&" and "@" use the English substitutions, "_" becomes "-",
		// and the ends lose their dashes.
		{branch: "  Feature/R&D_@home__x-- ", want: "feature-randd-athome-x"},
		{branch: "release/v1.2.3", want: "release-v1-2-3"},
		{branch: "日本語/ブランチ", want: "ri-ben-yu-buranchi"},
		// Smart truncation cuts after the last whole word at or before
		// 50 characters, and a single long word at 50.
		{branch: strings.Repeat("a", 49) + "-b", want: strings.Repeat("a", 49)},
		{branch: strings.Repeat("a", 50) + "-b", want: strings.Repeat("a", 50)},
		{branch: strings.Repeat("a", 60), want: strings.Repeat("a", 50)},
		{branch: "", want: ""},
	} {
		t.Run(tc.branch, func(t *testing.T) {
			upstream := upstreamPullRequestSlug(t, tc.branch)
			if upstream != tc.want {
				t.Fatalf("slug.Make(%q) = %q, table wants %q", tc.branch, upstream, tc.want)
			}
			if got := pullRequestBranchSlug(tc.branch); got != upstream {
				t.Fatalf("pullRequestBranchSlug(%q) = %q, want %q", tc.branch, got, upstream)
			}
		})
	}
}

// The branch_slug and target_branch_slug params use that slug.
func TestPullRequestParamsUseArgoCDSlug(t *testing.T) {
	params, err := pullRequestParams(PullRequestInput{Number: 7, Branch: "fix/café-crème", TargetBranch: "Release_2026/Été", HeadSHA: "0123456789abcdef"}, nil, true, nil)
	if err != nil {
		t.Fatalf("pullRequestParams() error = %v", err)
	}
	if params["branch_slug"] != "fix-cafe-creme" || params["target_branch_slug"] != "release-2026-ete" {
		t.Fatalf("slugs = %q, %q; want fix-cafe-creme, release-2026-ete", params["branch_slug"], params["target_branch_slug"])
	}
}

// The "en" table of gosimple/slug includes defaultSub: quotes are removed,
// and the figure, en, and em dashes and the horizontal bar become "-".
func TestPullRequestBranchSlugDefaultSubstitutions(t *testing.T) {
	for _, tc := range []struct {
		branch string
		want   string
	}{
		{branch: "fix/don't-break", want: "fix-dont-break"},
		{branch: "fix/it’s", want: "fix-its"},
		{branch: `feat/"quoted"`, want: "feat-quoted"},
		{branch: "a—b–c‒d―e", want: "a-b-c-d-e"},
	} {
		upstream := upstreamPullRequestSlug(t, tc.branch)
		if upstream != tc.want {
			t.Fatalf("slug.Make(%q) = %q, table wants %q", tc.branch, upstream, tc.want)
		}
		if got := pullRequestBranchSlug(tc.branch); got != upstream {
			t.Fatalf("pullRequestBranchSlug(%q) = %q, want %q", tc.branch, got, upstream)
		}
	}
}

// TestPullRequestBranchSlugMatchesSlugMakeForEveryRune keeps the copied
// substitution table in step with the slug library: every rune of the Basic
// Multilingual Plane, between two letters, must slug as slug.Make does.
func TestPullRequestBranchSlugMatchesSlugMakeForEveryRune(t *testing.T) {
	upstreamPullRequestSlug(t, "") // sets the slug options for the loop
	for r := rune(0); r <= 0xFFFF; r++ {
		if r >= 0xD800 && r <= 0xDFFF {
			continue
		}
		branch := "a" + string(r) + "b"
		if got, want := pullRequestBranchSlug(branch), slug.Make(branch); got != want {
			t.Fatalf("pullRequestBranchSlug(%q) = %q, slug.Make = %q (rune %U)", branch, got, want, r)
		}
	}
}
