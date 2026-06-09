package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/sholdee/drydock/internal/diagnostic"
	"go.yaml.in/yaml/v3"
)

type conditionCategory string

const (
	conditionCategoryNone            conditionCategory = "none"
	conditionCategorySource          conditionCategory = "source"
	conditionCategoryDestination     conditionCategory = "destination"
	conditionCategorySourceNamespace conditionCategory = "source-namespace"
)

var monitoredDrydockCodes = map[string]struct{}{
	diagnostic.CodeProjectSourceRepositoryDenied: {},
	diagnostic.CodeProjectDestinationDenied:      {},
	diagnostic.CodeProjectSourceNamespaceDenied:  {},
}

type options struct {
	ArgoCDAppDir       string
	DrydockDiagnostics string
	Expected           string
	Out                string
}

type expectedFile struct {
	Cases []expectedCase `yaml:"cases"`
}

type expectedCase struct {
	Name                  string            `yaml:"name"`
	Namespace             string            `yaml:"namespace"`
	ArgoCDCondition       conditionCategory `yaml:"argocdCondition"`
	DrydockDiagnosticCode string            `yaml:"drydockDiagnosticCode"`
}

type appStatusFile struct {
	Metadata struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	Status struct {
		Conditions []appCondition `json:"conditions"`
	} `json:"status"`
}

type appCondition struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type drydockDiagnosticFile struct {
	Diagnostics []diagnostic.Diagnostic `json:"diagnostics"`
}

type comparisonResult struct {
	Cases    int
	Failures []string
	Lines    []string
}

func main() {
	var opts options
	flag.StringVar(&opts.ArgoCDAppDir, "argocd-app-dir", "", "directory containing captured Argo CD Application JSON files")
	flag.StringVar(&opts.DrydockDiagnostics, "drydock-diagnostics", "", "drydock diag --render -o json output file")
	flag.StringVar(&opts.Expected, "expected", "", "YAML file describing expected project policy outcomes")
	flag.StringVar(&opts.Out, "out", "", "optional path for a text summary")
	flag.Parse()

	result, err := compare(opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "argocd project policy smoke: %v\n", err)
		os.Exit(2)
	}
	summary := result.Summary()
	if opts.Out != "" {
		if err := os.WriteFile(opts.Out, []byte(summary), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "argocd project policy smoke: write summary: %v\n", err)
			os.Exit(2)
		}
	}
	fmt.Print(summary)
	if len(result.Failures) > 0 {
		os.Exit(1)
	}
}

func compare(opts options) (comparisonResult, error) {
	if err := opts.validate(); err != nil {
		return comparisonResult{}, err
	}
	expected, err := readExpected(opts.Expected)
	if err != nil {
		return comparisonResult{}, err
	}
	apps, err := readApplicationStatusFiles(opts.ArgoCDAppDir)
	if err != nil {
		return comparisonResult{}, err
	}
	drydockDiagnostics, err := readDrydockDiagnostics(opts.DrydockDiagnostics)
	if err != nil {
		return comparisonResult{}, err
	}
	return evaluate(expected, apps, drydockDiagnostics), nil
}

func (opts options) validate() error {
	missing := make([]string, 0, 3)
	if strings.TrimSpace(opts.ArgoCDAppDir) == "" {
		missing = append(missing, "--argocd-app-dir")
	}
	if strings.TrimSpace(opts.DrydockDiagnostics) == "" {
		missing = append(missing, "--drydock-diagnostics")
	}
	if strings.TrimSpace(opts.Expected) == "" {
		missing = append(missing, "--expected")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required flags: %s", strings.Join(missing, ", "))
	}
	return nil
}

func readExpected(path string) ([]expectedCase, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read expected file: %w", err)
	}
	var expected expectedFile
	if err := yaml.Unmarshal(data, &expected); err != nil {
		return nil, fmt.Errorf("parse expected file: %w", err)
	}
	if err := validateExpected(expected.Cases); err != nil {
		return nil, err
	}
	return expected.Cases, nil
}

func validateExpected(cases []expectedCase) error {
	if len(cases) == 0 {
		return errors.New("expected file contains no cases")
	}
	seen := make(map[string]struct{}, len(cases))
	for _, tc := range cases {
		key := appKey(tc.Namespace, tc.Name)
		if strings.TrimSpace(tc.Name) == "" || strings.TrimSpace(tc.Namespace) == "" {
			return fmt.Errorf("expected case must include name and namespace: %#v", tc)
		}
		if _, ok := seen[key]; ok {
			return fmt.Errorf("duplicate expected case: %s", key)
		}
		seen[key] = struct{}{}
		if !validConditionCategory(tc.ArgoCDCondition) {
			return fmt.Errorf("expected case %s has unsupported argocdCondition %q", key, tc.ArgoCDCondition)
		}
	}
	return nil
}

func validConditionCategory(category conditionCategory) bool {
	switch category {
	case conditionCategoryNone, conditionCategorySource, conditionCategoryDestination, conditionCategorySourceNamespace:
		return true
	default:
		return false
	}
}

func readApplicationStatusFiles(dir string) (map[string]appStatusFile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read Argo CD Application directory: %w", err)
	}
	apps := make(map[string]appStatusFile)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		app, err := readApplicationStatusFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(app.Metadata.Name) == "" || strings.TrimSpace(app.Metadata.Namespace) == "" {
			return nil, fmt.Errorf("argocd application file %s is missing metadata.name or metadata.namespace", entry.Name())
		}
		key := appKey(app.Metadata.Namespace, app.Metadata.Name)
		if _, ok := apps[key]; ok {
			return nil, fmt.Errorf("duplicate Argo CD Application capture for %s", key)
		}
		apps[key] = app
	}
	if len(apps) == 0 {
		return nil, fmt.Errorf("no Argo CD Application JSON files found in %s", dir)
	}
	return apps, nil
}

func readApplicationStatusFile(path string) (appStatusFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return appStatusFile{}, fmt.Errorf("read Argo CD Application file %s: %w", path, err)
	}
	var app appStatusFile
	if err := json.Unmarshal(data, &app); err != nil {
		return appStatusFile{}, fmt.Errorf("parse Argo CD Application file %s: %w", path, err)
	}
	return app, nil
}

func readDrydockDiagnostics(path string) ([]diagnostic.Diagnostic, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read drydock diagnostics: %w", err)
	}
	var report drydockDiagnosticFile
	if err := json.Unmarshal(data, &report); err != nil {
		return nil, fmt.Errorf("parse drydock diagnostics: %w", err)
	}
	return report.Diagnostics, nil
}

func evaluate(expected []expectedCase, apps map[string]appStatusFile, drydockDiagnostics []diagnostic.Diagnostic) comparisonResult {
	result := comparisonResult{
		Cases: len(expected),
		Lines: make([]string, 0, len(expected)),
	}
	for _, tc := range expected {
		failures := evaluateCase(tc, apps[appKey(tc.Namespace, tc.Name)], drydockDiagnostics)
		if len(failures) > 0 {
			for _, failure := range failures {
				result.Failures = append(result.Failures, fmt.Sprintf("%s: %s", appKey(tc.Namespace, tc.Name), failure))
			}
			result.Lines = append(result.Lines, fmt.Sprintf("FAIL %s", appKey(tc.Namespace, tc.Name)))
			continue
		}
		result.Lines = append(result.Lines, fmt.Sprintf("ok %s", appKey(tc.Namespace, tc.Name)))
	}
	return result
}

func evaluateCase(tc expectedCase, app appStatusFile, drydockDiagnostics []diagnostic.Diagnostic) []string {
	failures := make([]string, 0, 2)
	if app.Metadata.Name == "" {
		failures = append(failures, "missing Argo CD Application JSON capture")
	} else if failure := evaluateArgoCDCondition(tc, app); failure != "" {
		failures = append(failures, failure)
	}
	if failure := evaluateDrydockDiagnostics(tc, drydockDiagnostics); failure != "" {
		failures = append(failures, failure)
	}
	return failures
}

func evaluateArgoCDCondition(tc expectedCase, app appStatusFile) string {
	actual := policyConditionCategories(app.Status.Conditions)
	expected := expectedConditionCategories(tc.ArgoCDCondition)
	if equalStringSets(categoryStrings(actual), categoryStrings(expected)) {
		return ""
	}
	return fmt.Sprintf("Argo CD policy condition categories got %s, want %s", strings.Join(categoryStrings(actual), ", "), strings.Join(categoryStrings(expected), ", "))
}

func expectedConditionCategories(category conditionCategory) []conditionCategory {
	if category == conditionCategoryNone {
		return nil
	}
	return []conditionCategory{category}
}

func policyConditionCategories(conditions []appCondition) []conditionCategory {
	categories := make([]conditionCategory, 0)
	for _, condition := range conditions {
		switch {
		case strings.EqualFold(condition.Type, "InvalidSpecError"):
			categories = append(categories, classifyInvalidSpecMessage(condition.Message))
		case strings.EqualFold(condition.Type, "UnknownError") && isSourceNamespacePolicyMessage(condition.Message):
			categories = append(categories, conditionCategorySourceNamespace)
		default:
			continue
		}
	}
	return categories
}

func classifyInvalidSpecMessage(message string) conditionCategory {
	normalized := strings.ToLower(message)
	switch {
	case strings.Contains(normalized, "source namespace"):
		return conditionCategorySourceNamespace
	case strings.Contains(normalized, "destination"):
		return conditionCategoryDestination
	case strings.Contains(normalized, "source") || strings.Contains(normalized, "repo"):
		return conditionCategorySource
	default:
		return conditionCategory("unknown")
	}
}

func isSourceNamespacePolicyMessage(message string) bool {
	normalized := strings.ToLower(message)
	return strings.Contains(normalized, " in namespace ") &&
		strings.Contains(normalized, " is not permitted to use project ")
}

func categoryStrings(categories []conditionCategory) []string {
	if len(categories) == 0 {
		return []string{"none"}
	}
	seen := make(map[string]struct{}, len(categories))
	for _, category := range categories {
		seen[string(category)] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for category := range seen {
		out = append(out, category)
	}
	sort.Strings(out)
	return out
}

func evaluateDrydockDiagnostics(tc expectedCase, diagnostics []diagnostic.Diagnostic) string {
	actual := monitoredDrydockCodesForCase(tc, diagnostics)
	expected := expectedDrydockCodes(tc.DrydockDiagnosticCode)
	if equalStringSets(actual, expected) {
		return ""
	}
	return fmt.Sprintf("drydock diagnostic codes got %s, want %s", strings.Join(displaySet(actual), ", "), strings.Join(displaySet(expected), ", "))
}

func expectedDrydockCodes(code string) []string {
	if strings.TrimSpace(code) == "" {
		return nil
	}
	return []string{code}
}

func monitoredDrydockCodesForCase(tc expectedCase, diagnostics []diagnostic.Diagnostic) []string {
	seen := make(map[string]struct{})
	for _, diag := range diagnostics {
		if diag.Provenance.Path != appKey(tc.Namespace, tc.Name) {
			continue
		}
		if _, ok := monitoredDrydockCodes[diag.Code]; ok {
			seen[diag.Code] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for code := range seen {
		out = append(out, code)
	}
	sort.Strings(out)
	return out
}

func equalStringSets(left, right []string) bool {
	left = normalizeStringSet(left)
	right = normalizeStringSet(right)
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func normalizeStringSet(values []string) []string {
	if len(values) == 0 || len(values) == 1 && values[0] == "none" {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" && value != "none" {
			seen[value] = struct{}{}
		}
	}
	out := make([]string, 0, len(seen))
	for value := range seen {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func displaySet(values []string) []string {
	values = normalizeStringSet(values)
	if len(values) == 0 {
		return []string{"none"}
	}
	return values
}

func appKey(namespace, name string) string {
	return strings.TrimSpace(namespace) + "/" + strings.TrimSpace(name)
}

func (result comparisonResult) Summary() string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "Cases: %d\n", result.Cases)
	fmt.Fprintf(&builder, "Failures: %d\n", len(result.Failures))
	for _, line := range result.Lines {
		fmt.Fprintf(&builder, "%s\n", line)
	}
	for _, failure := range result.Failures {
		fmt.Fprintf(&builder, "FAILURE %s\n", failure)
	}
	return builder.String()
}
