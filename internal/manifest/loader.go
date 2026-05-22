package manifest

import (
	"errors"
	"fmt"
	"io"

	"go.yaml.in/yaml/v4"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type Document struct {
	Path   string
	Index  int
	Object *unstructured.Unstructured
}

func DecodeDocuments(path string, reader io.Reader) ([]Document, error) {
	dec := yaml.NewDecoder(reader)
	var out []Document
	index := 0
	for {
		var raw map[string]any
		err := dec.Decode(&raw)
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("%s document %d: %w", path, index, err)
		}
		if len(raw) == 0 {
			index++
			continue
		}

		obj := &unstructured.Unstructured{Object: raw}
		if obj.GetKind() == "List" {
			items, ok, err := unstructured.NestedSlice(obj.Object, "items")
			if err != nil {
				return nil, fmt.Errorf("%s document %d /items: %w", path, index, err)
			}
			if ok {
				for i, item := range items {
					itemMap, ok := item.(map[string]any)
					if !ok {
						return nil, fmt.Errorf("%s document %d list item /items/%d is not an object", path, index, i)
					}
					out = append(out, Document{
						Path:   path,
						Index:  index,
						Object: &unstructured.Unstructured{Object: itemMap},
					})
				}
			}
			index++
			continue
		}

		out = append(out, Document{
			Path:   path,
			Index:  index,
			Object: obj,
		})
		index++
	}
}
