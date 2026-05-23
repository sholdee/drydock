package diff

import (
	"sort"
	"strings"

	"go.yaml.in/yaml/v4"
)

func ExtractImages(docs []Document) []string {
	images := map[string]struct{}{}
	for _, doc := range docs {
		var value any
		if err := yaml.Unmarshal([]byte(doc.Body), &value); err != nil {
			continue
		}
		collectWorkloadImages(value, images)
	}

	out := make([]string, 0, len(images))
	for image := range images {
		out = append(out, image)
	}
	sort.Strings(out)
	return out
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
