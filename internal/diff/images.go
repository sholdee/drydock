package diff

import (
	"sort"
	"strings"

	"go.yaml.in/yaml/v4"
)

func ExtractImages(body string) ([]string, error) {
	var value any
	if err := yaml.Unmarshal([]byte(body), &value); err != nil {
		return nil, err
	}

	images := map[string]struct{}{}
	collectImages(value, images)

	out := make([]string, 0, len(images))
	for image := range images {
		out = append(out, image)
	}
	sort.Strings(out)
	return out, nil
}

func collectImages(value any, images map[string]struct{}) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if key == "image" {
				addImage(child, images)
			}
			collectImages(child, images)
		}
	case map[any]any:
		for key, child := range typed {
			if key == "image" {
				addImage(child, images)
			}
			collectImages(child, images)
		}
	case []any:
		for _, child := range typed {
			collectImages(child, images)
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
