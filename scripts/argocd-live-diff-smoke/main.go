// Command argocd-live-diff-smoke checks drydock's diff normalization against
// the sync status of a live Argo CD in the Argo CD render parity smoke.
//
// Argo CD syncs each Application at one commit, then compares a second commit
// with the live objects. drydock diffs the same two commits. The expected file
// lists every resource of each Application with the sync status Argo CD must
// report for it. drydock must report a change for a resource exactly when
// that status is OutOfSync. The check fails when either side differs from the
// expected file, or when either side has a resource that the file does not
// list. An Application can also list warnings that drydock must print.
//
// An Application with comparisonError has ignore rules that Argo CD cannot
// build. Argo CD must report a ComparisonError condition and sync status
// Unknown, and a separate drydock diff of that Application must fail with
// the expected message. The main drydock diff leaves it out, because the
// failure fails the whole command.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

// The sync status values of an Argo CD resource and Application.
const (
	statusSynced    = "Synced"
	statusOutOfSync = "OutOfSync"
	statusUnknown   = "Unknown"
)

// comparisonErrorType is the Application condition type that the Argo CD
// controller sets when the comparison fails (controller/state.go at v3.6.1).
const comparisonErrorType = "ComparisonError"

// argocdNamespace is the namespace of the live diff Applications.
const argocdNamespace = "argocd"

type options struct {
	Expected             string
	ArgoCDAppDir         string
	DrydockDiff          string
	DrydockStderr        string
	DrydockFailureDir    string
	Out                  string
	List                 bool
	ListComparisonErrors bool
}

type expectedFile struct {
	TargetRevision string                `yaml:"targetRevision"`
	Applications   []expectedApplication `yaml:"applications"`
}

type expectedApplication struct {
	Name      string             `yaml:"name"`
	Resources []expectedResource `yaml:"resources"`
	// DrydockWarnings are texts that the stderr of the main drydock diff
	// must contain.
	DrydockWarnings []string `yaml:"drydockWarnings"`
	// ComparisonError marks an Application whose comparison must fail.
	ComparisonError *expectedComparisonError `yaml:"comparisonError"`
}

type expectedComparisonError struct {
	// ArgoCDMessage is text that the ComparisonError condition must contain.
	ArgoCDMessage string `yaml:"argocdMessage"`
	// DrydockMessage is text that the stderr of the failed drydock diff of
	// the Application must contain.
	DrydockMessage string `yaml:"drydockMessage"`
}

type expectedResource struct {
	Group  string `yaml:"group"`
	Kind   string `yaml:"kind"`
	Name   string `yaml:"name"`
	Status string `yaml:"status"`
}

// resourceKey names a resource inside one Application. The namespace is left
// out: each fixture Application has one destination namespace.
type resourceKey struct {
	Group string
	Kind  string
	Name  string
}

func (key resourceKey) String() string {
	if key.Group == "" {
		return key.Kind + "/" + key.Name
	}
	return key.Group + "/" + key.Kind + "/" + key.Name
}

type appStatusFile struct {
	Metadata struct {
		Name      string `json:"name"`
		Namespace string `json:"namespace"`
	} `json:"metadata"`
	Status struct {
		Sync struct {
			Status     string `json:"status"`
			ComparedTo struct {
				Source struct {
					TargetRevision string `json:"targetRevision"`
				} `json:"source"`
			} `json:"comparedTo"`
		} `json:"sync"`
		Resources  []appResourceStatus `json:"resources"`
		Conditions []appCondition      `json:"conditions"`
	} `json:"status"`
}

type appCondition struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

type appResourceStatus struct {
	Group  string `json:"group"`
	Kind   string `json:"kind"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

// drydockResult is one entry of `drydock diff apps -o json`.
type drydockResult struct {
	Parent struct {
		Namespace string `json:"namespace"`
		Name      string `json:"name"`
	} `json:"parent"`
	Resource struct {
		Group string `json:"group"`
		Kind  string `json:"kind"`
		Name  string `json:"name"`
	} `json:"resource"`
	Change string `json:"change"`
}

type comparisonResult struct {
	Applications int
	Resources    int
	Differences  int
	Failures     []string
	Lines        []string
}

func main() {
	var opts options
	flag.StringVar(&opts.Expected, "expected", "", "YAML file that lists the expected sync status of each resource")
	flag.StringVar(&opts.ArgoCDAppDir, "argocd-app-dir", "", "directory of captured Argo CD Application JSON files, one <name>.json per Application")
	flag.StringVar(&opts.DrydockDiff, "drydock-diff", "", "output of drydock diff apps -o json")
	flag.StringVar(&opts.DrydockStderr, "drydock-stderr", "", "stderr of the same drydock diff apps run")
	flag.StringVar(&opts.DrydockFailureDir, "drydock-failure-dir", "", "directory of drydock diff app results for the comparisonError Applications, one <name>.exit and <name>.stderr each")
	flag.StringVar(&opts.Out, "out", "", "optional path for a text summary")
	flag.BoolVar(&opts.List, "list", false, "print the names of the Applications without comparisonError, one per line, and exit")
	flag.BoolVar(&opts.ListComparisonErrors, "list-comparison-errors", false, "print the names of the comparisonError Applications, one per line, and exit")
	flag.Parse()

	if opts.List || opts.ListComparisonErrors {
		expected, err := readExpected(opts.Expected)
		if err != nil {
			fmt.Fprintf(os.Stderr, "argocd live diff smoke: %v\n", err)
			os.Exit(2)
		}
		for _, app := range expected.Applications {
			if (app.ComparisonError != nil) == opts.ListComparisonErrors {
				fmt.Println(app.Name)
			}
		}
		return
	}

	result, err := compare(opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "argocd live diff smoke: %v\n", err)
		os.Exit(2)
	}
	summary := result.Summary()
	if opts.Out != "" {
		if err := os.WriteFile(opts.Out, []byte(summary), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "argocd live diff smoke: write summary: %v\n", err)
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
	changes, err := readDrydockChanges(opts.DrydockDiff)
	if err != nil {
		return comparisonResult{}, err
	}
	stderr, err := os.ReadFile(opts.DrydockStderr)
	if err != nil {
		return comparisonResult{}, fmt.Errorf("read drydock diff stderr: %w", err)
	}
	result := comparisonResult{Applications: len(expected.Applications)}
	listed := make(map[string]struct{}, len(expected.Applications))
	for _, app := range expected.Applications {
		listed[app.Name] = struct{}{}
		result.Resources += len(app.Resources)
		failures := evaluateArgoCD(app, expected.TargetRevision, opts.ArgoCDAppDir)
		if app.ComparisonError != nil {
			failures = append(failures, evaluateDrydockFailure(app, opts.DrydockFailureDir)...)
		}
		failures = append(failures, evaluateDrydock(app, changes[app.Name])...)
		for _, warning := range app.DrydockWarnings {
			if !strings.Contains(string(stderr), warning) {
				failures = append(failures, fmt.Sprintf("drydock diff stderr does not contain %q", warning))
			}
		}
		for _, resource := range app.Resources {
			if resource.Status == statusOutOfSync {
				result.Differences++
			}
		}
		if len(failures) == 0 {
			result.Lines = append(result.Lines, "ok "+argocdNamespace+"/"+app.Name)
			continue
		}
		result.Lines = append(result.Lines, "FAIL "+argocdNamespace+"/"+app.Name)
		for _, failure := range failures {
			result.Failures = append(result.Failures, argocdNamespace+"/"+app.Name+": "+failure)
		}
	}
	for _, name := range sortedNames(changes) {
		if _, ok := listed[name]; !ok {
			result.Failures = append(result.Failures, fmt.Sprintf("drydock reports changes for Application %s, which the expected file does not list", name))
		}
	}
	return result, nil
}

func (opts options) validate() error {
	var missing []string
	for _, flagValue := range []struct{ name, value string }{
		{"--expected", opts.Expected},
		{"--argocd-app-dir", opts.ArgoCDAppDir},
		{"--drydock-diff", opts.DrydockDiff},
		{"--drydock-stderr", opts.DrydockStderr},
		{"--drydock-failure-dir", opts.DrydockFailureDir},
	} {
		if strings.TrimSpace(flagValue.value) == "" {
			missing = append(missing, flagValue.name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required flags: %s", strings.Join(missing, ", "))
	}
	return nil
}

func readExpected(path string) (expectedFile, error) {
	if strings.TrimSpace(path) == "" {
		return expectedFile{}, errors.New("missing required flag: --expected")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return expectedFile{}, fmt.Errorf("read expected file: %w", err)
	}
	var expected expectedFile
	if err := yaml.Unmarshal(data, &expected); err != nil {
		return expectedFile{}, fmt.Errorf("parse expected file: %w", err)
	}
	if err := validateExpected(expected); err != nil {
		return expectedFile{}, err
	}
	return expected, nil
}

func validateExpected(expected expectedFile) error {
	if strings.TrimSpace(expected.TargetRevision) == "" {
		return errors.New("expected file must set targetRevision")
	}
	if len(expected.Applications) == 0 {
		return errors.New("expected file lists no Applications")
	}
	apps := make(map[string]struct{}, len(expected.Applications))
	for _, app := range expected.Applications {
		if strings.TrimSpace(app.Name) == "" {
			return errors.New("expected Application must include name")
		}
		if _, ok := apps[app.Name]; ok {
			return fmt.Errorf("duplicate expected Application: %s", app.Name)
		}
		apps[app.Name] = struct{}{}
		if len(app.Resources) == 0 {
			return fmt.Errorf("expected Application %s lists no resources", app.Name)
		}
		resources := make(map[resourceKey]struct{}, len(app.Resources))
		for _, resource := range app.Resources {
			key := resource.key()
			if key.Kind == "" || key.Name == "" {
				return fmt.Errorf("expected Application %s has a resource without kind or name: %#v", app.Name, resource)
			}
			if _, ok := resources[key]; ok {
				return fmt.Errorf("expected Application %s lists %s twice", app.Name, key)
			}
			resources[key] = struct{}{}
			if err := app.validateStatus(resource); err != nil {
				return err
			}
		}
		if app.ComparisonError != nil && (app.ComparisonError.ArgoCDMessage == "" || app.ComparisonError.DrydockMessage == "") {
			return fmt.Errorf("expected Application %s comparisonError must set argocdMessage and drydockMessage", app.Name)
		}
	}
	return nil
}

// validateStatus requires Synced or OutOfSync, or Unknown for an Application
// whose comparison fails: Argo CD then reports every resource as Unknown.
func (app expectedApplication) validateStatus(resource expectedResource) error {
	if app.ComparisonError != nil {
		if resource.Status != statusUnknown {
			return fmt.Errorf("expected Application %s resource %s has status %q, want %s for a comparisonError Application", app.Name, resource.key(), resource.Status, statusUnknown)
		}
		return nil
	}
	if resource.Status != statusSynced && resource.Status != statusOutOfSync {
		return fmt.Errorf("expected Application %s resource %s has status %q, want %s or %s", app.Name, resource.key(), resource.Status, statusSynced, statusOutOfSync)
	}
	return nil
}

func (resource expectedResource) key() resourceKey {
	return resourceKey{Group: resource.Group, Kind: resource.Kind, Name: resource.Name}
}

// applicationStatus is the sync status Argo CD must report for the whole
// Application: Unknown when its comparison fails, and OutOfSync when any
// resource is OutOfSync.
func (app expectedApplication) applicationStatus() string {
	if app.ComparisonError != nil {
		return statusUnknown
	}
	for _, resource := range app.Resources {
		if resource.Status == statusOutOfSync {
			return statusOutOfSync
		}
	}
	return statusSynced
}

func evaluateArgoCD(app expectedApplication, targetRevision, dir string) []string {
	status, err := readApplicationStatusFile(filepath.Join(dir, app.Name+".json"))
	if err != nil {
		return []string{err.Error()}
	}
	if status.Metadata.Name != app.Name || status.Metadata.Namespace != argocdNamespace {
		return []string{fmt.Sprintf("Argo CD capture is Application %s/%s", status.Metadata.Namespace, status.Metadata.Name)}
	}
	if got := status.Status.Sync.ComparedTo.Source.TargetRevision; got != targetRevision {
		return []string{fmt.Sprintf("Argo CD compared targetRevision %q, want %q", got, targetRevision)}
	}
	var failures []string
	if app.ComparisonError != nil {
		failures = append(failures, evaluateComparisonError(app, status.Status.Conditions)...)
	}
	if got, want := status.Status.Sync.Status, app.applicationStatus(); got != want {
		failures = append(failures, fmt.Sprintf("Argo CD Application sync status is %q, want %q", got, want))
	}
	live := make(map[resourceKey]string, len(status.Status.Resources))
	for _, resource := range status.Status.Resources {
		live[resourceKey{Group: resource.Group, Kind: resource.Kind, Name: resource.Name}] = resource.Status
	}
	for _, resource := range app.Resources {
		got, ok := live[resource.key()]
		switch {
		case !ok:
			failures = append(failures, fmt.Sprintf("Argo CD has no resource %s", resource.key()))
		case got != resource.Status:
			failures = append(failures, fmt.Sprintf("Argo CD reports %s as %q, want %q", resource.key(), got, resource.Status))
		}
		delete(live, resource.key())
	}
	for _, key := range sortedResourceKeys(live) {
		failures = append(failures, fmt.Sprintf("Argo CD has resource %s, which the expected file does not list", key))
	}
	return failures
}

func evaluateComparisonError(app expectedApplication, conditions []appCondition) []string {
	var messages []string
	for _, condition := range conditions {
		if condition.Type != comparisonErrorType {
			continue
		}
		if strings.Contains(condition.Message, app.ComparisonError.ArgoCDMessage) {
			return nil
		}
		messages = append(messages, condition.Message)
	}
	if len(messages) == 0 {
		return []string{fmt.Sprintf("Argo CD has no %s condition, want one containing %q", comparisonErrorType, app.ComparisonError.ArgoCDMessage)}
	}
	return []string{fmt.Sprintf("Argo CD %s condition %q does not contain %q", comparisonErrorType, strings.Join(messages, " | "), app.ComparisonError.ArgoCDMessage)}
}

// evaluateDrydockFailure checks the separate drydock diff of a
// comparisonError Application: it must fail with the expected message.
func evaluateDrydockFailure(app expectedApplication, dir string) []string {
	exitData, err := os.ReadFile(filepath.Join(dir, app.Name+".exit"))
	if err != nil {
		return []string{fmt.Sprintf("missing drydock exit code capture: %v", err)}
	}
	stderr, err := os.ReadFile(filepath.Join(dir, app.Name+".stderr"))
	if err != nil {
		return []string{fmt.Sprintf("missing drydock stderr capture: %v", err)}
	}
	var failures []string
	if strings.TrimSpace(string(exitData)) == "0" {
		failures = append(failures, "drydock diffed the Application, want a failure")
	}
	if !strings.Contains(string(stderr), app.ComparisonError.DrydockMessage) {
		failures = append(failures, fmt.Sprintf("drydock stderr does not contain %q", app.ComparisonError.DrydockMessage))
	}
	return failures
}

func readApplicationStatusFile(path string) (appStatusFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return appStatusFile{}, errors.New("missing Argo CD Application JSON capture")
		}
		return appStatusFile{}, fmt.Errorf("read Argo CD Application file %s: %w", path, err)
	}
	var app appStatusFile
	if err := json.Unmarshal(data, &app); err != nil {
		return appStatusFile{}, fmt.Errorf("parse Argo CD Application file %s: %w", path, err)
	}
	return app, nil
}

// readDrydockChanges returns, per Application name, the change drydock
// reports for each resource.
func readDrydockChanges(path string) (map[string]map[resourceKey]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read drydock diff output: %w", err)
	}
	var results []drydockResult
	if err := json.Unmarshal(data, &results); err != nil {
		return nil, fmt.Errorf("parse drydock diff output %s: %w", path, err)
	}
	changes := map[string]map[resourceKey]string{}
	for _, result := range results {
		if result.Parent.Namespace != argocdNamespace {
			return nil, fmt.Errorf("drydock reports a change for Application %s/%s outside the %s namespace", result.Parent.Namespace, result.Parent.Name, argocdNamespace)
		}
		if changes[result.Parent.Name] == nil {
			changes[result.Parent.Name] = map[resourceKey]string{}
		}
		key := resourceKey{Group: result.Resource.Group, Kind: result.Resource.Kind, Name: result.Resource.Name}
		changes[result.Parent.Name][key] = result.Change
	}
	return changes, nil
}

func evaluateDrydock(app expectedApplication, changes map[resourceKey]string) []string {
	remaining := make(map[resourceKey]string, len(changes))
	maps.Copy(remaining, changes)
	var failures []string
	for _, resource := range app.Resources {
		change, changed := remaining[resource.key()]
		delete(remaining, resource.key())
		switch {
		case resource.Status == statusOutOfSync && !changed:
			failures = append(failures, fmt.Sprintf("drydock reports no change for %s, want a change (Argo CD: %s)", resource.key(), statusOutOfSync))
		case resource.Status == statusSynced && changed:
			failures = append(failures, fmt.Sprintf("drydock reports %s %s, want no change (Argo CD: %s)", resource.key(), change, statusSynced))
		case changed && change != "modified":
			failures = append(failures, fmt.Sprintf("drydock reports %s %s, want modified", resource.key(), change))
		}
	}
	for _, key := range sortedResourceKeys(remaining) {
		failures = append(failures, fmt.Sprintf("drydock reports %s %s, which the expected file does not list", key, remaining[key]))
	}
	return failures
}

func sortedNames[V any](values map[string]V) []string {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func sortedResourceKeys[V any](values map[resourceKey]V) []resourceKey {
	keys := make([]resourceKey, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].String() < keys[j].String() })
	return keys
}

func (result comparisonResult) Summary() string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "Applications: %d\n", result.Applications)
	fmt.Fprintf(&builder, "Resources: %d\n", result.Resources)
	fmt.Fprintf(&builder, "Differences: %d\n", result.Differences)
	fmt.Fprintf(&builder, "Failures: %d\n", len(result.Failures))
	for _, line := range result.Lines {
		fmt.Fprintf(&builder, "%s\n", line)
	}
	for _, failure := range result.Failures {
		fmt.Fprintf(&builder, "FAILURE %s\n", failure)
	}
	return builder.String()
}
