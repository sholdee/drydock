package pluginonboarding

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/sholdee/drydock/internal/diagnostic"
	"github.com/sholdee/drydock/internal/manifest"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

func collectSidecarCandidates(root string) ([]SidecarCandidate, error) {
	var out []SidecarCandidate
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			if shouldSkipDir(entry.Name()) && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		if !isYAML(path) {
			return nil
		}
		candidates, err := sidecarCandidatesFromFile(root, path)
		if err != nil {
			return err
		}
		out = append(out, candidates...)
		return nil
	})
	return dedupeSidecarCandidates(out), err
}

func sidecarCandidatesFromFile(root, path string) ([]SidecarCandidate, error) {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return nil, err
	}
	rel = filepath.ToSlash(rel)
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()

	data, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}
	var out []SidecarCandidate
	docs, err := manifest.DecodeDocuments(rel, strings.NewReader(string(data)))
	if err == nil {
		for _, doc := range docs {
			out = append(out, sidecarCandidatesFromObject(rel, doc.Index, doc.Object)...)
		}
	}
	out = append(out, sidecarCandidatesFromHelmValues(rel, data)...)
	return out, nil
}

func sidecarCandidatesFromObject(path string, documentIndex int, obj *unstructured.Unstructured) []SidecarCandidate {
	if obj == nil || !isRepoServerWorkload(obj) {
		return nil
	}
	containers, _, _ := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "containers")
	cmpKeysByVolume := cmpConfigMapKeysByVolume(obj)
	var out []SidecarCandidate
	for index, item := range containers {
		container, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name, _ := container["name"].(string)
		image, _ := container["image"].(string)
		name = strings.TrimSpace(name)
		image = strings.TrimSpace(image)
		if name == "" || image == "" || isRepoServerContainer(name) {
			continue
		}
		out = append(out, SidecarCandidate{
			Name:   name,
			Image:  image,
			Source: "manifest",
			Provenance: diagnostic.Provenance{
				Path:    path,
				Pointer: fmt.Sprintf("doc[%d].spec.template.spec.containers[%d].image", documentIndex, index),
			},
			Signals: sidecarSignals(container, cmpKeysByVolume),
		})
	}
	return out
}

func isRepoServerWorkload(obj *unstructured.Unstructured) bool {
	kind := obj.GetKind()
	if kind != "Deployment" && kind != "StatefulSet" {
		return false
	}
	name := strings.ToLower(obj.GetName())
	if strings.Contains(name, "repo-server") {
		return true
	}
	labels := obj.GetLabels()
	for key, value := range labels {
		key = strings.ToLower(key)
		value = strings.ToLower(value)
		if strings.Contains(key, "name") && strings.Contains(value, "repo-server") {
			return true
		}
	}
	return false
}

func isRepoServerContainer(name string) bool {
	name = strings.ToLower(name)
	return name == "argocd-repo-server" || name == "repo-server" || strings.Contains(name, "repo-server")
}

func cmpConfigMapKeysByVolume(obj *unstructured.Unstructured) map[string]map[string]string {
	volumes, _, _ := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "volumes")
	out := map[string]map[string]string{}
	for _, item := range volumes {
		volume, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name, _ := volume["name"].(string)
		configMap, _ := volume["configMap"].(map[string]any)
		configMapName, _ := configMap["name"].(string)
		if strings.TrimSpace(name) == "" || configMapName != "argocd-cmp-cm" {
			continue
		}
		keys := map[string]string{}
		if items, ok := configMap["items"].([]any); ok {
			for _, rawItem := range items {
				mapping, ok := rawItem.(map[string]any)
				if !ok {
					continue
				}
				key, _ := mapping["key"].(string)
				path, _ := mapping["path"].(string)
				key = strings.TrimSpace(key)
				path = strings.TrimSpace(path)
				if key == "" {
					continue
				}
				keys[key] = key
				if path != "" {
					keys[path] = key
				}
			}
		}
		out[name] = keys
	}
	return out
}

func sidecarSignals(container map[string]any, cmpKeysByVolume map[string]map[string]string) []string {
	var signals []string
	if mounts, ok := container["volumeMounts"].([]any); ok {
		for _, item := range mounts {
			mount, ok := item.(map[string]any)
			if !ok {
				continue
			}
			for _, key := range []string{"name", "mountPath", "subPath"} {
				if value, ok := mount[key].(string); ok && strings.TrimSpace(value) != "" {
					signals = append(signals, value)
				}
			}
			volumeName, _ := mount["name"].(string)
			subPath, _ := mount["subPath"].(string)
			if keys, ok := cmpKeysByVolume[volumeName]; ok {
				if len(keys) == 0 && subPath != "" {
					signals = append(signals, "argocd-cmp-cm:"+subPath)
				}
				if key := keys[subPath]; key != "" {
					signals = append(signals, "argocd-cmp-cm:"+key)
				}
			}
		}
	}
	if args, ok := container["args"].([]any); ok {
		for _, arg := range args {
			if value, ok := arg.(string); ok {
				signals = append(signals, value)
			}
		}
	}
	return signals
}

func sidecarCandidatesFromHelmValues(path string, data []byte) []SidecarCandidate {
	var values map[string]any
	if yaml.Unmarshal(data, &values) != nil {
		return nil
	}
	repoServer, ok := values["repoServer"].(map[string]any)
	if !ok {
		return nil
	}
	raw, ok := repoServer["extraContainers"]
	if !ok {
		return nil
	}
	var out []SidecarCandidate
	switch typed := raw.(type) {
	case []any:
		for index, item := range typed {
			if candidate, ok := helmValuesSidecarCandidate(path, fmt.Sprintf("repoServer.extraContainers[%d]", index), item); ok {
				out = append(out, candidate)
			}
		}
	case map[string]any:
		for key, item := range typed {
			if candidate, ok := helmValuesSidecarCandidate(path, "repoServer.extraContainers."+key, item); ok {
				if candidate.Name == "" {
					candidate.Name = key
				}
				out = append(out, candidate)
			}
		}
	}
	return out
}

func helmValuesSidecarCandidate(path, pointer string, item any) (SidecarCandidate, bool) {
	container, ok := item.(map[string]any)
	if !ok {
		return SidecarCandidate{}, false
	}
	name, _ := container["name"].(string)
	image, _ := container["image"].(string)
	name = strings.TrimSpace(name)
	image = strings.TrimSpace(image)
	if name == "" || image == "" || isRepoServerContainer(name) {
		return SidecarCandidate{}, false
	}
	return SidecarCandidate{
		Name:       name,
		Image:      image,
		Source:     "helm-values",
		Provenance: diagnostic.Provenance{Path: path, Pointer: pointer + ".image"},
		Signals:    sidecarSignals(container, nil),
	}, true
}

func dedupeSidecarCandidates(candidates []SidecarCandidate) []SidecarCandidate {
	seen := map[string]struct{}{}
	out := make([]SidecarCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		key := candidate.Name + "\x00" + candidate.Image + "\x00" + candidate.Provenance.Path + "\x00" + candidate.Provenance.Pointer
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, candidate)
	}
	return out
}

func shouldSkipDir(name string) bool {
	switch name {
	case ".git", ".hg", ".svn", "node_modules", ".terraform", ".drydock-cache":
		return true
	default:
		return false
	}
}

func isYAML(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".yaml", ".yml":
		return true
	default:
		return false
	}
}
