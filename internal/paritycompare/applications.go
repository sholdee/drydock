package paritycompare

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/pmezard/go-difflib/difflib"
)

// ApplicationOptions names the two Application inventories to compare.
// ArgoCDFile holds `kubectl get applications.argoproj.io -o json` output (a
// List) taken from the live Argo CD instance. DrydockFile holds
// `drydock get apps -o json` output for the same fixture repository.
type ApplicationOptions struct {
	ArgoCDFile  string
	DrydockFile string
	OutDir      string
}

// ApplicationResult counts what CompareApplications found. Missing counts
// Applications that only Argo CD has, Extra counts Applications that only
// drydock has, and SpecDifferences counts Applications that both sides have
// with a different projected spec.
type ApplicationResult struct {
	Applications    int
	Missing         []string
	Extra           []string
	SpecDifferences []string
	DiffFile        string
}

// Failed reports whether the two inventories disagree.
func (r ApplicationResult) Failed() bool {
	return len(r.Missing) > 0 || len(r.Extra) > 0 || len(r.SpecDifferences) > 0
}

// applicationProjection is the exact shape `drydock get apps -o json`
// prints for each Application. The live Argo CD side is projected into the
// same shape, so the comparison sees the generated name, namespace,
// project, destination, and every source's repoURL, targetRevision, path,
// chart, and ref. The rendered-manifest comparison covers the rest of the
// spec through its effect on the render.
type applicationProjection struct {
	Namespace   string                `json:"namespace"`
	Name        string                `json:"name"`
	Project     string                `json:"project"`
	Destination destinationProjection `json:"destination"`
	Sources     []map[string]string   `json:"sources"`
}

type destinationProjection struct {
	Name      string `json:"name"`
	Server    string `json:"server"`
	Namespace string `json:"namespace"`
}

// CompareApplications checks that drydock discovers and generates exactly the
// Applications that the live Argo CD instance holds, with the same projected
// spec. It writes one unified diff of every mismatch to OutDir.
func CompareApplications(options ApplicationOptions) (ApplicationResult, error) {
	argocd, drydock, err := loadApplicationInventories(options)
	if err != nil {
		return ApplicationResult{}, err
	}

	var result ApplicationResult
	var diff strings.Builder
	keys := unionKeys(argocd, drydock)
	result.Applications = len(keys)
	for _, key := range keys {
		left, inArgoCD := argocd[key]
		right, inDrydock := drydock[key]
		switch {
		case !inDrydock:
			result.Missing = append(result.Missing, key)
		case !inArgoCD:
			result.Extra = append(result.Extra, key)
		case left == right:
			continue
		default:
			result.SpecDifferences = append(result.SpecDifferences, key)
		}
		text, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
			A:        difflib.SplitLines(left),
			B:        difflib.SplitLines(right),
			FromFile: "argocd/" + key,
			ToFile:   "drydock/" + key,
			Context:  20,
		})
		if err != nil {
			return ApplicationResult{}, fmt.Errorf("diff %s: %w", key, err)
		}
		diff.WriteString(text)
	}
	if diff.Len() > 0 {
		result.DiffFile = filepath.Join(options.OutDir, "applications.diff")
		if err := os.WriteFile(result.DiffFile, []byte(diff.String()), 0o644); err != nil {
			return ApplicationResult{}, fmt.Errorf("write applications diff: %w", err)
		}
	}
	return result, nil
}

// loadApplicationInventories validates options, loads both sides, resets
// OutDir, and writes each side's projected inventory there.
func loadApplicationInventories(options ApplicationOptions) (map[string]string, map[string]string, error) {
	if strings.TrimSpace(options.ArgoCDFile) == "" {
		return nil, nil, errors.New("argocd applications file is required")
	}
	if strings.TrimSpace(options.DrydockFile) == "" {
		return nil, nil, errors.New("drydock applications file is required")
	}
	if strings.TrimSpace(options.OutDir) == "" {
		return nil, nil, errors.New("out dir is required")
	}
	argocd, err := loadArgoCDApplications(options.ArgoCDFile)
	if err != nil {
		return nil, nil, fmt.Errorf("load argocd applications: %w", err)
	}
	// Two empty inventories would match. An empty Argo CD side means the
	// capture ran too early or against the wrong cluster, not that both
	// sides agree, so it fails here.
	if len(argocd) == 0 {
		return nil, nil, fmt.Errorf("load argocd applications: %s holds no Applications", options.ArgoCDFile)
	}
	drydock, err := loadDrydockApplications(options.DrydockFile)
	if err != nil {
		return nil, nil, fmt.Errorf("load drydock applications: %w", err)
	}
	if err := os.RemoveAll(options.OutDir); err != nil {
		return nil, nil, fmt.Errorf("clear output dir: %w", err)
	}
	if err := os.MkdirAll(options.OutDir, 0o755); err != nil {
		return nil, nil, fmt.Errorf("create output dir: %w", err)
	}
	for side, apps := range map[string]map[string]string{"argocd": argocd, "drydock": drydock} {
		if err := writeApplicationInventory(options.OutDir, side, apps); err != nil {
			return nil, nil, err
		}
	}
	return argocd, drydock, nil
}

func loadArgoCDApplications(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var list argoappv1.ApplicationList
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	projections := make([]applicationProjection, 0, len(list.Items))
	for _, application := range list.Items {
		projections = append(projections, projectArgoCDApplication(application))
	}
	return indexApplicationProjections(path, projections)
}

func loadDrydockApplications(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	// Unknown fields fail the load: a new field in drydock's projection must
	// be added to the Argo CD side here too, not dropped without notice.
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var projections []applicationProjection
	if err := decoder.Decode(&projections); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	// Decode reads one JSON value. Anything after it means the capture
	// mixed other output into the file, so it fails the load.
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("decode %s: unexpected data after the Application list", path)
	}
	return indexApplicationProjections(path, projections)
}

// projectArgoCDApplication mirrors applicationProjections and sourceSummary
// in internal/cli/get.go: spec.sources wins over spec.source, and path,
// chart, and ref appear only when set.
func projectArgoCDApplication(application argoappv1.Application) applicationProjection {
	sources := application.Spec.Sources
	if len(sources) == 0 && application.Spec.Source != nil {
		sources = argoappv1.ApplicationSources{*application.Spec.Source}
	}
	summaries := make([]map[string]string, 0, len(sources))
	for _, source := range sources {
		summary := map[string]string{
			"repoURL":        source.RepoURL,
			"targetRevision": source.TargetRevision,
		}
		if source.Path != "" {
			summary["path"] = source.Path
		}
		if source.Chart != "" {
			summary["chart"] = source.Chart
		}
		if source.Ref != "" {
			summary["ref"] = source.Ref
		}
		summaries = append(summaries, summary)
	}
	return applicationProjection{
		Namespace: application.Namespace,
		Name:      application.Name,
		Project:   application.Spec.Project,
		Destination: destinationProjection{
			Name:      application.Spec.Destination.Name,
			Server:    application.Spec.Destination.Server,
			Namespace: application.Spec.Destination.Namespace,
		},
		Sources: summaries,
	}
}

func indexApplicationProjections(path string, projections []applicationProjection) (map[string]string, error) {
	out := make(map[string]string, len(projections))
	for _, projection := range projections {
		if projection.Name == "" {
			return nil, fmt.Errorf("%s: Application with an empty name", path)
		}
		if projection.Sources == nil {
			projection.Sources = []map[string]string{}
		}
		key := projection.Namespace + "/" + projection.Name
		if _, duplicate := out[key]; duplicate {
			return nil, fmt.Errorf("%s: duplicate Application %s", path, key)
		}
		body, err := json.MarshalIndent(projection, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("%s: encode %s: %w", path, key, err)
		}
		out[key] = string(body) + "\n"
	}
	return out, nil
}

func writeApplicationInventory(root, side string, apps map[string]string) error {
	var body strings.Builder
	for _, key := range unionKeys(apps, nil) {
		body.WriteString(apps[key])
	}
	path := filepath.Join(root, side+"-applications.json")
	if err := os.WriteFile(path, []byte(body.String()), 0o644); err != nil {
		return fmt.Errorf("write %s applications: %w", side, err)
	}
	return nil
}
