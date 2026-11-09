package discovery

import (
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path"
	"path/filepath"
	"strings"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/change"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/manifest"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type Options struct {
	AppManifestPaths []string
	// IgnoreGlobs removes matching repository-relative files from discovery
	// before any decode, including explicit AppManifestPaths scans.
	IgnoreGlobs []string
}

type SourceTier int

const (
	SourceTierStatic SourceTier = iota
	SourceTierExplicitRendered
	SourceTierPolicyBootstrap
	SourceTierRenderedFleet
	// SourceTierExternalManifest marks objects loaded from an operator-supplied
	// --discover-manifest file outside the trees under analysis. Their Path is
	// the absolute file path and never a repository path.
	SourceTierExternalManifest
)

type ApplicationFile struct {
	Path          string
	DocumentIndex int
	Application   argoappv1.Application
	Tier          SourceTier
	InputPaths    []string
	// ParentKey and RenderedDir record what rendered the Application, for
	// changed-only ownership only; they never key a cache or fingerprint.
	// ParentKey is the caller's namespace/name key of the Application whose
	// render declared it (SourceTierRenderedFleet). RenderedDir is the
	// repository directory rendered to declare it without a parent
	// Application (SourceTierExplicitRendered, SourceTierPolicyBootstrap).
	ParentKey   string
	RenderedDir string
	// ApplicationSetKey is the namespace/name key (namespace + "\x00" +
	// name) of the ApplicationSet that generated the Application. It is
	// empty for an Application that a manifest declares. Like ParentKey, it
	// never keys a cache or fingerprint.
	ApplicationSetKey string
	// GitGeneratorProject, when set, is the AppProject that a Git generator
	// of that ApplicationSet looked up (appset.GeneratedApplication). Argo
	// CD fails the whole ApplicationSet when it does not exist in the
	// cluster. Discovery warns when the repository does not declare it.
	GitGeneratorProject *string
}

type ApplicationSetFile struct {
	Path           string
	DocumentIndex  int
	ApplicationSet argoappv1.ApplicationSet
	Tier           SourceTier
	InputPaths     []string
	// ParentKey and RenderedDir pass on to the generated Applications; see
	// ApplicationFile.
	ParentKey   string
	RenderedDir string
}

type ProjectFile struct {
	Path          string
	DocumentIndex int
	Project       argoappv1.AppProject
	Tier          SourceTier
}

type SettingsCandidate struct {
	Path          string
	DocumentIndex int
	Kind          string
	APIVersion    string
	Namespace     string
	Name          string
	Object        *unstructured.Unstructured
	Tier          SourceTier
	// RenderValuesFiles and RenderFiles list, for a settings ConfigMap that
	// an Application render produced, the repository files that render read:
	// the local Helm values files, and the other local Kustomize input files.
	// They hold files only, never directories. The rendered ConfigMap is what
	// the cluster gets from those files, so a committed values file in
	// RenderValuesFiles, or a committed settings ConfigMap in RenderFiles, is
	// replaced by it. Only rendered candidates set them.
	RenderValuesFiles []string
	RenderFiles       []string
	// Origin is the key of the Application whose render produced the
	// candidate. It is empty for a committed file.
	Origin string
	// RemoteOnly reports a rendered candidate that only Applications on
	// other clusters render. It stays a settings source, but it never names
	// the Argo CD controller namespace of this cluster.
	RemoteOnly bool
	// InstallNamespace is set on a settings ConfigMap that an Argo CD
	// install on this cluster renders: the namespace where it lands, its
	// own or else the destination namespace of the install. It counts as a
	// setting only when that is the controller namespace.
	InstallNamespace string
	// OtherInstall reports a settings ConfigMap of an Argo CD install whose
	// own argocd-cmd-params-cm would not reconcile the Application that
	// installs it. That install is another Argo CD, such as a tenant one:
	// its ConfigMaps never name the controller namespace and are never
	// settings.
	OtherInstall bool
	// SettingsOnly reports a candidate of an Application that drydock
	// renders only for its Argo CD settings. Each settle round of those
	// renders replaces the candidates of the earlier rounds.
	SettingsOnly bool
}

// IsSettingsConfigMapKind reports whether kind is the candidate kind of an
// Argo CD settings ConfigMap: argocd-cm, argocd-cmd-params-cm, or
// argocd-cmp-cm.
func IsSettingsConfigMapKind(kind string) bool {
	switch kind {
	case "argocd-cm", "argocd-cmd-params-cm", "argocd-cmp-cm":
		return true
	default:
		return false
	}
}

type Result struct {
	Applications       []ApplicationFile
	ApplicationSets    []ApplicationSetFile
	SettingsCandidates []SettingsCandidate
	Projects           []ProjectFile
	// ControllerNamespaceHint is the Argo CD controller namespace that the
	// last discovery run used, when its settings do not settle on it. The
	// settings of the result then use it too, so discovery and the final
	// render agree. It is empty otherwise.
	ControllerNamespaceHint string
}

func Scan(root string, opts Options) (Result, error) {
	var result Result
	ignore, err := change.NewIgnoreMatcher(opts.IgnoreGlobs, "discover-ignore")
	if err != nil {
		return result, err
	}
	roots := opts.AppManifestPaths
	explicit := len(roots) > 0
	if !explicit {
		roots = []string{"."}
	}

	seen := map[string]struct{}{}
	for _, relRoot := range roots {
		start, err := scanStart(root, relRoot)
		if err != nil {
			return result, err
		}
		if explicit {
			if err := rejectSymlinkComponents(root, start); err != nil {
				return result, err
			}
		}
		if err := scanPath(root, start, explicit, ignore, seen, &result); err != nil {
			return result, err
		}
	}
	return result, nil
}

func ScanObjects(path string, objects []*unstructured.Unstructured) (Result, error) {
	var result Result
	for index, obj := range objects {
		if obj == nil {
			continue
		}
		if err := scanDocument(path, index, obj, true, &result); err != nil {
			return result, err
		}
	}
	return result, nil
}

func scanPath(root, start string, explicit bool, ignore change.IgnoreMatcher, seen map[string]struct{}, result *Result) error {
	info, err := os.Lstat(start)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		if explicit {
			rel, relErr := relativePath(root, start)
			if relErr != nil {
				return relErr
			}
			return fmt.Errorf("app manifest path %q is a symlink", rel)
		}
		return nil
	}
	if !info.IsDir() {
		if !isYAML(start) {
			return nil
		}
		return scanYAMLFileOnce(root, start, explicit, ignore, seen, result)
	}

	return filepath.WalkDir(start, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() {
			if shouldSkipDir(entry.Name()) && path != start {
				return filepath.SkipDir
			}
			return nil
		}
		if !isYAML(path) {
			return nil
		}
		return scanYAMLFileOnce(root, path, explicit, ignore, seen, result)
	})
}

func scanYAMLFileOnce(root, path string, explicit bool, ignore change.IgnoreMatcher, seen map[string]struct{}, result *Result) error {
	rel, err := relativePath(root, path)
	if err != nil {
		return err
	}
	// ignore wins everywhere, including explicit AppManifestPaths scans, and
	// runs before any decode work.
	if ignore.Matches(filepath.ToSlash(rel)) {
		return nil
	}
	if _, ok := seen[rel]; ok {
		return nil
	}
	seen[rel] = struct{}{}
	if !explicit {
		if isHelmTemplateYAML(root, path) {
			return nil
		}
		candidate, err := looksLikeCandidate(path)
		if err != nil {
			return err
		}
		if !candidate {
			return nil
		}
	}
	return scanYAMLFile(path, rel, result)
}

const discoverIgnoreHint = "(use --discover-ignore to exclude non-deployable manifests from discovery)"

func scanYAMLFile(path, rel string, result *Result) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()

	docs, err := manifest.DecodeDocuments(path, file)
	if err != nil {
		return fmt.Errorf("%w %s", err, discoverIgnoreHint)
	}
	for _, doc := range docs {
		if err := scanDocument(rel, doc.Index, doc.Object, false, result); err != nil {
			return fmt.Errorf("%w %s", err, discoverIgnoreHint)
		}
	}
	return nil
}

func scanDocument(rel string, documentIndex int, obj *unstructured.Unstructured, includeObject bool, result *Result) error {
	switch {
	case isArgoGVK(obj, "Application"):
		var app argoappv1.Application
		if err := unstructuredToTyped(obj.Object, &app); err != nil {
			return fmt.Errorf("%s: decode Application: %w", rel, err)
		}
		result.Applications = append(result.Applications, ApplicationFile{Path: rel, DocumentIndex: documentIndex, Application: app, InputPaths: []string{rel}})
	case isArgoGVK(obj, "ApplicationSet"):
		var appSet argoappv1.ApplicationSet
		if err := unstructuredToTyped(obj.Object, &appSet); err != nil {
			return fmt.Errorf("%s: decode ApplicationSet: %w", rel, err)
		}
		result.ApplicationSets = append(result.ApplicationSets, ApplicationSetFile{Path: rel, DocumentIndex: documentIndex, ApplicationSet: appSet, InputPaths: []string{rel}})
	case isArgoGVK(obj, "AppProject"):
		var project argoappv1.AppProject
		if err := unstructuredToTyped(obj.Object, &project); err != nil {
			return fmt.Errorf("%s: decode AppProject: %w", rel, err)
		}
		result.Projects = append(result.Projects, ProjectFile{Path: rel, DocumentIndex: documentIndex, Project: project})
	default:
		if kind, ok := settingsCandidateKind(obj); ok {
			result.SettingsCandidates = append(result.SettingsCandidates, settingsCandidate(rel, documentIndex, kind, obj, includeObject))
		}
	}
	return nil
}

func settingsCandidateKind(obj *unstructured.Unstructured) (string, bool) {
	switch {
	case isCoreGVK(obj, "ConfigMap") && obj.GetName() == "argocd-cm":
		return "argocd-cm", true
	case isCoreGVK(obj, "ConfigMap") && obj.GetName() == "argocd-cmd-params-cm":
		return "argocd-cmd-params-cm", true
	case isCoreGVK(obj, "ConfigMap") && obj.GetName() == "argocd-cmp-cm" && isConfigManagementPluginConfigMap(obj):
		return "argocd-cmp-cm", true
	case isCoreGVK(obj, "Secret") && obj.GetLabels()["argocd.argoproj.io/secret-type"] == "repository":
		return "repository-secret", true
	case isCoreGVK(obj, "Secret") && obj.GetLabels()["argocd.argoproj.io/secret-type"] == "cluster":
		return "cluster-secret", true
	case isArgoHelmValuesSettings(obj):
		return "argocd-values", true
	case IsRepoServerObject(obj, "apps", "Deployment"):
		return RepoServerDeploymentKind, true
	case IsRepoServerObject(obj, "", "ServiceAccount"):
		return RepoServerServiceAccountKind, true
	default:
		return "", false
	}
}

// The candidate kinds of the Argo CD repo-server Deployment and its
// ServiceAccount. They decide the namespace that helm uses when Argo CD or
// kustomize passes no --namespace (see config.RepoServerHelmNamespace).
const (
	RepoServerDeploymentKind     = "argocd-repo-server"
	RepoServerServiceAccountKind = "argocd-repo-server-serviceaccount"
)

// IsRepoServerKind reports whether kind is a repo-server candidate kind.
func IsRepoServerKind(kind string) bool {
	return kind == RepoServerDeploymentKind || kind == RepoServerServiceAccountKind
}

// IsRepoServerObject reports whether obj is the Argo CD repo-server object
// of the given group and kind: the upstream install names it
// argocd-repo-server, and the argo-cd Helm chart labels it with the
// repo-server component of argocd.
func IsRepoServerObject(obj *unstructured.Unstructured, group, kind string) bool {
	gvk := obj.GroupVersionKind()
	if gvk.Group != group || gvk.Version != "v1" || gvk.Kind != kind {
		return false
	}
	if obj.GetName() == "argocd-repo-server" {
		return true
	}
	labels := obj.GetLabels()
	return labels["app.kubernetes.io/component"] == "repo-server" && labels["app.kubernetes.io/part-of"] == "argocd"
}

func settingsCandidate(rel string, documentIndex int, kind string, obj *unstructured.Unstructured, includeObject bool) SettingsCandidate {
	candidate := SettingsCandidate{
		Path:          rel,
		DocumentIndex: documentIndex,
		Kind:          kind,
	}
	if obj == nil {
		return candidate
	}
	candidate.APIVersion = obj.GetAPIVersion()
	candidate.Namespace = obj.GetNamespace()
	candidate.Name = obj.GetName()
	// A repo-server object is kept also from a committed file: drydock has
	// no loader that reads it back by path, and the sanitized copy is small.
	if includeObject || IsRepoServerKind(kind) {
		candidate.Object = settingsCandidateObject(kind, obj)
	}
	return candidate
}

func settingsCandidateObject(kind string, obj *unstructured.Unstructured) *unstructured.Unstructured {
	switch kind {
	case "repository-secret":
		return sanitizedSecretObject(obj, []string{"name", "type", "url", "enableOCI", "enableLfs", "project"})
	case "cluster-secret":
		return sanitizedSecretObject(obj, []string{"name", "server", "namespaces", "clusterResources", "project"})
	case RepoServerDeploymentKind:
		return sanitizedRepoServerDeployment(obj)
	case RepoServerServiceAccountKind:
		return sanitizedRepoServerServiceAccount(obj)
	default:
		return obj.DeepCopy()
	}
}

func sanitizedSecretObject(obj *unstructured.Unstructured, allowedKeys []string) *unstructured.Unstructured {
	allowed := make(map[string]struct{}, len(allowedKeys))
	for _, key := range allowedKeys {
		allowed[key] = struct{}{}
	}

	out := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": obj.GetAPIVersion(),
		"kind":       obj.GetKind(),
		"metadata": map[string]any{
			"name":      obj.GetName(),
			"namespace": obj.GetNamespace(),
			"labels":    obj.GetLabels(),
		},
	}}
	if data := filteredNestedStringMap(obj, allowed, "data"); len(data) > 0 {
		out.Object["data"] = data
	}
	if stringData := filteredNestedStringMap(obj, allowed, "stringData"); len(stringData) > 0 {
		out.Object["stringData"] = stringData
	}
	return out
}

func filteredNestedStringMap(obj *unstructured.Unstructured, allowed map[string]struct{}, fields ...string) map[string]any {
	values, ok, err := unstructured.NestedStringMap(obj.Object, fields...)
	if err != nil || !ok {
		return nil
	}
	filtered := make(map[string]any, len(values))
	for key, value := range values {
		if _, ok := allowed[key]; !ok {
			continue
		}
		filtered[key] = value
	}
	return filtered
}

func isArgoGVK(obj *unstructured.Unstructured, kind string) bool {
	gvk := obj.GroupVersionKind()
	return gvk.Group == "argoproj.io" && gvk.Version == "v1alpha1" && gvk.Kind == kind
}

func isCoreGVK(obj *unstructured.Unstructured, kind string) bool {
	gvk := obj.GroupVersionKind()
	return gvk.Group == "" && gvk.Version == "v1" && gvk.Kind == kind
}

func isArgoHelmValuesSettings(obj *unstructured.Unstructured) bool {
	configs, ok := obj.Object["configs"].(map[string]any)
	if !ok {
		return false
	}
	if cmp, ok := configs["cmp"].(map[string]any); ok {
		if plugins, ok := cmp["plugins"].(map[string]any); ok && len(plugins) > 0 {
			return true
		}
	}
	cm, ok := configs["cm"].(map[string]any)
	if !ok {
		return false
	}
	for key := range cm {
		if config.IsArgoCDHelmValuesConfigMapKey(key) {
			return true
		}
	}
	return false
}

func isConfigManagementPluginConfigMap(obj *unstructured.Unstructured) bool {
	data, ok, err := unstructured.NestedStringMap(obj.Object, "data")
	if err != nil || !ok {
		return false
	}
	for _, value := range data {
		if looksLikeConfigManagementPluginYAML(value) {
			return true
		}
	}
	return false
}

func scanStart(root, relRoot string) (string, error) {
	if filepath.IsAbs(relRoot) {
		return "", fmt.Errorf("app manifest path %q must be relative", relRoot)
	}
	joined := filepath.Join(root, relRoot)
	rel, err := filepath.Rel(root, joined)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("app manifest path %q escapes repository root", relRoot)
	}
	return joined, nil
}

func relativePath(root, path string) (string, error) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return "", err
	}
	return filepath.Clean(rel), nil
}

func rejectSymlinkComponents(root, target string) error {
	rel, err := relativePath(root, target)
	if err != nil {
		return err
	}
	if rel == "." {
		return nil
	}

	current := root
	for component := range strings.SplitSeq(rel, string(filepath.Separator)) {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("app manifest path %q includes symlink component %q", rel, component)
		}
	}
	return nil
}

func looksLikeCandidate(path string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	text := string(data)
	return strings.Contains(text, "argoproj.io/v1alpha1") ||
		strings.Contains(text, "argocd-cm") ||
		strings.Contains(text, "argocd-cmd-params-cm") ||
		strings.Contains(text, "ConfigManagementPlugin") ||
		strings.Contains(text, "argocd.argoproj.io/secret-type") ||
		(strings.Contains(text, "configs:") && (strings.Contains(text, "cm:") || strings.Contains(text, "cmp:"))), nil
}

func looksLikeConfigManagementPluginYAML(value string) bool {
	return strings.Contains(value, "ConfigManagementPlugin")
}

func shouldSkipDir(name string) bool {
	switch name {
	case ".git", ".out", ".Trash", ".Trashes", ".Spotlight-V100", ".DocumentRevisions-V100", ".TemporaryItems", ".fseventsd":
		return true
	default:
		return strings.HasPrefix(name, ".cache")
	}
}

func isHelmTemplateYAML(root, path string) bool {
	if !isYAML(path) {
		return false
	}
	root = filepath.Clean(root)
	dir := filepath.Clean(filepath.Dir(path))
	for {
		if filepath.Base(dir) == "templates" && chartFileExists(filepath.Dir(dir)) {
			return true
		}
		if samePath(dir, root) {
			return false
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

func chartFileExists(dir string) bool {
	info, err := os.Lstat(filepath.Join(dir, "Chart.yaml"))
	return err == nil && info.Mode().IsRegular()
}

func samePath(left, right string) bool {
	leftAbs, leftErr := filepath.Abs(filepath.Clean(left))
	rightAbs, rightErr := filepath.Abs(filepath.Clean(right))
	if leftErr == nil && rightErr == nil {
		return leftAbs == rightAbs
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

func isYAML(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml":
		return true
	default:
		return false
	}
}

func unstructuredToTyped(in map[string]any, out any) error {
	data, err := json.Marshal(in)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

// repoServerNamespaceEnv are the container environment variables that decide
// the namespace helm uses: HELM_NAMESPACE (helm EnvSettings), POD_NAMESPACE
// (client-go in-cluster config), and KUBECONFIG.
var repoServerNamespaceEnv = map[string]struct{}{
	"HELM_NAMESPACE":   {},
	"HELM_KUBECONTEXT": {},
	"POD_NAMESPACE":    {},
	"KUBECONFIG":       {},
}

// sanitizedRepoServerDeployment keeps only the fields of a repo-server
// Deployment that decide the namespace helm uses, so no other environment
// value of the install is kept: the metadata, the pod
// automountServiceAccountToken and serviceAccountName, and per container
// its name, the volume mount paths, and the env entries in
// repoServerNamespaceEnv.
func sanitizedRepoServerDeployment(obj *unstructured.Unstructured) *unstructured.Unstructured {
	out := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": obj.GetAPIVersion(),
		"kind":       obj.GetKind(),
	}}
	out.SetName(obj.GetName())
	out.SetNamespace(obj.GetNamespace())
	out.SetLabels(obj.GetLabels())
	podSpec, _, _ := unstructured.NestedMap(obj.Object, "spec", "template", "spec")
	keptSpec := map[string]any{}
	for _, key := range []string{"automountServiceAccountToken", "serviceAccountName", "serviceAccount"} {
		if value, ok := podSpec[key]; ok {
			keptSpec[key] = value
		}
	}
	containers, _, _ := unstructured.NestedSlice(podSpec, "containers")
	keptContainers := make([]any, 0, len(containers))
	for _, item := range containers {
		container, ok := item.(map[string]any)
		if !ok {
			continue
		}
		kept := map[string]any{"name": container["name"]}
		var env []any
		envItems, _ := container["env"].([]any)
		for _, envItem := range envItems {
			entry, ok := envItem.(map[string]any)
			if !ok {
				continue
			}
			name, _ := entry["name"].(string)
			if _, ok := repoServerNamespaceEnv[name]; ok {
				env = append(env, sanitizedRepoServerEnv(name, entry))
			}
		}
		if len(env) > 0 {
			kept["env"] = env
		}
		if envFrom, ok := container["envFrom"].([]any); ok && len(envFrom) > 0 {
			kept[config.RepoServerEnvFromKey] = true
		}
		var mounts []any
		mountItems, _ := container["volumeMounts"].([]any)
		for _, mountItem := range mountItems {
			if mount, ok := mountItem.(map[string]any); ok {
				mounts = append(mounts, map[string]any{"name": mount["name"], "mountPath": mount["mountPath"]})
			}
		}
		if len(mounts) > 0 {
			kept["volumeMounts"] = mounts
		}
		keptContainers = append(keptContainers, kept)
	}
	keptSpec["containers"] = keptContainers
	if volumes := sanitizedRepoServerVolumes(podSpec); len(volumes) > 0 {
		keptSpec["volumes"] = volumes
	}
	_ = unstructured.SetNestedMap(out.Object, map[string]any{"spec": keptSpec}, "spec", "template")
	return out
}

// sanitizedRepoServerVolumes keeps, for each volume of the pod, only its
// name and whether it holds a file named token and a file named namespace
// (config.RepoServerTokenFileKey and config.RepoServerNamespaceFileKey).
// client-go needs the token for the in-cluster config and reads the
// namespace file next to it. A Secret volume without items holds every key
// of the Secret; a service account token Secret holds both files, so it
// counts for both. With items, and for the secret, configMap, and
// downwardAPI sources of a projected volume, a file counts only when an
// item has its path, and a serviceAccountToken source gives the token at
// its path. drydock keeps no value and no object name.
func sanitizedRepoServerVolumes(podSpec map[string]any) []any {
	items, _ := podSpec["volumes"].([]any)
	out := make([]any, 0, len(items))
	for _, item := range items {
		volume, ok := item.(map[string]any)
		if !ok {
			continue
		}
		files := map[string]bool{}
		if secret, ok := volume["secret"].(map[string]any); ok {
			paths, listed := itemPaths(secret)
			if !listed {
				files["token"], files["namespace"] = true, true
			}
			maps.Copy(files, paths)
		}
		sources, _, _ := unstructured.NestedSlice(volume, "projected", "sources")
		for _, sourceItem := range sources {
			source, _ := sourceItem.(map[string]any)
			if token, ok := source["serviceAccountToken"].(map[string]any); ok {
				if tokenPath, _ := token["path"].(string); tokenPath != "" {
					files[tokenPath] = true
				}
			}
			for _, kind := range []string{"secret", "configMap", "downwardAPI"} {
				if projection, ok := source[kind].(map[string]any); ok {
					paths, _ := itemPaths(projection)
					maps.Copy(files, paths)
				}
			}
		}
		out = append(out, map[string]any{
			"name":                            volume["name"],
			config.RepoServerTokenFileKey:     files["token"],
			config.RepoServerNamespaceFileKey: files["namespace"],
		})
	}
	return out
}

// itemPaths returns the file paths that the items of a volume source
// project, and whether the source lists items.
func itemPaths(source map[string]any) (map[string]bool, bool) {
	items, ok := source["items"].([]any)
	if !ok {
		return nil, false
	}
	paths := map[string]bool{}
	for _, item := range items {
		if entry, ok := item.(map[string]any); ok {
			if itemPath, _ := entry["path"].(string); itemPath != "" {
				paths[path.Clean(itemPath)] = true
			}
		}
	}
	return paths, true
}

// sanitizedRepoServerServiceAccount keeps only the fields of a repo-server
// ServiceAccount that decide whether its token is mounted.
func sanitizedRepoServerServiceAccount(obj *unstructured.Unstructured) *unstructured.Unstructured {
	out := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": obj.GetAPIVersion(),
		"kind":       obj.GetKind(),
	}}
	out.SetName(obj.GetName())
	out.SetNamespace(obj.GetNamespace())
	out.SetLabels(obj.GetLabels())
	if automount, ok := obj.Object["automountServiceAccountToken"].(bool); ok {
		out.Object["automountServiceAccountToken"] = automount
	}
	return out
}

// sanitizedRepoServerEnv keeps an env entry that decides the namespace only
// when drydock knows its value: a literal value without a $(VAR) reference,
// or the pod namespace through the downward API. For any other source it
// keeps only the kind of the source (config.RepoServerUnknownSourceKey),
// never the value or the name of the object that holds it.
func sanitizedRepoServerEnv(name string, entry map[string]any) map[string]any {
	if value, ok := entry["value"].(string); ok {
		if strings.Contains(value, "$(") {
			return map[string]any{"name": name, config.RepoServerUnknownSourceKey: "a $(VAR) reference"}
		}
		return map[string]any{"name": name, "value": value}
	}
	valueFrom, ok := entry["valueFrom"].(map[string]any)
	if !ok {
		return map[string]any{"name": name}
	}
	if fieldPath, _, _ := unstructured.NestedString(valueFrom, "fieldRef", "fieldPath"); fieldPath == "metadata.namespace" {
		return map[string]any{"name": name, "valueFrom": map[string]any{"fieldRef": map[string]any{"fieldPath": "metadata.namespace"}}}
	}
	source := "valueFrom"
	for _, kind := range []string{"configMapKeyRef", "secretKeyRef", "fieldRef", "resourceFieldRef", "fileKeyRef"} {
		if _, ok := valueFrom[kind]; ok {
			source = "valueFrom " + kind
			break
		}
	}
	return map[string]any{"name": name, config.RepoServerUnknownSourceKey: source}
}
