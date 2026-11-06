package render

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	pathutil "github.com/argoproj/argo-cd/v3/util/io/path"
	"helm.sh/helm/v4/pkg/strvals"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

// upstreamHelmFileParameterValues is how the Argo CD v3.6.1 repo-server
// and helm handle helm.fileParameters, with the upstream path resolver.
// helmTemplate (reposerver/repository/repository.go:1361-1377) resolves
// every entry in list order. An entry whose first path segment, before env
// substitution, is a key of refSources resolves with getResolvedRefValueFile
// (repository.go:1583-1604, getReferencedSourceName at 1620-1625): the first
// segment is dropped and the rest is env-substituted and resolved in that
// root. Any other entry is env-substituted and resolved with
// pathutil.ResolveValueFilePathOrUrl. The resolved paths are keyed by name,
// so the last entry per name wins. helm then parses each name=<path> with
// strvals after cleanSetParameters (util/helm/cmd.go:450-451) and reads
// the file (helm pkg/cli/values/options.go:94-106). It returns the values
// by name, or the error that the repo-server reports.
func upstreamHelmFileParameterValues(appPath, repoRoot string, env argoappv1.Env, refSources map[string]string, parameters []argoappv1.HelmFileParameter) (map[string]string, error) {
	schemes := []string{"https", "http"}
	setFile := map[string]string{}
	for _, parameter := range parameters {
		refName := ""
		if strings.HasPrefix(parameter.Path, "$") {
			refName = strings.Split(parameter.Path, "/")[0]
		}
		if root := refSources[refName]; refName != "" && root != "" {
			pathStrings := strings.Split(parameter.Path, "/")
			pathStrings[0] = ""
			resolved, _, err := pathutil.ResolveValueFilePathOrUrl(root, root, env.Envsubst(strings.Join(pathStrings, "/")), schemes)
			if err != nil {
				return nil, fmt.Errorf("error resolving set-file path: error resolving value file path: %w", err)
			}
			setFile[parameter.Name] = string(resolved)
			continue
		}
		resolved, _, err := pathutil.ResolveValueFilePathOrUrl(appPath, repoRoot, env.Envsubst(parameter.Path), schemes)
		if err != nil {
			return nil, fmt.Errorf("error resolving helm value file path: %w", err)
		}
		setFile[parameter.Name] = string(resolved)
	}
	values := map[string]any{}
	read := func(input []rune) (any, error) {
		data, err := os.ReadFile(string(input))
		if err != nil {
			return nil, err
		}
		return string(data), nil
	}
	for name, path := range setFile {
		if err := strvals.ParseIntoFile(name+"="+cleanHelmSetParameter(path), values, read); err != nil {
			return nil, fmt.Errorf("failed parsing --set-file data: %w", err)
		}
	}
	out := map[string]string{}
	for name, value := range values {
		out[name] = fmt.Sprint(value)
	}
	return out, nil
}

// writeHelmFileParameterSymlinkRepo writes a repository at root/repo with a
// chart at charts/app and symlinks under links, and a file outside the
// repository at root/outside.txt. It returns the repository root.
func writeHelmFileParameterSymlinkRepo(t *testing.T, root string) string {
	t.Helper()
	repo := filepath.Join(root, "repo")
	chartDir := filepath.Join(repo, "charts", "app")
	writeFile(t, filepath.Join(chartDir, "Chart.yaml"), "apiVersion: v2\nname: probe\nversion: 0.1.0\n")
	writeFile(t, filepath.Join(chartDir, "values.yaml"), "{}\n")
	writeFile(t, filepath.Join(chartDir, "templates", "cm.yaml"), `apiVersion: v1
kind: ConfigMap
metadata:
  name: probe
data:
  values: {{ toJson .Values | quote }}
`)
	writeFile(t, filepath.Join(root, "outside.txt"), "content-outside-the-repository")
	links := filepath.Join(repo, "links")
	writeFile(t, filepath.Join(links, "target.txt"), "target")
	writeFile(t, filepath.Join(links, "real", "inner.txt"), "inner")
	writeFile(t, filepath.Join(links, "real", "which.txt"), "physical")
	writeFile(t, filepath.Join(links, "which.txt"), "lexical")
	writeFile(t, filepath.Join(chartDir, "$values", "x.txt"), "escaped-dollar")
	writeFile(t, filepath.Join(chartDir, "ab.txt"), "without-backslash")
	writeFile(t, filepath.Join(repo, ".git", "config"), "[core]\n")
	if err := os.MkdirAll(filepath.Join(links, "real", "deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	symlink(t, "target.txt", filepath.Join(links, "file-link.txt"))
	symlink(t, "file-link.txt", filepath.Join(links, "chain.txt"))
	symlink(t, "real", filepath.Join(links, "dir-link"))
	symlink(t, "real", filepath.Join(links, "to-dir"))
	symlink(t, "../../outside.txt", filepath.Join(links, "escape.txt"))
	symlink(t, "missing-target.txt", filepath.Join(links, "dangling.txt"))
	symlink(t, "loop.txt", filepath.Join(links, "loop.txt"))
	symlink(t, "..", filepath.Join(links, "root-link"))
	symlink(t, "../..", filepath.Join(links, "escape-dir"))
	// lex-dir is a symlink to a deeper directory. The repo-server joins the
	// target of a symlink under it to lex-dir, not to real/deep.
	symlink(t, "real/deep", filepath.Join(links, "lex-dir"))
	symlink(t, "../which.txt", filepath.Join(links, "real", "deep", "sibling.txt"))
	symlink(t, "../../../links/target.txt", filepath.Join(links, "real", "deep", "up.txt"))
	return repo
}

// TestHelmFileParameterSymlinksMatchArgoCD renders file parameters that
// pass through symlinks with drydock and with the Argo CD v3.6.1
// repo-server's resolution (upstreamHelmFileParameterValues) and requires
// the same values or the same error. The path resolver follows a symlink at
// the path itself, joins its target as text, and checks the result against
// the repository root as text. So a file symlink in the repository works, a
// symlinked directory on the way works, a dangling symlink resolves to its
// missing target, and an overridden entry fails when its path does not
// resolve. Out-of-bounds symlinks are allowed here, as with
// reposerver.allow.oob.symlinks, because the out-of-bounds check is not
// part of this code path.
func TestHelmFileParameterSymlinksMatchArgoCD(t *testing.T) {
	for _, tt := range []struct {
		name       string
		parameters []argoappv1.HelmFileParameter
		// drydockErr is set where drydock fails by design and the
		// repo-server does not.
		drydockErr string
	}{
		{name: "file symlink", parameters: []argoappv1.HelmFileParameter{{Name: "f", Path: "../../links/file-link.txt"}}},
		{name: "symlink chain", parameters: []argoappv1.HelmFileParameter{{Name: "f", Path: "../../links/chain.txt"}}},
		{name: "symlinked parent directory", parameters: []argoappv1.HelmFileParameter{{Name: "f", Path: "../../links/dir-link/inner.txt"}}},
		{name: "absolute path is relative to the repository root", parameters: []argoappv1.HelmFileParameter{{Name: "f", Path: "/links/target.txt"}}},
		{name: "absolute path to a symlink", parameters: []argoappv1.HelmFileParameter{{Name: "f", Path: "/links/file-link.txt"}}},
		{name: "absolute path that leaves the repository", parameters: []argoappv1.HelmFileParameter{{Name: "f", Path: "/../outside.txt"}}},
		{name: "symlink target joined to the symlinked directory", parameters: []argoappv1.HelmFileParameter{{Name: "f", Path: "../../links/lex-dir/sibling.txt"}}},
		{name: "symlink out of the repository", parameters: []argoappv1.HelmFileParameter{{Name: "f", Path: "../../links/escape.txt"}}},
		{name: "overridden symlink out of the repository", parameters: []argoappv1.HelmFileParameter{
			{Name: "f", Path: "../../links/escape.txt"},
			{Name: "f", Path: "../../links/target.txt"},
		}},
		{name: "overridden symlink that leaves the repository as text", parameters: []argoappv1.HelmFileParameter{
			{Name: "f", Path: "../../links/lex-dir/up.txt"},
			{Name: "f", Path: "../../links/target.txt"},
		}},
		{name: "dangling symlink", parameters: []argoappv1.HelmFileParameter{{Name: "f", Path: "../../links/dangling.txt"}}},
		{name: "overridden dangling symlink", parameters: []argoappv1.HelmFileParameter{
			{Name: "f", Path: "../../links/dangling.txt"},
			{Name: "f", Path: "../../links/target.txt"},
		}},
		{name: "overridden symlink loop", parameters: []argoappv1.HelmFileParameter{
			{Name: "f", Path: "../../links/loop.txt"},
			{Name: "f", Path: "../../links/target.txt"},
		}},
		{name: "symlink to the repository root", parameters: []argoappv1.HelmFileParameter{{Name: "f", Path: "../../links/root-link"}}},
		{name: "symlink to a directory", parameters: []argoappv1.HelmFileParameter{{Name: "f", Path: "../../links/to-dir"}}},
		{name: "overridden symlink to a directory", parameters: []argoappv1.HelmFileParameter{
			{Name: "f", Path: "../../links/to-dir"},
			{Name: "f", Path: "../../links/target.txt"},
		}},
		{name: "ref file symlink", parameters: []argoappv1.HelmFileParameter{{Name: "f", Path: "$values/links/file-link.txt"}}},
		{name: "ref symlink out of the ref root", parameters: []argoappv1.HelmFileParameter{{Name: "f", Path: "$values/links/escape.txt"}}},
		{name: "env substitution", parameters: []argoappv1.HelmFileParameter{{Name: "f", Path: "../../links/$ARGOCD_APP_NAME.txt"}}},
		{name: "ref decided before env substitution", parameters: []argoappv1.HelmFileParameter{{Name: "f", Path: "$values/links/$ARGOCD_APP_NAME.txt"}}},
		{name: "escaped dollar is not a ref", parameters: []argoappv1.HelmFileParameter{{Name: "f", Path: "$$values/x.txt"}}},
		{name: "bare ref", parameters: []argoappv1.HelmFileParameter{{Name: "f", Path: "$values"}}},
		{name: "bare ref with a slash", parameters: []argoappv1.HelmFileParameter{{Name: "f", Path: "$values/"}}},
		{name: "helm drops a backslash", parameters: []argoappv1.HelmFileParameter{{Name: "f", Path: `a\b.txt`}}},
		{name: "overridden path in .git", parameters: []argoappv1.HelmFileParameter{
			{Name: "f", Path: "../../.git/config"},
			{Name: "f", Path: "../../links/target.txt"},
		}},
		{name: "path in .git", parameters: []argoappv1.HelmFileParameter{{Name: "f", Path: "../../.git/config"}}, drydockErr: "enters a .git directory"},
		{name: "overridden URL", parameters: []argoappv1.HelmFileParameter{
			{Name: "f", Path: "https://example.invalid/values.txt"},
			{Name: "f", Path: "../../links/file-link.txt"},
		}},
		{
			// The repo-server reads the file outside the repository. Its
			// out-of-bounds check fails the repository first unless
			// reposerver.allow.oob.symlinks is true. drydock never reads
			// outside the repository.
			name:       "symlinked parent directory out of the repository",
			parameters: []argoappv1.HelmFileParameter{{Name: "f", Path: "../../links/escape-dir/outside.txt"}},
			drydockErr: "makes helm read a file outside the repository, which drydock never reads",
		},
		{name: "overridden symlinked parent directory out of the repository", parameters: []argoappv1.HelmFileParameter{
			{Name: "f", Path: "../../links/escape-dir/outside.txt"},
			{Name: "f", Path: "../../links/target.txt"},
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo := writeHelmFileParameterSymlinkRepo(t, t.TempDir())
			refRoots := map[string]string{"$values": repo}
			env := argoappv1.Env{{Name: "ARGOCD_APP_NAME", Value: "file-link"}}
			want, wantErr := upstreamHelmFileParameterValues(filepath.Join(repo, "charts", "app"), repo, env, refRoots, tt.parameters)
			result, _, err := (HelmRenderer{}).Render(context.Background(), ResolvedSource{RepoRoot: repo, Path: "charts/app"}, RenderOptions{
				AppName:            "probe",
				ArgoEnv:            env,
				RefRoots:           refRoots,
				HelmFileParameters: tt.parameters,
			})
			if tt.drydockErr != "" {
				if wantErr != nil {
					t.Fatalf("upstream error = %v, want a render", wantErr)
				}
				if err == nil || !strings.Contains(err.Error(), tt.drydockErr) {
					t.Fatalf("Render() error = %v, want %q", err, tt.drydockErr)
				}
				if strings.Contains(err.Error(), "content-outside-the-repository") {
					t.Fatalf("Render() error = %v leaked file contents", err)
				}
				return
			}
			if wantErr != nil {
				// A read error names the absolute path. drydock reads the
				// real path, which can differ above the repository, and
				// words the start of the message in its own way.
				wantText := wantErr.Error()
				if i := strings.Index(wantText, "/repo/"); strings.HasPrefix(wantText, "failed parsing --set-file data: ") && i >= 0 {
					wantText = wantText[i+1:]
				}
				if err == nil || !strings.Contains(err.Error(), wantText) {
					t.Fatalf("Render() error = %v, want the repo-server's %q", err, wantText)
				}
				return
			}
			if err != nil {
				t.Fatalf("Render() error = %v, want %v", err, want)
			}
			raw, _, _ := unstructured.NestedString(result[0].Object.Object, "data", "values")
			var got map[string]string
			if err := yaml.Unmarshal([]byte(raw), &got); err != nil {
				t.Fatalf("decode values %q: %v", raw, err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("values = %v, want %v", got, want)
			}
		})
	}
}

// TestCollectHelmLocalInputPathsFollowsFileParameterSymlinks pins that the
// persistent render cache digest of a Helm source covers the symlinks that
// a file parameter path passes through and the file that helm reads, after
// helm drops a backslash, and, as optional paths, those of an overridden
// entry whose path passes through a symlink. An overridden path in .git
// adds nothing and does not fail.
func TestCollectHelmLocalInputPathsFollowsFileParameterSymlinks(t *testing.T) {
	repo := writeHelmFileParameterSymlinkRepo(t, t.TempDir())
	got, err := CollectHelmLocalInputPaths(HelmLocalInputOptions{
		RepoRoot: repo,
		Source:   ResolvedSource{Path: "charts/app"},
		Options: RenderOptions{
			RefRoots: map[string]string{"$values": "."},
			HelmFileParameters: []argoappv1.HelmFileParameter{
				{Name: "chain", Path: "../../links/chain.txt"},
				{Name: "inner", Path: "../../links/dir-link/inner.txt"},
				{Name: "lexical", Path: "../../links/lex-dir/sibling.txt"},
				{Name: "ref", Path: "$values/links/file-link.txt"},
				{Name: "absolute", Path: "/links/target.txt"},
				{Name: "backslash", Path: `a\b.txt`},
			},
		},
		OverriddenFileParameters: []argoappv1.HelmFileParameter{
			{Name: "chain", Path: "../../links/dangling.txt"},
			{Name: "inner", Path: "../../links/target.txt"},
			{Name: "absolute", Path: "../../.git/config"},
		},
	})
	if err != nil {
		t.Fatalf("CollectHelmLocalInputPaths() error = %v", err)
	}
	want := []HelmLocalInputPath{
		{Path: "charts/app"},
		{Path: `charts/app/a\b.txt`, Optional: true},
		{Path: "charts/app/ab.txt"},
		{Path: "links/chain.txt"},
		{Path: "links/dangling.txt", Optional: true},
		{Path: "links/dir-link"},
		{Path: "links/file-link.txt"},
		{Path: "links/lex-dir"},
		{Path: "links/missing-target.txt", Optional: true},
		{Path: "links/real/deep/sibling.txt"},
		{Path: "links/real/inner.txt"},
		{Path: "links/real/which.txt", Optional: true},
		{Path: "links/target.txt"},
		{Path: "links/which.txt"},
	}
	sort.Slice(want, func(i, j int) bool { return want[i].Path < want[j].Path })
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("paths =\n%#v\nwant\n%#v", got, want)
	}
}

// TestHelmFileParameterSymlinksSmokeFixtureMatchesUpstream renders the two
// file parameter symlink Applications of the Argo CD render parity smoke
// (testdata/argocd-parity) with drydock and with the repo-server's
// resolution, and pins the values that the smoke compares live.
// parity-helm-file-parameter-symlinks sets its file parameters in the spec,
// next to a $files ref to its own repository.
// parity-helm-file-parameter-symlinks-override sets them only in its
// .argocd-source-<name>.yaml file.
func TestHelmFileParameterSymlinksSmokeFixtureMatchesUpstream(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", "testdata", "argocd-parity", "repo"))
	if err != nil {
		t.Fatal(err)
	}
	const chartPath = "charts/helm-file-parameter-symlinks"
	spec, override, overrideParameters := readHelmFileParameterSymlinkApplications(t, repoRoot, chartPath)
	for _, tt := range []struct {
		name       string
		parameters []argoappv1.HelmFileParameter
		want       map[string]string
	}{
		{
			name:       spec.Name,
			parameters: spec.Spec.Sources[1].Helm.FileParameters,
			want: map[string]string{
				"linked":     "from-symlink-target\n",
				"chain":      "from-symlink-target\n",
				"linkedDir":  "from-linked-dir\n",
				"absolute":   "from-symlink-target\n",
				"lexical":    "lexical\n",
				"ref":        "from-symlink-target\n",
				"overridden": "from-symlink-target\n",
			},
		},
		{
			name:       override.Name,
			parameters: overrideParameters,
			want: map[string]string{
				"chain":      "from-symlink-target\n",
				"linkedDir":  "from-linked-dir\n",
				"overridden": "from-symlink-target\n",
			},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			refRoots := map[string]string{"$files": repoRoot}
			upstream, err := upstreamHelmFileParameterValues(filepath.Join(repoRoot, chartPath), repoRoot, nil, refRoots, tt.parameters)
			if err != nil {
				t.Fatalf("upstream error = %v", err)
			}
			if !reflect.DeepEqual(upstream, tt.want) {
				t.Fatalf("upstream values = %v, want %v", upstream, tt.want)
			}
			result, _, err := (HelmRenderer{}).Render(context.Background(), ResolvedSource{RepoRoot: repoRoot, Path: chartPath}, RenderOptions{
				AppName:            tt.name,
				Namespace:          "parity-helm-file-parameter-symlinks",
				RefRoots:           refRoots,
				HelmFileParameters: tt.parameters,
			})
			if err != nil {
				t.Fatalf("Render() error = %v", err)
			}
			got, _, _ := unstructured.NestedStringMap(result[0].Object.Object, "data")
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("data = %v, want %v", got, tt.want)
			}
		})
	}
}

// readHelmFileParameterSymlinkApplications reads the two file parameter
// symlink Applications of the smoke fixture and the file parameters of the
// override file of the second one.
func readHelmFileParameterSymlinkApplications(t *testing.T, repoRoot, chartPath string) (spec, override argoappv1.Application, overrideParameters []argoappv1.HelmFileParameter) {
	t.Helper()
	read := func(path string, into any) {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := yaml.Unmarshal(data, into); err != nil {
			t.Fatal(err)
		}
	}
	read(filepath.Join(repoRoot, "applications", "helm-file-parameter-symlinks.yaml"), &spec)
	if len(spec.Spec.Sources) != 2 || spec.Spec.Sources[1].Path != chartPath || spec.Spec.Sources[1].Helm == nil {
		t.Fatalf("parity-helm-file-parameter-symlinks sources = %#v", spec.Spec.Sources)
	}
	read(filepath.Join(repoRoot, "applications", "helm-file-parameter-symlinks-override.yaml"), &override)
	if override.Spec.Source == nil || override.Spec.Source.Path != chartPath || override.Spec.Source.Helm != nil {
		t.Fatalf("parity-helm-file-parameter-symlinks-override source = %#v", override.Spec.Source)
	}
	var overrideSource argoappv1.ApplicationSource
	read(filepath.Join(repoRoot, chartPath, ".argocd-source-"+override.Name+".yaml"), &overrideSource)
	if overrideSource.Helm == nil {
		t.Fatal("the override file sets no helm block")
	}
	return spec, override, overrideSource.Helm.FileParameters
}

// TestHelmFileParameterErrorsComeBeforeValueFileReads pins the error order
// of Argo CD v3.6.1. The repo-server resolves the value file paths
// (reposerver/repository/repository.go:1328) and then every file parameter
// path (1361-1377) before it runs helm, and helm reads the value files only
// then. So a file parameter that does not resolve wins over a value file
// that is missing or does not parse.
func TestHelmFileParameterErrorsComeBeforeValueFileReads(t *testing.T) {
	for _, tt := range []struct {
		name  string
		files map[string]string
	}{
		{name: "missing value file"},
		{name: "value file that does not parse", files: map[string]string{"values-extra.yaml": "a: [\n"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo := writeHelmFileParameterSymlinkRepo(t, t.TempDir())
			for name, content := range tt.files {
				writeFile(t, filepath.Join(repo, "charts", "app", name), content)
			}
			_, _, err := (HelmRenderer{}).Render(context.Background(), ResolvedSource{RepoRoot: repo, Path: "charts/app"}, RenderOptions{
				AppName:    "probe",
				ValueFiles: []string{"values-extra.yaml"},
				HelmFileParameters: []argoappv1.HelmFileParameter{
					{Name: "f", Path: "../../../outside.txt"},
				},
			})
			want := "error resolving helm value file path: file '../../../outside.txt' resolved to outside repository root"
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("Render() error = %v, want %q", err, want)
			}
		})
	}
}

// TestHelmValueFileEscapedDollarIsNotARef pins that a value file path that
// starts with "$$" is a local path, not a $ref path. The repo-server
// decides the ref on the raw path (getReferencedSourceName,
// reposerver/repository/repository.go:1620-1625 at v3.6.1), where "$$values"
// names no source, and Env.Envsubst then turns "$$" into "$".
func TestHelmValueFileEscapedDollarIsNotARef(t *testing.T) {
	repo := writeHelmFileParameterSymlinkRepo(t, t.TempDir())
	writeFile(t, filepath.Join(repo, "charts", "app", "$values", "v.yaml"), "fromLocal: escaped-dollar\n")
	writeFile(t, filepath.Join(repo, "v.yaml"), "fromRef: wrong-file\n")
	result, _, err := (HelmRenderer{}).Render(context.Background(), ResolvedSource{RepoRoot: repo, Path: "charts/app"}, RenderOptions{
		AppName:    "probe",
		RefRoots:   map[string]string{"$values": repo},
		ValueFiles: []string{"$$values/v.yaml"},
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	raw, _, _ := unstructured.NestedString(result[0].Object.Object, "data", "values")
	if raw != `{"fromLocal":"escaped-dollar"}` {
		t.Fatalf("values = %s, want the local file", raw)
	}
}
