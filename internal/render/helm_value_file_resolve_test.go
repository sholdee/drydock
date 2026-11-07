package render

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	pathutil "github.com/argoproj/argo-cd/v3/util/io/path"
	"github.com/bmatcuk/doublestar/v4"
	chartv2loader "helm.sh/helm/v4/pkg/chart/v2/loader"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

// upstreamResolvedValueFiles is getResolvedValueFiles
// (reposerver/repository/repository.go:1468-1581 at v3.6.1), copied with
// the ref sources given as a map of ref name to root, which stands for
// getResolvedRefValueFile and gitRepoPaths (repository.go:1583-1604).
//
//nolint:gocyclo // A copy of upstream code, kept close to the original.
func upstreamResolvedValueFiles(appPath, repoRoot string, env argoappv1.Env, allowedValueFilesSchemas, rawValueFiles []string, refSources map[string]string, ignoreMissingValueFiles bool) ([]pathutil.ResolvedFilePath, error) {
	resolveRef := func(rawValueFile, repoPath string) (pathutil.ResolvedFilePath, error) {
		pathStrings := strings.Split(rawValueFile, "/")
		pathStrings[0] = ""
		substitutedPath := strings.Join(pathStrings, "/")
		resolvedPath, _, err := pathutil.ResolveValueFilePathOrUrl(repoPath, repoPath, env.Envsubst(substitutedPath), allowedValueFilesSchemas)
		if err != nil {
			return "", fmt.Errorf("error resolving value file path: %w", err)
		}
		return resolvedPath, nil
	}
	referencedSource := func(rawValueFile string) string {
		if !strings.HasPrefix(rawValueFile, "$") {
			return ""
		}
		return refSources[strings.Split(rawValueFile, "/")[0]]
	}
	isGlobPath := func(path string) bool {
		return strings.ContainsAny(path, "*?[")
	}
	verifyGlobMatchesWithinRoot := func(matches []string, effectiveRoot string) error {
		absRoot, err := filepath.Abs(effectiveRoot)
		if err != nil {
			return fmt.Errorf("error resolving repo root: %w", err)
		}
		canonicalRoot, err := filepath.EvalSymlinks(absRoot)
		if err != nil {
			return fmt.Errorf("error resolving symlinks in repo root: %w", err)
		}
		requiredRootPath := canonicalRoot
		if !strings.HasSuffix(requiredRootPath, string(os.PathSeparator)) {
			requiredRootPath += string(os.PathSeparator)
		}
		for _, match := range matches {
			realMatch, err := filepath.EvalSymlinks(match)
			if err != nil {
				return fmt.Errorf("error resolving symlink for glob match %q: %w", match, err)
			}
			if realMatch+string(os.PathSeparator) != requiredRootPath && !strings.HasPrefix(realMatch, requiredRootPath) {
				return fmt.Errorf("glob match %q resolved to outside repository root", match)
			}
		}
		return nil
	}

	explicitPaths := make(map[pathutil.ResolvedFilePath]struct{})
	for _, rawValueFile := range rawValueFiles {
		var resolved pathutil.ResolvedFilePath
		var err error
		if root := referencedSource(rawValueFile); root != "" {
			resolved, err = resolveRef(rawValueFile, root)
		} else {
			resolved, _, err = pathutil.ResolveValueFilePathOrUrl(appPath, repoRoot, env.Envsubst(rawValueFile), allowedValueFilesSchemas)
		}
		if err != nil {
			continue
		}
		if !isGlobPath(string(resolved)) {
			explicitPaths[resolved] = struct{}{}
		}
	}

	var resolvedValueFiles []pathutil.ResolvedFilePath
	seen := make(map[pathutil.ResolvedFilePath]struct{})
	appendUnique := func(p pathutil.ResolvedFilePath) {
		if _, ok := seen[p]; !ok {
			seen[p] = struct{}{}
			resolvedValueFiles = append(resolvedValueFiles, p)
		}
	}
	for _, rawValueFile := range rawValueFiles {
		isRemote := false
		var resolvedPath pathutil.ResolvedFilePath
		var err error
		effectiveRoot := repoRoot
		if root := referencedSource(rawValueFile); root != "" {
			resolvedPath, err = resolveRef(rawValueFile, root)
			if err != nil {
				return nil, fmt.Errorf("error resolving value file path: %w", err)
			}
			effectiveRoot = root
		} else {
			resolvedPath, isRemote, err = pathutil.ResolveValueFilePathOrUrl(appPath, repoRoot, env.Envsubst(rawValueFile), allowedValueFilesSchemas)
			if err != nil {
				return nil, fmt.Errorf("error resolving value file path: %w", err)
			}
		}
		if !isRemote && isGlobPath(string(resolvedPath)) {
			matches, err := doublestar.FilepathGlob(string(resolvedPath))
			if err != nil {
				return nil, fmt.Errorf("error expanding glob pattern %q: %w", rawValueFile, err)
			}
			if len(matches) == 0 {
				if ignoreMissingValueFiles {
					continue
				}
				return nil, fmt.Errorf("values file glob %q matched no files", rawValueFile)
			}
			if err := verifyGlobMatchesWithinRoot(matches, effectiveRoot); err != nil {
				return nil, fmt.Errorf("glob pattern %q: %w", rawValueFile, err)
			}
			for _, match := range matches {
				if _, isExplicit := explicitPaths[pathutil.ResolvedFilePath(match)]; !isExplicit {
					appendUnique(pathutil.ResolvedFilePath(match))
				}
			}
			continue
		}
		if !isRemote {
			_, err = os.Stat(string(resolvedPath))
			if os.IsNotExist(err) && ignoreMissingValueFiles {
				continue
			}
		}
		appendUnique(resolvedPath)
	}
	return resolvedValueFiles, nil
}

// upstreamHelmValueFileValues resolves the value files with the repo-server
// (upstreamResolvedValueFiles, wrapped as helmTemplate wraps it at
// repository.go:1328-1330) and reads them as helm --values does
// (pkg/cli/values/options.go:47-62 at helm v4.3.1).
func upstreamHelmValueFileValues(appPath, repoRoot string, env argoappv1.Env, refSources map[string]string, files []string, ignoreMissing bool) (map[string]any, error) {
	resolved, err := upstreamResolvedValueFiles(appPath, repoRoot, env, []string{"https", "http"}, files, refSources, ignoreMissing)
	if err != nil {
		return nil, fmt.Errorf("error resolving helm value files: %w", err)
	}
	base := map[string]any{}
	for _, file := range resolved {
		data, err := os.ReadFile(string(file))
		if err != nil {
			return nil, err
		}
		values, err := chartv2loader.LoadValues(strings.NewReader(string(data)))
		if err != nil {
			return nil, fmt.Errorf("failed to parse %s: %w", file, err)
		}
		base = chartv2loader.MergeMaps(base, values)
	}
	return base, nil
}

// writeHelmValueFileSymlinkRepo writes a repository at root/repo with a
// chart at charts/app, value files with symlinks, and a value file outside
// the repository at root/outside.yaml. It returns the repository root.
func writeHelmValueFileSymlinkRepo(t *testing.T, root string) string {
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
	writeFile(t, filepath.Join(repo, "v", "a.yaml"), "winner: a\na: a\n")
	writeFile(t, filepath.Join(repo, "v", "b.yaml"), "winner: b\nb: b\n")
	writeFile(t, filepath.Join(repo, "v", "c.yaml"), "winner: c\nc: c\n")
	writeFile(t, filepath.Join(chartDir, "$values", "x.yaml"), "winner: escaped-dollar\n")
	writeFile(t, filepath.Join(repo, "vals", "abs.yaml"), "winner: abs\n")
	writeFile(t, filepath.Join(repo, "vals", "real", "inner.yaml"), "winner: inner\n")
	writeFile(t, filepath.Join(root, "outside.yaml"), "winner: content-outside-the-repository\n")
	writeFile(t, filepath.Join(repo, ".git", "config.yaml"), "winner: git\n")
	symlink(t, "a.yaml", filepath.Join(repo, "v", "link-a.yaml"))
	symlink(t, "../../vals/abs.yaml", filepath.Join(repo, "vals", "glob", "link-abs.yaml"))
	writeFile(t, filepath.Join(repo, "vals", "glob", "plain.yaml"), "winner: plain\n")
	symlink(t, "real", filepath.Join(repo, "vals", "dir-link"))
	symlink(t, "../../outside.yaml", filepath.Join(repo, "vals", "escape.yaml"))
	symlink(t, "../../../outside.yaml", filepath.Join(repo, "vals", "oob", "escape.yaml"))
	symlink(t, "../..", filepath.Join(repo, "vals", "escape-dir"))
	symlink(t, "missing.yaml", filepath.Join(repo, "vals", "dangling.yaml"))
	symlink(t, "loop.yaml", filepath.Join(repo, "vals", "loop.yaml"))
	return repo
}

// TestHelmValueFilesMatchArgoCD renders value files with drydock and with
// the Argo CD v3.6.1 repo-server (upstreamHelmValueFileValues) and requires
// the same values or the same error. Each resolved path is read once, at
// its first place, a path that starts with "/" is relative to the
// repository root, a symlink at the path or on the way is followed, a glob
// match must stay in the repository after its symlinks resolve, and a bare
// $ref resolves to the ref root and fails.
func TestHelmValueFilesMatchArgoCD(t *testing.T) {
	for _, tt := range []struct {
		name          string
		files         []string
		ignoreMissing bool
		// wantErr is the text both sides must report, set where the
		// repo-server's error names an absolute path in its checkout.
		wantErr string
		// drydockErr is set where drydock fails by design and the
		// repo-server does not.
		drydockErr string
	}{
		{name: "repeated file is read once at its first place", files: []string{"../../v/a.yaml", "../../v/b.yaml", "../../v/a.yaml"}},
		{name: "repeated file through a symlink is read once", files: []string{"../../v/a.yaml", "../../v/b.yaml", "../../v/link-a.yaml"}},
		{name: "symlink to a file in the repository", files: []string{"../../v/link-a.yaml", "../../v/b.yaml"}},
		{name: "symlinked directory on the way", files: []string{"../../vals/dir-link/inner.yaml"}},
		{name: "absolute path is relative to the repository root", files: []string{"/vals/abs.yaml"}},
		{name: "absolute path through a symlinked directory", files: []string{"/vals/dir-link/inner.yaml"}},
		{name: "absolute path that leaves the repository", files: []string{"/../outside.yaml"}},
		{name: "env substitution to an absolute path", files: []string{"$NOPE/vals/abs.yaml"}},
		{name: "escaped dollar is not a ref", files: []string{"$$values/x.yaml"}},
		{name: "symlink out of the repository", files: []string{"/vals/escape.yaml"}},
		{name: "dangling symlink", files: []string{"/vals/dangling.yaml"}, wantErr: "no such file or directory"},
		{name: "dangling symlink with ignoreMissingValueFiles", files: []string{"/vals/dangling.yaml", "../../v/a.yaml"}, ignoreMissing: true},
		{name: "symlink loop", files: []string{"/vals/loop.yaml"}},
		{name: "glob keeps explicit files at their place", files: []string{"../../v/*.yaml", "../../v/a.yaml"}},
		{name: "glob after an explicit file", files: []string{"../../v/c.yaml", "../../v/*.yaml"}},
		{name: "two globs read a file once", files: []string{"../../v/[ab].yaml", "../../v/*.yaml"}},
		{name: "glob matches a symlink in the repository", files: []string{"/vals/glob/*.yaml"}},
		{name: "glob explicit symlink target", files: []string{"/vals/glob/*.yaml", "/vals/abs.yaml"}},
		{name: "glob matches a symlink out of the repository", files: []string{"/vals/oob/*.yaml"}, wantErr: `glob pattern "/vals/oob/*.yaml": glob match`},
		{name: "glob that matches nothing", files: []string{"/vals/none/*.yaml"}},
		{name: "glob that matches nothing with ignoreMissingValueFiles", files: []string{"/vals/none/*.yaml", "../../v/a.yaml"}, ignoreMissing: true},
		{name: "missing file with ignoreMissingValueFiles", files: []string{"missing.yaml", "../../v/b.yaml"}, ignoreMissing: true},
		{name: "ref file", files: []string{"$values/vals/abs.yaml"}},
		{name: "ref symlinked directory", files: []string{"$values/vals/dir-link/inner.yaml"}},
		{name: "ref symlink out of the ref root", files: []string{"$values/vals/escape.yaml"}},
		{name: "bare ref", files: []string{"$values"}},
		{name: "bare ref with a slash", files: []string{"$values/"}},
		{name: "disallowed URL scheme", files: []string{"s3://bucket/values.yaml"}},
		{
			// The repo-server reads the file outside the repository. Its
			// out-of-bounds check fails the repository first unless
			// reposerver.allow.oob.symlinks is true. drydock never reads
			// outside the repository.
			name:       "symlinked directory out of the repository",
			files:      []string{"/vals/escape-dir/outside.yaml"},
			drydockErr: "makes helm read a file outside the repository, which drydock never reads",
		},
		{name: "path in .git", files: []string{"/.git/config.yaml"}, drydockErr: "enters a .git directory"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			repo := writeHelmValueFileSymlinkRepo(t, t.TempDir())
			refRoots := map[string]string{"$values": repo}
			want, wantErr := upstreamHelmValueFileValues(filepath.Join(repo, "charts", "app"), repo, nil, refRoots, tt.files, tt.ignoreMissing)
			result, _, err := (HelmRenderer{}).Render(context.Background(), ResolvedSource{RepoRoot: repo, Path: "charts/app"}, RenderOptions{
				AppName:                 "probe",
				RefRoots:                refRoots,
				ValueFiles:              tt.files,
				IgnoreMissingValueFiles: tt.ignoreMissing,
			})
			assertHelmValueFileParity(t, result, err, want, wantErr, tt.wantErr, tt.drydockErr)
		})
	}
}

// assertHelmValueFileParity requires the drydock render result and err to
// match the upstream values want or error wantErr. wantText is the text
// that both errors must hold, when set. drydockErr is the error that
// drydock must report where the repo-server renders.
func assertHelmValueFileParity(t *testing.T, result []Manifest, err error, want map[string]any, wantErr error, wantText, drydockErr string) {
	t.Helper()
	if drydockErr != "" {
		if wantErr != nil {
			t.Fatalf("upstream error = %v, want a render", wantErr)
		}
		if err == nil || !strings.Contains(err.Error(), drydockErr) {
			t.Fatalf("Render() error = %v, want %q", err, drydockErr)
		}
		if strings.Contains(err.Error(), "content-outside-the-repository") {
			t.Fatalf("Render() error = %v leaked file contents", err)
		}
		return
	}
	if wantErr != nil {
		text := wantErr.Error()
		if wantText != "" {
			if !strings.Contains(text, wantText) {
				t.Fatalf("upstream error = %v, want %q", wantErr, wantText)
			}
			text = wantText
		}
		if err == nil || !strings.Contains(err.Error(), text) {
			t.Fatalf("Render() error = %v, want the repo-server's %q", err, text)
		}
		return
	}
	if wantText != "" {
		t.Fatalf("upstream values = %v, want error %q", want, wantText)
	}
	if err != nil {
		t.Fatalf("Render() error = %v, want %v", err, want)
	}
	raw, _, _ := unstructured.NestedString(result[0].Object.Object, "data", "values")
	var got map[string]any
	if err := yaml.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("decode values %q: %v", raw, err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("values = %v, want %v", got, want)
	}
}

// TestCollectHelmLocalInputPathsFollowsValueFileSymlinks pins that the
// persistent render cache digest of a Helm source covers the symlinks that
// a value file path passes through and the file that helm reads.
func TestCollectHelmLocalInputPathsFollowsValueFileSymlinks(t *testing.T) {
	repo := writeHelmValueFileSymlinkRepo(t, t.TempDir())
	paths, err := CollectHelmLocalInputPaths(HelmLocalInputOptions{
		RepoRoot: repo,
		Source:   ResolvedSource{Path: "charts/app"},
		Options: RenderOptions{
			ValueFiles: []string{"../../v/link-a.yaml", "/vals/dir-link/inner.yaml", "/vals/glob/*.yaml"},
		},
	})
	if err != nil {
		t.Fatalf("CollectHelmLocalInputPaths() error = %v", err)
	}
	got := map[string]bool{}
	for _, path := range paths {
		got[path.Path] = path.Optional
	}
	for _, want := range []string{
		"charts/app",
		"v/link-a.yaml",
		"v/a.yaml",
		"vals/dir-link",
		"vals/real/inner.yaml",
		"vals/glob/link-abs.yaml",
		"vals/glob/plain.yaml",
		"vals/abs.yaml",
	} {
		optional, ok := got[want]
		if !ok || optional {
			t.Fatalf("input paths = %#v, want required %q", paths, want)
		}
	}
}

// TestHelmFileParameterURLFetchesThroughRemoteCache pins that a
// fileParameters URL with an allowed scheme is read as helm --set-file reads
// it in Argo CD: helm fetches the URL (pkg/cli/values readFile at helm
// v4.3.1). drydock fetches it through the remote-resource cache and passes
// --offline on, so an offline render reads only a cached copy.
func TestHelmFileParameterURLFetchesThroughRemoteCache(t *testing.T) {
	repo := writeHelmValueFileSymlinkRepo(t, t.TempDir())
	content := filepath.Join(t.TempDir(), "remote.txt")
	writeFile(t, content, "remote-content")
	acquirer := &fakeRemoteAcquirer{path: content}
	result, _, err := (HelmRenderer{}).Render(context.Background(), ResolvedSource{RepoRoot: repo, Path: "charts/app"}, RenderOptions{
		AppName:                "probe",
		RemoteResourceAcquirer: acquirer,
		OfflineRemoteResources: true,
		HelmFileParameters: []argoappv1.HelmFileParameter{
			{Name: "f", Path: "https://values.example.test/a,b.txt"},
		},
	})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	raw, _, _ := unstructured.NestedString(result[0].Object.Object, "data", "values")
	var got map[string]string
	if err := yaml.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("decode values %q: %v", raw, err)
	}
	if got["f"] != "remote-content" {
		t.Fatalf("values = %v, want f from the remote file", got)
	}
	if len(acquirer.requests) != 1 || acquirer.requests[0].URL != "https://values.example.test/a,b.txt" || !acquirer.requests[0].AnyFileType {
		t.Fatalf("remote requests = %#v, want the file parameter URL", acquirer.requests)
	}
	if !acquirer.options[0].Offline {
		t.Fatalf("remote options = %#v, want --offline passed on", acquirer.options)
	}

	_, _, err = (HelmRenderer{}).Render(context.Background(), ResolvedSource{RepoRoot: repo, Path: "charts/app"}, RenderOptions{
		AppName:                "probe",
		RemoteResourceAcquirer: acquirer,
		HelmFileParameters: []argoappv1.HelmFileParameter{
			{Name: "f", Path: "s3://bucket/f.txt"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "error resolving helm value file path: the URL scheme 's3' is not allowed") {
		t.Fatalf("Render() error = %v, want the repo-server's scheme error", err)
	}
}

// TestHelmValueFileSmokeFixtureMatchesUpstream renders the value files of
// the parity smoke Application parity-helm-value-file-paths with drydock
// and with the repo-server's resolution (upstreamHelmValueFileValues), and
// requires the values that the fixture README lists.
func TestHelmValueFileSmokeFixtureMatchesUpstream(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", "testdata", "argocd-parity", "repo"))
	if err != nil {
		t.Fatal(err)
	}
	const chartPath = "charts/helm-value-file-paths"
	data, err := os.ReadFile(filepath.Join(repoRoot, "applications", "helm-value-file-paths.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var app argoappv1.Application
	if err := yaml.Unmarshal(data, &app); err != nil {
		t.Fatal(err)
	}
	files := app.Spec.Source.Helm.ValueFiles
	want := map[string]any{
		"winner":        "b",
		"fromA":         "a",
		"fromB":         "b",
		"fromAbsolute":  "absolute",
		"fromLinkedDir": "linked-dir",
		"globPlain":     "plain",
	}
	upstream, err := upstreamHelmValueFileValues(filepath.Join(repoRoot, chartPath), repoRoot, nil, nil, files, false)
	if err != nil {
		t.Fatalf("upstream error = %v", err)
	}
	upstream = chartv2loader.MergeMaps(map[string]any{"winner": "chart-default"}, upstream)
	if !reflect.DeepEqual(upstream, want) {
		t.Fatalf("upstream values = %v, want %v", upstream, want)
	}
	result, _, err := (HelmRenderer{}).Render(context.Background(), ResolvedSource{RepoRoot: repoRoot, Path: chartPath}, RenderOptions{
		AppName:    app.Name,
		Namespace:  app.Spec.Destination.Namespace,
		ValueFiles: files,
	})
	assertHelmValueFileParity(t, result, err, want, nil, "", "")
}
