package diff

import (
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"
)

func ExtractImages(docs []Document) []string {
	images := map[string]struct{}{}
	for _, doc := range docs {
		var value any
		if err := yaml.Unmarshal([]byte(doc.Body), &value); err != nil {
			continue
		}
		collectWorkloadImages(value, images)
		collectExactImageFields(value, images)
	}

	out := make([]string, 0, len(images))
	for image := range images {
		out = append(out, image)
	}
	sort.Strings(out)
	return out
}

func collectExactImageFields(value any, images map[string]struct{}) {
	kind, _ := stringField(value, "kind")
	if kind == "Secret" {
		return
	}
	collectExactImageFieldsAt(value, images, kind, nil)
}

func collectExactImageFieldsAt(value any, images map[string]struct{}, kind string, path []string) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			collectExactImageFieldChild(key, child, images, kind, path)
		}
	case map[any]any:
		for key, child := range typed {
			stringKey, ok := key.(string)
			if !ok {
				continue
			}
			collectExactImageFieldChild(stringKey, child, images, kind, path)
		}
	case []any:
		for _, child := range typed {
			collectExactImageFieldsAt(child, images, kind, path)
		}
	}
}

func collectExactImageFieldChild(key string, value any, images map[string]struct{}, kind string, path []string) {
	if skipExactImageFieldPath(kind, path, key) {
		return
	}
	if key == "image" {
		addImage(value, images)
		return
	}
	collectExactImageFieldsAt(value, images, kind, append(path, key))
}

func skipExactImageFieldPath(kind string, path []string, key string) bool {
	if len(path) != 0 {
		return false
	}
	if key == "metadata" || key == "status" {
		return true
	}
	return kind == "ConfigMap" && (key == "data" || key == "binaryData")
}

func collectWorkloadImages(value any, images map[string]struct{}) {
	kind, ok := stringField(value, "kind")
	if !ok {
		return
	}

	var podSpec any
	switch kind {
	case "Pod":
		podSpec = pathValue(value, "spec")
	case "Deployment", "StatefulSet", "DaemonSet", "ReplicaSet", "ReplicationController", "Job":
		podSpec = pathValue(value, "spec", "template", "spec")
	case "CronJob":
		podSpec = pathValue(value, "spec", "jobTemplate", "spec", "template", "spec")
	default:
		return
	}

	for _, field := range []string{"containers", "initContainers", "ephemeralContainers"} {
		for _, container := range listField(podSpec, field) {
			image, ok := stringField(container, "image")
			if !ok {
				continue
			}
			addImage(image, images)
		}
	}
}

func addImage(value any, images map[string]struct{}) {
	image, ok := value.(string)
	if !ok {
		return
	}
	image = strings.TrimSpace(image)
	if image == "" {
		return
	}
	images[image] = struct{}{}
}

func stringField(value any, key string) (string, bool) {
	field, ok := mapField(value, key)
	if !ok {
		return "", false
	}
	out, ok := field.(string)
	return out, ok
}

func listField(value any, key string) []any {
	field, ok := mapField(value, key)
	if !ok {
		return nil
	}
	out, ok := field.([]any)
	if !ok {
		return nil
	}
	return out
}

func pathValue(value any, keys ...string) any {
	current := value
	for _, key := range keys {
		next, ok := mapField(current, key)
		if !ok {
			return nil
		}
		current = next
	}
	return current
}

func mapField(value any, key string) (any, bool) {
	switch typed := value.(type) {
	case map[string]any:
		out, ok := typed[key]
		return out, ok
	case map[any]any:
		out, ok := typed[key]
		return out, ok
	default:
		return nil, false
	}
}
