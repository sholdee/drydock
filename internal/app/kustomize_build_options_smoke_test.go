package app

import (
	"bufio"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sourcepkg "github.com/sholdee/drydock/internal/source"
	"sigs.k8s.io/yaml"
)

// kustomizeBuildOptionsSmokeCase is one line of the expected.psv file of a
// kustomize build options smoke phase: "render|app|present|absent" or
// "fail|app|argocdMessage|drydockMessage".
type kustomizeBuildOptionsSmokeCase struct {
	kind, app, first, second string
}

func readKustomizeBuildOptionsSmokeCases(t *testing.T, path string) []kustomizeBuildOptionsSmokeCase {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	var cases []kustomizeBuildOptionsSmokeCase
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "|")
		if len(fields) != 4 || (fields[0] != "render" && fields[0] != "fail") {
			t.Fatalf("%s: line %q is not kind|app|text|text", path, line)
		}
		cases = append(cases, kustomizeBuildOptionsSmokeCase{kind: fields[0], app: fields[1], first: fields[2], second: fields[3]})
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	return cases
}

// The phases of the kustomize build options smoke render offline as the
// smoke expects them to render in Argo CD: each render Application holds
// its marker, and each failing Application fails with its drydock message.
func TestKustomizeBuildOptionsSmokeFixture(t *testing.T) {
	const repoURL = "git://argocd-parity-git.argocd-parity.svc.cluster.local/repo.git"
	base := filepath.Join("..", "..", "testdata", "argocd-parity", "kustomize-build-options")
	data := map[string]map[string]any{}
	for _, phase := range []string{"alpha", "no-helm", "unknown-flag", "output-file"} {
		t.Run(phase, func(t *testing.T) {
			root := filepath.Join(base, phase, "repo")
			cases := readKustomizeBuildOptionsSmokeCases(t, filepath.Join(base, phase, "expected.psv"))
			result, err := Orchestrator{}.Build(context.Background(), BuildRequest{
				Path:     root,
				RepoMaps: []sourcepkg.RepoMap{{URL: repoURL, Path: root}},
				Offline:  true,
			})
			rendered := map[string]string{}
			for _, item := range result.ApplicationManifests {
				text, marshalErr := yaml.Marshal(item.Manifest.Object.Object)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				rendered[item.Application.Name] += string(text)
				if values, ok := item.Manifest.Object.Object["data"].(map[string]any); ok {
					data[item.Manifest.Object.GetName()] = values
				}
			}
			for _, tc := range cases {
				checkKustomizeBuildOptionsSmokeCase(t, tc, rendered, err)
			}
		})
	}
	// Without --enable-helm, Argo CD adds no kube or API versions: the
	// kube version comes from --helm-kube-version in kustomize.buildOptions,
	// and the apiVersions of the Application are not used. The first entry
	// of a kustomization writes its apiVersions over the flag values that
	// the second entry sees.
	for name, want := range map[string]map[string]any{
		"parity-kbuild-alpha-helm":         {"kubeVersion": "v1.29.0", "hasBuildOptionsAPI": "true", "hasApplicationAPI": "false", "hasEntryAPI": "false"},
		"parity-kbuild-alpha-shared-own":   {"kubeVersion": "v1.29.0", "hasBuildOptionsAPI": "false", "hasEntryAPI": "true"},
		"parity-kbuild-alpha-shared-flags": {"kubeVersion": "v1.29.0", "hasBuildOptionsAPI": "false", "hasEntryAPI": "true"},
		// Without --enable-exec, kustomize skips the KSOPS exec function, so
		// only the ConfigMap of the source renders.
		"parity-kbuild-alpha-ksops": {"plain": "true"},
	} {
		for key, value := range want {
			if data[name][key] != value {
				t.Errorf("%s data = %v, want %s=%v", name, data[name], key, value)
			}
		}
	}
}

func checkKustomizeBuildOptionsSmokeCase(t *testing.T, tc kustomizeBuildOptionsSmokeCase, rendered map[string]string, buildErr error) {
	t.Helper()
	text, ok := rendered[tc.app]
	if tc.kind == "fail" {
		if ok {
			t.Errorf("%s rendered, want it to fail", tc.app)
		}
		if buildErr == nil || !strings.Contains(buildErr.Error(), tc.app) || !strings.Contains(buildErr.Error(), tc.second) {
			t.Errorf("Build() error = %v, want %s to fail with %q", buildErr, tc.app, tc.second)
		}
		return
	}
	if buildErr != nil && strings.Contains(buildErr.Error(), tc.app) {
		t.Errorf("Build() error = %v, want %s to render", buildErr, tc.app)
		return
	}
	// An empty present text allows an empty render.
	if !ok && tc.first != "" {
		t.Errorf("%s did not render; Build() error = %v", tc.app, buildErr)
		return
	}
	if !strings.Contains(text, tc.first) {
		t.Errorf("%s renders\n%s\nwant %q", tc.app, text, tc.first)
	}
	if tc.second != "" && strings.Contains(text, tc.second) {
		t.Errorf("%s renders\n%s\nwant no %q", tc.app, text, tc.second)
	}
}
