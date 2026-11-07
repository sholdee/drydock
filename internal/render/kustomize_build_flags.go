package render

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"slices"
	"strings"

	"github.com/sholdee/drydock/internal/diagnostic"

	"sigs.k8s.io/kustomize/api/konfig"
	"sigs.k8s.io/kustomize/api/resmap"
	"sigs.k8s.io/kustomize/api/types"
)

// setKustomizeManagedByLabel sets app.kubernetes.io/managed-by to the
// value that the kustomize binary of Argo CD writes. krusty adds the label
// when --enable-managedby-label is set or the root kustomization lists
// managedByLabel in buildMetadata (Kustomizer.Run, api/krusty/
// kustomizer.go:98-111 at kustomize/api v0.21.3), and it names the version
// that provenance finds in the build information of the program. In
// drydock that is not the kustomize version, so the label is written
// again, on the same resources, with argoCDKustomizeVersion.
func setKustomizeManagedByLabel(resMap resmap.ResMap, root string, settings kustomizeBuildSettings) error {
	enabled := settings.AddManagedbyLabel
	if !enabled {
		kustomizationFile, err := findKustomizationFile(root)
		if err != nil {
			return err
		}
		content, err := os.ReadFile(kustomizationFile)
		if err != nil {
			return err
		}
		kustomization, err := decodeKustomization(kustomizationFile, content)
		if err != nil {
			return err
		}
		enabled = slices.Contains(kustomization.BuildMetadata, types.ManagedByLabelOption)
	}
	if !enabled {
		return nil
	}
	// krusty sets the label with a LabelTransformer on metadata/labels
	// only, with CreateIfNotPresent, so it is on every resource.
	for _, resource := range resMap.Resources() {
		labels := resource.GetLabels()
		labels[konfig.ManagedbyLabelKey] = "kustomize-" + argoCDKustomizeVersion
		if err := resource.SetLabels(labels); err != nil {
			return err
		}
	}
	return nil
}

// kustomizeHelmMissingFlagError runs the checks of
// HelmChartInflationGeneratorPlugin.Config that come before an entry is
// read (api/internal/builtins/HelmChartInflationGenerator.go:50-57 at
// kustomize/api v0.21.3). They are the same for every entry. drydock
// renders helmCharts with the Helm library, so a --helm-command other
// than the default "helm" fails: drydock cannot run that program.
func kustomizeHelmMissingFlagError(settings kustomizeBuildSettings) error {
	const prefix = "trouble configuring builtin HelmChartInflationGenerator"
	if !settings.HelmEnabled {
		return fmt.Errorf("%s: must specify --enable-helm", prefix)
	}
	if settings.HelmCommand == "" {
		return fmt.Errorf("%s: must specify --helm-command", prefix)
	}
	// kustomize runs the program that --helm-command names. A path to a
	// program named helm, such as /usr/local/bin/helm, is helm.
	if path.Base(settings.HelmCommand) != "helm" {
		return fmt.Errorf("kustomize build option --helm-command %q runs a program other than helm; drydock renders helmCharts with its Helm library and cannot run that program", settings.HelmCommand)
	}
	return nil
}

// kustomizeStdoutPaths are the paths that name the standard output of the
// kustomize process in the Linux repo-server of Argo CD.
var kustomizeStdoutPaths = []string{"/dev/stdout", "/dev/fd/1", "/proc/self/fd/1"}

// kustomizeOutputDiscarded reports whether --output sends the build
// somewhere other than the standard output that Argo CD reads. kustomize
// writes the build to the --output path with os.WriteFile, or one file per
// resource when the path is a directory (NewCmdBuild, commands/build/
// build.go at kustomize v5.8.3). A path that names the standard output,
// such as /dev/stdout, gives the usual output. Any other path leaves the
// standard output empty, and Argo CD then reads no manifests and does not
// fail.
//
// Linux opens a path with extra or "." elements, such as /dev//stdout, as
// the cleaned path, so the check cleans the path. A trailing slash is
// different: Linux refuses to create or truncate a file through such a
// path (EISDIR), so kustomize fails with "open /dev/stdout/: is a
// directory".
// drydock fails such a path to the standard output in the same way. For
// another path with a trailing slash, drydock cannot tell whether the
// repo-server has that directory, so it reads the path as a directory and
// renders nothing.
func kustomizeOutputDiscarded(settings kustomizeBuildSettings) (bool, error) {
	if settings.OutputPath == "" {
		return false, nil
	}
	stdout := slices.Contains(kustomizeStdoutPaths, path.Clean(settings.OutputPath))
	if stdout && strings.HasSuffix(settings.OutputPath, "/") {
		return false, fmt.Errorf("open %s: is a directory", settings.OutputPath)
	}
	return !stdout, nil
}

// KustomizeBuildOutputDiscardedCode identifies the warning for a
// kustomize build whose --output sends the build to a file.
const KustomizeBuildOutputDiscardedCode = "kustomize.build-output-discarded"

func kustomizeOutputDiscardedDiagnostic(settings kustomizeBuildSettings) diagnostic.Diagnostic {
	return diagnostic.Diagnostic{
		Code:     KustomizeBuildOutputDiscardedCode,
		Severity: diagnostic.SeverityWarning,
		Category: "kustomize",
		Message:  fmt.Sprintf("kustomize build option --output %q writes the build to a file instead of the standard output, so Argo CD reads no manifests from this source; drydock renders none either", settings.OutputPath),
	}
}

// kustomizeHelmCapabilities is the kube version and the API versions of one
// helmCharts entry.
type kustomizeHelmCapabilities struct {
	KubeVersion string   `json:"kubeVersion,omitempty"`
	APIVersions []string `json:"apiVersions,omitempty"`
}

// configureKustomizeHelmCapabilities sets the kube version and the API
// versions of a helmCharts entry as HelmChartInflationGeneratorPlugin.Config
// does (HelmChartInflationGenerator.go:58-70): it sets the flag values
// first and then decodes the entry over them. A field that the entry sets
// replaces the flag value, and an empty one keeps it.
//
// Config assigns the flag slice of --helm-api-versions itself, without a
// copy, and the JSON decoder writes the apiVersions of the entry into its
// backing array. So the apiVersions of one entry overwrite the first flag
// values, for the entries configured before it and after it that hold the
// same array. This function copies that defect with the same operations:
// settings.HelmAPIVersions is the slice that pflag built, and
// encoding/json decodes into it. Read APIVersions only after every entry
// of the kustomization is configured, as kustomize renders an entry only
// then (KustTarget.runGenerators).
func configureKustomizeHelmCapabilities(settings kustomizeBuildSettings, helmChart types.HelmChart) (*kustomizeHelmCapabilities, error) {
	capabilities := &kustomizeHelmCapabilities{}
	if settings.HelmKubeVersion != "" {
		capabilities.KubeVersion = settings.HelmKubeVersion
	}
	if len(settings.HelmAPIVersions) != 0 {
		capabilities.APIVersions = settings.HelmAPIVersions
	}
	entry, err := json.Marshal(kustomizeHelmCapabilities{
		KubeVersion: helmChart.KubeVersion,
		APIVersions: helmChart.ApiVersions,
	})
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(entry, capabilities); err != nil {
		return nil, err
	}
	return capabilities, nil
}

// configureKustomizeHelmCharts runs HelmChartInflationGeneratorPlugin.Config
// for every helmCharts entry of a kustomization, in order, before any is
// rendered, as KustTarget.runGenerators does: the flag checks, the kube
// version and API versions, and the argument checks.
func configureKustomizeHelmCharts(loader kustomizeValuesFileLoader, settings kustomizeBuildSettings, helmCharts []types.HelmChart) ([]*kustomizeHelmCapabilities, error) {
	if err := kustomizeHelmMissingFlagError(settings); err != nil {
		return nil, err
	}
	capabilities := make([]*kustomizeHelmCapabilities, len(helmCharts))
	for i, helmChart := range helmCharts {
		entryCapabilities, err := configureKustomizeHelmCapabilities(settings, helmChart)
		if err != nil {
			return nil, err
		}
		capabilities[i] = entryCapabilities
		if err := validateKustomizeHelmChartArgs(loader, helmChart); err != nil {
			return nil, err
		}
	}
	return capabilities, nil
}
