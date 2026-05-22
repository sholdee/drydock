package render

import (
	"bytes"
	"context"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/home-operations/argocd-local/internal/diagnostic"
	"github.com/home-operations/argocd-local/internal/manifest"
	"helm.sh/helm/v4/pkg/chart"
	"helm.sh/helm/v4/pkg/chart/common"
	chartutil "helm.sh/helm/v4/pkg/chart/common/util"
	"helm.sh/helm/v4/pkg/chart/loader"
	"helm.sh/helm/v4/pkg/engine"
)

type HelmRenderer struct{}

func (HelmRenderer) Render(ctx context.Context, source ResolvedSource, opts RenderOptions) ([]Manifest, []diagnostic.Diagnostic, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if len(opts.ValueFiles) != 0 {
		return nil, nil, fmt.Errorf("helm value files are not supported yet")
	}

	chartPath, err := sourceRoot(source)
	if err != nil {
		return nil, nil, err
	}
	manifestPath, err := relativeManifestPath(source.RepoRoot, chartPath)
	if err != nil {
		return nil, nil, err
	}

	chart, err := loader.Load(chartPath)
	if err != nil {
		return nil, nil, fmt.Errorf("load helm chart %s: %w", manifestPath, err)
	}

	releaseName := opts.ReleaseName
	if releaseName == "" {
		releaseName = opts.AppName
	}

	capabilities := common.DefaultCapabilities.Copy()
	if opts.KubeVersion != "" {
		kubeVersion, err := common.ParseKubeVersion(opts.KubeVersion)
		if err != nil {
			return nil, nil, fmt.Errorf("parse helm kube version %q: %w", opts.KubeVersion, err)
		}
		capabilities.KubeVersion = *kubeVersion
	}
	capabilities.APIVersions = append(capabilities.APIVersions, opts.APIVersions...)

	values, err := chartutil.ToRenderValuesWithSchemaValidation(chart, cloneValues(opts.ValuesObject), common.ReleaseOptions{
		Name:      releaseName,
		Namespace: opts.Namespace,
		Revision:  1,
		IsInstall: true,
		IsUpgrade: false,
	}, capabilities, false)
	if err != nil {
		return nil, nil, fmt.Errorf("helm render values %s: %w", manifestPath, err)
	}

	rendered, err := engine.Render(chart, values)
	if err != nil {
		return nil, nil, fmt.Errorf("helm template %s: %w", manifestPath, err)
	}

	docs, err := manifest.DecodeDocuments(manifestPath, bytes.NewReader(helmManifestBytes(chart, rendered)))
	if err != nil {
		return nil, nil, err
	}

	out := make([]Manifest, 0, len(docs))
	for _, doc := range docs {
		out = append(out, Manifest{
			Path:   doc.Path,
			Object: doc.Object,
		})
	}
	return out, nil, nil
}

type helmCRDProvider interface {
	CRDs() []*common.File
}

func helmManifestBytes(chrt chart.Charter, rendered map[string]string) []byte {
	var out bytes.Buffer
	if provider, ok := chrt.(helmCRDProvider); ok {
		for _, crd := range provider.CRDs() {
			writeHelmDocument(&out, string(crd.Data))
		}
	}

	names := make([]string, 0, len(rendered))
	for name := range rendered {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if path.Base(name) == "NOTES.txt" {
			continue
		}
		writeHelmDocument(&out, rendered[name])
	}
	return out.Bytes()
}

func writeHelmDocument(out *bytes.Buffer, document string) {
	if strings.TrimSpace(document) == "" {
		return
	}
	if out.Len() > 0 {
		out.WriteString("\n---\n")
	}
	out.WriteString(document)
	if !strings.HasSuffix(document, "\n") {
		out.WriteByte('\n')
	}
}

func cloneValues(values map[string]any) map[string]any {
	out := make(map[string]any, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}
