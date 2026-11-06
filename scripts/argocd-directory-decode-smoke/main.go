// Command argocd-directory-decode-smoke checks the directory-source decode
// failures in the Argo CD render parity smoke.
//
// Each case is one Application whose directory holds a file that fails the
// source. Argo CD must report a ComparisonError condition that names the
// file, and drydock must fail to render the same Application with an error
// that names the same file. Files that both sides skip are not checked here:
// they live in an Application that renders, and the main manifest comparison
// covers it.
//
// A manifest without a kind fails later, in the Argo CD controller. Its
// ComparisonError quotes the object instead of naming the file, so such a
// case sets argocdQuotesObject and its argocdMessage quotes the start of that
// object. Tracking metadata that cannot be set fails in the repo-server with
// a message that names the object instead of the file, so such a case sets
// argocdNamesObject to that object's name.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// comparisonErrorType is the Application condition type the Argo CD
// controller sets when manifest generation fails (controller/state.go at
// v3.6.1).
const comparisonErrorType = "ComparisonError"

// missingKindPrefix starts the error the Argo CD controller reports when a
// generated manifest has no kind. v1alpha1.UnmarshalToUnstructured
// (pkg/apis/application/v1alpha1/types.go:4154-4164 at v3.6.1) returns the
// apimachinery missing-kind error, which quotes the whole manifest.
const missingKindPrefix = "Object 'Kind' is missing in '{"

type options struct {
	Expected     string
	Namespace    string
	ArgoCDAppDir string
	DrydockDir   string
	Out          string
	List         bool
}

type expectedFile struct {
	Cases []expectedCase `yaml:"cases"`
}

type expectedCase struct {
	Name           string `yaml:"name"`
	File           string `yaml:"file"`
	ArgoCDMessage  string `yaml:"argocdMessage"`
	DrydockMessage string `yaml:"drydockMessage"`
	// ArgoCDQuotesObject marks a case whose Argo CD message quotes the
	// manifest without a kind instead of naming the file.
	ArgoCDQuotesObject bool `yaml:"argocdQuotesObject"`
	// ArgoCDNamesObject is the name of the object in the failing file that
	// the Argo CD message names instead of the file.
	ArgoCDNamesObject string `yaml:"argocdNamesObject"`
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

// drydockResult is one captured `drydock build app` run.
type drydockResult struct {
	ExitCode int
	Stderr   string
}

type comparisonResult struct {
	Cases    int
	Failures []string
	Lines    []string
}

func main() {
	var opts options
	flag.StringVar(&opts.Expected, "expected", "", "YAML file that lists the expected decode failures")
	flag.StringVar(&opts.Namespace, "namespace", "", "namespace that holds the decode failure Applications")
	flag.StringVar(&opts.ArgoCDAppDir, "argocd-app-dir", "", "directory of captured Argo CD Application JSON files, one <name>.json per case")
	flag.StringVar(&opts.DrydockDir, "drydock-dir", "", "directory of drydock results, one <name>.exit and <name>.stderr per case")
	flag.StringVar(&opts.Out, "out", "", "optional path for a text summary")
	flag.BoolVar(&opts.List, "list", false, "print the expected Application names, one per line, and exit")
	flag.Parse()

	if opts.List {
		cases, err := readExpected(opts.Expected)
		if err != nil {
			fmt.Fprintf(os.Stderr, "argocd directory decode smoke: %v\n", err)
			os.Exit(2)
		}
		for _, tc := range cases {
			fmt.Println(tc.Name)
		}
		return
	}

	result, err := compare(opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "argocd directory decode smoke: %v\n", err)
		os.Exit(2)
	}
	summary := result.Summary()
	if opts.Out != "" {
		if err := os.WriteFile(opts.Out, []byte(summary), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "argocd directory decode smoke: write summary: %v\n", err)
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
	cases, err := readExpected(opts.Expected)
	if err != nil {
		return comparisonResult{}, err
	}
	result := comparisonResult{Cases: len(cases)}
	for _, tc := range cases {
		key := opts.Namespace + "/" + tc.Name
		failures := evaluateArgoCD(tc, opts.Namespace, opts.ArgoCDAppDir)
		failures = append(failures, evaluateDrydock(tc, opts.DrydockDir)...)
		if len(failures) == 0 {
			result.Lines = append(result.Lines, "ok "+key)
			continue
		}
		result.Lines = append(result.Lines, "FAIL "+key)
		for _, failure := range failures {
			result.Failures = append(result.Failures, key+": "+failure)
		}
	}
	return result, nil
}

func (opts options) validate() error {
	var missing []string
	for _, flagValue := range []struct{ name, value string }{
		{"--expected", opts.Expected},
		{"--namespace", opts.Namespace},
		{"--argocd-app-dir", opts.ArgoCDAppDir},
		{"--drydock-dir", opts.DrydockDir},
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

func readExpected(path string) ([]expectedCase, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("missing required flag: --expected")
	}
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
		if strings.TrimSpace(tc.Name) == "" {
			return fmt.Errorf("expected case must include name: %#v", tc)
		}
		if _, ok := seen[tc.Name]; ok {
			return fmt.Errorf("duplicate expected case: %s", tc.Name)
		}
		seen[tc.Name] = struct{}{}
		if strings.TrimSpace(tc.File) == "" {
			return fmt.Errorf("expected case %s must name the failing file", tc.Name)
		}
		// Each side's message must name the file, so a failure caused by
		// some other file cannot pass the case. The Argo CD message for a
		// manifest without a kind names no file, so it must quote part of
		// the object instead. The Argo CD message for tracking metadata that
		// cannot be set names the object instead.
		switch {
		case tc.ArgoCDQuotesObject && tc.ArgoCDNamesObject != "":
			return fmt.Errorf("expected case %s must not set both argocdQuotesObject and argocdNamesObject", tc.Name)
		case tc.ArgoCDNamesObject != "":
			if !strings.Contains(tc.ArgoCDMessage, tc.ArgoCDNamesObject) {
				return fmt.Errorf("expected case %s argocdMessage must contain the object name %q", tc.Name, tc.ArgoCDNamesObject)
			}
		case tc.ArgoCDQuotesObject:
			if !strings.HasPrefix(tc.ArgoCDMessage, missingKindPrefix) || len(tc.ArgoCDMessage) == len(missingKindPrefix) {
				return fmt.Errorf("expected case %s argocdMessage must start with %q and quote part of the object", tc.Name, missingKindPrefix)
			}
		case !strings.Contains(tc.ArgoCDMessage, tc.File):
			return fmt.Errorf("expected case %s argocdMessage must contain the file name %q", tc.Name, tc.File)
		}
		if !strings.Contains(tc.DrydockMessage, tc.File) {
			return fmt.Errorf("expected case %s drydockMessage must contain the file name %q", tc.Name, tc.File)
		}
	}
	return nil
}

func evaluateArgoCD(tc expectedCase, namespace, dir string) []string {
	app, err := readApplicationStatusFile(filepath.Join(dir, tc.Name+".json"))
	if err != nil {
		return []string{err.Error()}
	}
	if app.Metadata.Name != tc.Name || app.Metadata.Namespace != namespace {
		return []string{fmt.Sprintf("Argo CD capture is Application %s/%s", app.Metadata.Namespace, app.Metadata.Name)}
	}
	var messages []string
	for _, condition := range app.Status.Conditions {
		if condition.Type != comparisonErrorType {
			continue
		}
		if strings.Contains(condition.Message, tc.ArgoCDMessage) {
			return nil
		}
		messages = append(messages, condition.Message)
	}
	if len(messages) == 0 {
		return []string{fmt.Sprintf("Argo CD has no %s condition, want one containing %q", comparisonErrorType, tc.ArgoCDMessage)}
	}
	return []string{fmt.Sprintf("Argo CD %s condition %q does not contain %q", comparisonErrorType, strings.Join(messages, " | "), tc.ArgoCDMessage)}
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

func evaluateDrydock(tc expectedCase, dir string) []string {
	result, err := readDrydockResult(dir, tc.Name)
	if err != nil {
		return []string{err.Error()}
	}
	var failures []string
	if result.ExitCode == 0 {
		failures = append(failures, "drydock rendered the Application, want a render failure")
	}
	if !strings.Contains(result.Stderr, tc.DrydockMessage) {
		failures = append(failures, fmt.Sprintf("drydock stderr does not contain %q", tc.DrydockMessage))
	}
	return failures
}

func readDrydockResult(dir, name string) (drydockResult, error) {
	exitData, err := os.ReadFile(filepath.Join(dir, name+".exit"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return drydockResult{}, errors.New("missing drydock exit code capture")
		}
		return drydockResult{}, fmt.Errorf("read drydock exit code for %s: %w", name, err)
	}
	exitCode, err := strconv.Atoi(strings.TrimSpace(string(exitData)))
	if err != nil {
		return drydockResult{}, fmt.Errorf("parse drydock exit code for %s: %w", name, err)
	}
	stderr, err := os.ReadFile(filepath.Join(dir, name+".stderr"))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return drydockResult{}, errors.New("missing drydock stderr capture")
		}
		return drydockResult{}, fmt.Errorf("read drydock stderr for %s: %w", name, err)
	}
	return drydockResult{ExitCode: exitCode, Stderr: string(stderr)}, nil
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
