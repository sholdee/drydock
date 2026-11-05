package main

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// projectPolicyPhase is one project-policy phase of
// scripts/argocd-parity-smoke.sh: its case array in the script, its expected
// file and the Applications its fixture repo declares.
type projectPolicyPhase struct {
	casesArray   string
	expected     string
	applications string
}

var projectPolicyPhases = []projectPolicyPhase{
	{
		casesArray:   "PROJECT_POLICY_CASES",
		expected:     "../../testdata/argocd-project-policy/expected.yaml",
		applications: "../../testdata/argocd-project-policy/repo/project-policy/applications.yaml",
	},
	{
		casesArray:   "PROJECT_POLICY_IN_CLUSTER_SECRET_CASES",
		expected:     "../../testdata/argocd-project-policy/in-cluster-secret/expected.yaml",
		applications: "../../testdata/argocd-project-policy/in-cluster-secret/repo/project-policy-in-cluster-secret/applications.yaml",
	},
	{
		casesArray:   "PROJECT_POLICY_IN_CLUSTER_DISABLED_CASES",
		expected:     "../../testdata/argocd-project-policy/in-cluster-disabled/expected.yaml",
		applications: "../../testdata/argocd-project-policy/in-cluster-disabled/repo/project-policy-in-cluster-disabled/applications.yaml",
	},
}

// TestProjectPolicyPhasesAgree keeps each phase's three lists in step: the
// harness waits for and captures the script's cases, the comparator reads
// the expected file, and Argo CD reconciles the declared Applications.
func TestProjectPolicyPhasesAgree(t *testing.T) {
	script, err := os.ReadFile("../argocd-parity-smoke.sh")
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	for _, phase := range projectPolicyPhases {
		t.Run(phase.casesArray, func(t *testing.T) {
			scriptCases := shellArray(t, script, phase.casesArray)
			expected, err := readExpected(phase.expected)
			if err != nil {
				t.Fatalf("readExpected() error = %v", err)
			}
			expectedCases := make([]string, 0, len(expected))
			expectedApps := make([]string, 0, len(expected))
			for _, tc := range expected {
				expectedCases = append(expectedCases, tc.Namespace+"|"+tc.Name+"|"+string(tc.ArgoCDCondition))
				expectedApps = append(expectedApps, appKey(tc.Namespace, tc.Name))
			}
			if !slices.Equal(scriptCases, expectedCases) {
				t.Fatalf("%s = %q, want the cases of %s in order: %q", phase.casesArray, scriptCases, phase.expected, expectedCases)
			}
			declared := declaredApplications(t, phase.applications)
			slices.Sort(declared)
			slices.Sort(expectedApps)
			if !slices.Equal(declared, expectedApps) {
				t.Fatalf("Applications in %s = %q, want %q", phase.applications, declared, expectedApps)
			}
		})
	}
}

// shellArray returns the quoted entries of a bash array assignment.
func shellArray(t *testing.T, script []byte, name string) []string {
	t.Helper()
	scanner := bufio.NewScanner(bytes.NewReader(script))
	inArray := false
	var entries []string
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		switch {
		case !inArray && line == name+"=(":
			inArray = true
		case inArray && line == ")":
			return entries
		case inArray && strings.HasPrefix(line, `"`) && strings.HasSuffix(line, `"`):
			entries = append(entries, strings.Trim(line, `"`))
		}
	}
	t.Fatalf("array %s not found in the smoke script", name)
	return nil
}

func declaredApplications(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var apps []string
	for {
		var doc struct {
			Kind     string `yaml:"kind"`
			Metadata struct {
				Name      string `yaml:"name"`
				Namespace string `yaml:"namespace"`
			} `yaml:"metadata"`
		}
		if err := decoder.Decode(&doc); err != nil {
			if errors.Is(err, io.EOF) {
				return apps
			}
			t.Fatalf("decode %s: %v", path, err)
		}
		if doc.Kind == "Application" {
			apps = append(apps, appKey(doc.Metadata.Namespace, doc.Metadata.Name))
		}
	}
}
