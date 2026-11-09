package app

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/discovery"
	"github.com/sholdee/drydock/internal/render"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	sigsyaml "sigs.k8s.io/yaml"
)

// kustomizationPatches holds the patch refs of a kustomization.
type kustomizationPatches struct {
	Patches []struct {
		Path string `json:"path"`
	} `json:"patches"`
	PatchesStrategicMerge []string `json:"patchesStrategicMerge"`
	PatchesJSON6902       []struct {
		Path string `json:"path"`
	} `json:"patchesJson6902"`
}

// readKustomizationPatches reads the patch refs of the kustomization in
// dir.
func readKustomizationPatches(root, dir string) (kustomizationPatches, bool) {
	for _, name := range render.KustomizationFileNames {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(dir), name))
		if err != nil {
			continue
		}
		var kustomization kustomizationPatches
		if err := sigsyaml.Unmarshal(data, &kustomization); err != nil {
			return kustomizationPatches{}, false
		}
		return kustomization, true
	}
	return kustomizationPatches{}, false
}

// componentWorkloadKinds maps each Argo CD program to the candidate kind of
// its workload.
var componentWorkloadKinds = map[config.ArgoCDComponent]string{
	config.RepoServerComponent:               discovery.RepoServerDeploymentKind,
	config.ApplicationControllerComponent:    discovery.ApplicationControllerKind,
	config.ApplicationSetControllerComponent: discovery.ApplicationSetControllerKind,
}

// componentContainerNames maps each program to the container name of the
// argo-cd Helm chart. The upstream install names the container after the
// program.
var componentContainerNames = map[config.ArgoCDComponent]string{
	config.RepoServerComponent:               "repo-server",
	config.ApplicationControllerComponent:    "application-controller",
	config.ApplicationSetControllerComponent: "applicationset-controller",
}

// applyComponentWorkloadSettings reads the argocd-cmd-params-cm keys that
// drydock uses from the workload of the Argo CD program that reads each
// key, as Argo CD v3.6.1 does, and stores them in settings. Argo CD reads
// each key only from an environment variable of the program's container,
// which the install maps from argocd-cmd-params-cm with a configMapKeyRef,
// and a flag of the program wins over the variable
// (config.ComponentSettings).
//
// The kubelet builds the environment from envFrom first and then from env
// in order, and a later entry with the same name wins. A configMapKeyRef
// whose key is missing sets nothing when it is optional
// (makeEnvironmentVariables, pkg/kubelet/kubelet_pods.go:795-987 at
// Kubernetes v1.37.0). So the order of the chart template decides whether a
// literal value in repoServer.env wins over argocd-cmd-params-cm: the
// repo-server and ApplicationSet controller templates of the argo-cd chart
// 10.14.0 put the chart values first, so argocd-cmd-params-cm wins when it
// has the key, and the application controller StatefulSet puts them last,
// so they win.
//
// A workload counts with the rules of the repo-server for the Helm
// namespace (collectRepoServerCandidates): from the same install as an
// argocd-cm, or in the controller namespace. Without one, drydock keeps the
// argocd-cmd-params-cm values, as earlier releases did. drydock warns when
// it cannot know a value offline (a secretKeyRef, envFrom, a $(VAR)
// reference, a configMapKeyRef to a ConfigMap that it does not find) and
// when the workload does not pass a key that argocd-cmd-params-cm sets.
func applyComponentWorkloadSettings(root string, collected workloadCandidates, settings *config.ArgoSettings) []diagnostic.Diagnostic {
	var diags []diagnostic.Diagnostic
	base := *settings
	for _, component := range config.ArgoCDComponents {
		workloads := collected.byKind[componentWorkloadKinds[component]]
		var chosen []componentValue
		chosenRendered := false
		for _, item := range workloads {
			rendered := item.candidate.Tier != discovery.SourceTierStatic
			container := componentContainer(item.candidate.Object, component)
			if container == nil || (!rendered && committedKustomizePatch(root, item.candidate.Path)) {
				// Not a complete workload of the program: a CMP sidecar,
				// a patch without the program, or a listed Kustomize
				// patch, which holds only part of the env list.
				continue
			}
			name := repoServerDisplayName(item)
			provenance := diagnostic.Provenance{Path: filepath.ToSlash(item.candidate.Path)}
			values := make([]componentValue, 0, len(config.ComponentSettings(component)))
			for _, setting := range config.ComponentSettings(component) {
				values = append(values, resolveComponentSetting(container, setting, base, provenance))
			}
			if chosen != nil {
				// A committed workload after a rendered one is often a
				// Kustomize patch of it, which holds only part of the env.
				if rendered || !chosenRendered {
					diags = append(diags, componentConflictDiagnostics(component, name, provenance, chosen, values)...)
				}
				continue
			}
			chosen, chosenRendered = values, rendered
			for _, note := range componentArgsNotes(container) {
				diags = append(diags, repoServerWarning(provenance, fmt.Sprintf("cannot read the command line of the Argo CD %s %s offline: %s", component, name, note)))
			}
			for _, value := range values {
				diags = append(diags, value.diagnostics(component, name, base, provenance)...)
				config.ApplyComponentSetting(settings, value.setting.Key, value.value)
			}
		}
	}
	return diags
}

// componentContainer returns the container of a sanitized workload whose
// command line runs component (config.ComponentRunsProgramKey): the one
// named after the program or with the chart container name first, else the
// first one that runs it. It returns nil when no container runs the
// program: drydock does not guess which container would.
func componentContainer(obj *unstructured.Unstructured, component config.ArgoCDComponent) map[string]any {
	containers, _, _ := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "containers")
	var first map[string]any
	for _, item := range containers {
		container, ok := item.(map[string]any)
		if !ok || container[config.ComponentRunsProgramKey] != true {
			continue
		}
		if first == nil {
			first = container
		}
		if name := container["name"]; name == string(component) || name == componentContainerNames[component] {
			return container
		}
	}
	return first
}

// committedKustomizePatch reports whether a kustomization in the directory
// of the committed file, or in a directory above it, lists the file as a
// patch: a patches path, a patchesStrategicMerge entry, or a
// patchesJson6902 path. kustomize merges such a file into a workload, so it
// is not a workload of its own. drydock does not search other directories.
func committedKustomizePatch(root, file string) bool {
	file = path.Clean(filepath.ToSlash(file))
	for dir := path.Dir(file); ; dir = path.Dir(dir) {
		if kustomizationListsPatch(root, dir, file) {
			return true
		}
		if dir == "." || dir == "/" {
			return false
		}
	}
}

func kustomizationListsPatch(root, dir, file string) bool {
	kustomization, ok := readKustomizationPatches(root, dir)
	if !ok {
		return false
	}
	refs := append([]string(nil), kustomization.PatchesStrategicMerge...)
	for _, patch := range kustomization.Patches {
		refs = append(refs, patch.Path)
	}
	for _, patch := range kustomization.PatchesJSON6902 {
		refs = append(refs, patch.Path)
	}
	for _, ref := range refs {
		if ref != "" && !strings.Contains(ref, "\n") && path.Clean(path.Join(dir, ref)) == file {
			return true
		}
	}
	return false
}

func componentArgsNotes(container map[string]any) []string {
	items, _ := container[config.ComponentArgsNotesKey].([]any)
	notes := make([]string, 0, len(items))
	for _, item := range items {
		if note, ok := item.(string); ok {
			notes = append(notes, note)
		}
	}
	return notes
}

// componentValue is the value of one setting that a workload gives.
type componentValue struct {
	setting config.ComponentSetting
	// value is the setting that drydock uses. A zero value means the
	// default of the program.
	value config.Value[string]
	// source says where the value comes from: "flag", "env", "params"
	// for the argocd-cmd-params-cm reference, or "" for the default.
	source string
	// unknown names what drydock cannot know about the variable. drydock
	// then reads the variable as if that entry were not there.
	unknown []string
}

func (v componentValue) diagnostics(component config.ArgoCDComponent, name string, params config.ArgoSettings, workload diagnostic.Provenance) []diagnostic.Diagnostic {
	provenance := v.value.Provenance
	if provenance.Path == "" {
		provenance = workload
	}
	used := "the default"
	if v.source != "" {
		used = fmt.Sprintf("%q", v.value.Value)
	}
	var diags []diagnostic.Diagnostic
	for _, unknown := range v.unknown {
		diags = append(diags, repoServerWarning(provenance, fmt.Sprintf("cannot tell %s of the Argo CD %s %s offline: %s %s; drydock uses %s", v.setting.Key, component, name, v.setting.Env, unknown, used)))
	}
	declared := config.ComponentSettingValue(params, v.setting.Key)
	if v.source == "" && declared.Provenance.Path != "" && len(v.unknown) == 0 {
		diags = append(diags, repoServerWarning(declared.Provenance, fmt.Sprintf("argocd-cmd-params-cm sets %s, but the Argo CD %s %s does not pass it to the program as %s, so Argo CD ignores it; drydock uses the default", v.setting.Key, component, name, v.setting.Env)))
	}
	if v.setting.Key == applicationNamespacesSetting && (v.source == "flag" || v.source == "env") && v.value.Value != declared.Value {
		// commandParametersReach and installReconciles decide, before
		// drydock knows the workloads, which argocd-cmd-params-cm counts
		// and which install reconciles its own Application. They read the
		// list of each ConfigMap, so they can disagree with the gate.
		diags = append(diags, repoServerWarning(provenance, fmt.Sprintf("the Argo CD %s %s sets application.namespaces to %q, and drydock uses it for the namespace gate, but it decides which argocd-cmd-params-cm counts and whether an Argo CD install reconciles its own Application with the argocd-cmd-params-cm value %q", component, name, v.value.Value, declared.Value)))
	}
	return diags
}

// componentConflictDiagnostics warns when another workload that counts
// gives a setting another value than the one that drydock uses.
func componentConflictDiagnostics(component config.ArgoCDComponent, name string, provenance diagnostic.Provenance, chosen, values []componentValue) []diagnostic.Diagnostic {
	var diags []diagnostic.Diagnostic
	for i, value := range values {
		if i < len(chosen) && chosen[i].value.Value != value.value.Value {
			diags = append(diags, repoServerWarning(provenance, fmt.Sprintf("the Argo CD %s %s gives %s the value %q, but drydock uses %q from %s", component, name, value.setting.Key, value.value.Value, chosen[i].value.Value, chosen[i].value.Provenance.Path)))
		}
	}
	return diags
}

// resolveComponentSetting works out the value of setting in container: the
// flag, else the variable as the kubelet builds it, else the default.
func resolveComponentSetting(container map[string]any, setting config.ComponentSetting, params config.ArgoSettings, provenance diagnostic.Provenance) componentValue {
	out := componentValue{setting: setting}
	if envFromCanSet(container, setting.Env) {
		out.unknown = []string{"can come from envFrom"}
	}
	items, _ := container["env"].([]any)
	for index, item := range items {
		entry, ok := item.(map[string]any)
		if !ok || entry["name"] != setting.Env {
			continue
		}
		pointer := fmt.Sprintf("spec.template.spec.containers[%v].env[%d]", container["name"], index)
		out = out.withEnvEntry(entry, params, diagnostic.Provenance{Path: provenance.Path, Pointer: pointer})
	}
	flags, _ := container[config.ComponentFlagsKey].([]any)
	for _, item := range flags {
		flag, ok := item.(map[string]any)
		if !ok || flag["flag"] != setting.Flag || setting.Flag == "" {
			continue
		}
		value, _ := flag["value"].(string)
		pointer := fmt.Sprintf("spec.template.spec.containers[%v].args --%s", container["name"], setting.Flag)
		// The flag wins over the variable, so nothing about the variable
		// matters.
		return componentValue{setting: setting, source: "flag", value: config.Value[string]{Value: value, Provenance: diagnostic.Provenance{Path: provenance.Path, Pointer: pointer}}}
	}
	return out
}

// envFromCanSet reports whether an envFrom entry of container can set the
// variable name. The kubelet puts the prefix of the entry in front of each
// key of its source, so only an entry whose prefix name starts with can.
func envFromCanSet(container map[string]any, name string) bool {
	if container[config.RepoServerEnvFromKey] != true {
		return false
	}
	prefixes, ok := container[config.ComponentEnvFromPrefixesKey].([]any)
	if !ok {
		return true
	}
	for _, item := range prefixes {
		if prefix, _ := item.(string); strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// withEnvEntry applies one env entry of the variable, as the kubelet does:
// a later entry replaces the value, and an optional configMapKeyRef whose
// key is missing sets nothing.
func (v componentValue) withEnvEntry(entry map[string]any, params config.ArgoSettings, provenance diagnostic.Provenance) componentValue {
	if unknown, ok := entry[config.RepoServerUnknownSourceKey].(string); ok {
		v.unknown = append(v.unknown, "comes from "+unknown)
		return v
	}
	if value, ok := entry["value"].(string); ok {
		return componentValue{setting: v.setting, source: "env", value: config.Value[string]{Value: value, Provenance: provenance}}
	}
	ref, _, _ := unstructured.NestedMap(entry, "valueFrom", "configMapKeyRef")
	refName, _ := ref["name"].(string)
	key, _ := ref["key"].(string)
	optional, _ := ref["optional"].(bool)
	if refName != commandParametersConfigMapName {
		v.unknown = append(v.unknown, fmt.Sprintf("comes from ConfigMap %s, which drydock does not find in the render of the workload", refName))
		return v
	}
	declared, known := commandParameterByKey(params, key)
	switch {
	case !known:
		v.unknown = append(v.unknown, fmt.Sprintf("comes from the argocd-cmd-params-cm key %s, which drydock does not read", key))
	case declared.Provenance.Path != "":
		return componentValue{setting: v.setting, source: "params", value: declared}
	case !optional:
		v.unknown = append(v.unknown, fmt.Sprintf("comes from the argocd-cmd-params-cm key %s, which drydock does not find; the reference is not optional, so the pod does not start without it", key))
	}
	return v
}

const commandParametersConfigMapName = "argocd-cmd-params-cm"

// applicationNamespacesSetting is the argocd-cmd-params-cm key that the
// application controller reads as --application-namespaces.
const applicationNamespacesSetting = "application.namespaces"

// commandParameterByKey returns the value of an argocd-cmd-params-cm key
// that drydock reads, and false for a key that drydock does not read.
func commandParameterByKey(params config.ArgoSettings, key string) (config.Value[string], bool) {
	for _, component := range config.ArgoCDComponents {
		for _, setting := range config.ComponentSettings(component) {
			if setting.Key == key {
				return config.ComponentSettingValue(params, key), true
			}
		}
	}
	return config.Value[string]{}, false
}

// resolveWorkloadConfigMapRefs replaces, in the Argo CD workloads of one
// render, each setting variable that a configMapKeyRef reads from a
// ConfigMap of the same render with what the kubelet would read: the value
// of the key, nothing when the key is missing and the reference is
// optional, or a mark that the pod does not start. A reference to
// argocd-cmd-params-cm stays: drydock reads it from the settings that it
// finds (applyComponentWorkloadSettings). A reference to a ConfigMap that
// is not in the render stays too, and drydock warns about it. The
// manifests of the render are not changed: a changed workload is a copy.
func resolveWorkloadConfigMapRefs(manifests []render.Manifest) []render.Manifest {
	configMaps := map[string]*unstructured.Unstructured{}
	for _, manifest := range manifests {
		if obj := manifest.Object; obj != nil && obj.GetAPIVersion() == "v1" && obj.GetKind() == "ConfigMap" {
			configMaps[obj.GetNamespace()+"/"+obj.GetName()] = obj
		}
	}
	if len(configMaps) == 0 {
		return manifests
	}
	out := manifests
	copied := false
	for i, manifest := range manifests {
		if manifest.Object == nil {
			continue
		}
		kind, ok := discovery.SettingsCandidateKind(manifest.Object)
		component, isComponent := discovery.WorkloadComponent(kind)
		if !ok || !isComponent {
			continue
		}
		obj, changed := resolvedWorkload(manifest.Object, component, configMaps)
		if !changed {
			continue
		}
		if !copied {
			out = append([]render.Manifest(nil), manifests...)
			copied = true
		}
		out[i].Object = obj
	}
	return out
}

func resolvedWorkload(obj *unstructured.Unstructured, component config.ArgoCDComponent, configMaps map[string]*unstructured.Unstructured) (*unstructured.Unstructured, bool) {
	copyObj := obj.DeepCopy()
	containers, _, _ := unstructured.NestedSlice(copyObj.Object, "spec", "template", "spec", "containers")
	changed := false
	for ci, item := range containers {
		container, ok := item.(map[string]any)
		if !ok {
			continue
		}
		items, _ := container["env"].([]any)
		kept := make([]any, 0, len(items))
		for _, envItem := range items {
			entry, ok := envItem.(map[string]any)
			name, _ := entry["name"].(string)
			if !ok || !config.ComponentSettingEnv(component, name) {
				kept = append(kept, envItem)
				continue
			}
			resolved, keep, entryChanged := resolvedEnvEntry(entry, obj.GetNamespace(), configMaps)
			changed = changed || entryChanged
			if keep {
				kept = append(kept, resolved)
			}
		}
		container["env"] = kept
		containers[ci] = container
	}
	if !changed {
		return obj, false
	}
	_ = unstructured.SetNestedSlice(copyObj.Object, containers, "spec", "template", "spec", "containers")
	return copyObj, true
}

// resolvedEnvEntry resolves one configMapKeyRef entry against the
// ConfigMaps of the render in namespace. keep is false when the kubelet
// sets nothing for it.
func resolvedEnvEntry(entry map[string]any, namespace string, configMaps map[string]*unstructured.Unstructured) (resolved map[string]any, keep, changed bool) {
	if value, ok := entry["value"].(string); ok && value != "" {
		return entry, true, false
	}
	ref, found, _ := unstructured.NestedMap(entry, "valueFrom", "configMapKeyRef")
	if !found {
		return entry, true, false
	}
	refName, _ := ref["name"].(string)
	configMap, ok := configMaps[namespace+"/"+refName]
	if !ok || refName == commandParametersConfigMapName {
		return entry, true, false
	}
	key, _ := ref["key"].(string)
	name, _ := entry["name"].(string)
	value, found, _ := unstructured.NestedString(configMap.Object, "data", key)
	if found {
		return map[string]any{"name": name, "value": value}, true, true
	}
	if optional, _ := ref["optional"].(bool); optional {
		return nil, false, true
	}
	return map[string]any{"name": name, config.RepoServerUnknownSourceKey: fmt.Sprintf("ConfigMap %s without the key %s, and the reference is not optional, so the pod does not start", refName, strings.TrimSpace(key))}, true, true
}
