package manifest

import (
	"errors"
	"fmt"
	"io"

	"github.com/sholdee/drydock/internal/manifestyaml"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type Document struct {
	Path       string
	Index      int
	Object     *unstructured.Unstructured
	RootObject *unstructured.Unstructured
}

// DecodeDocuments decodes a YAML or JSON manifest stream the way kubectl and
// Argo CD's directory source read it (see manifestyaml.Decoder) and flattens
// List documents into their items.
func DecodeDocuments(path string, reader io.Reader) ([]Document, error) {
	return decodeDocuments(path, manifestyaml.NewDecoder(reader), true)
}

// DecodeDocumentRoots is DecodeDocuments without List flattening.
func DecodeDocumentRoots(path string, reader io.Reader) ([]Document, error) {
	return decodeDocuments(path, manifestyaml.NewDecoder(reader), false)
}

type documentDecoder interface {
	// Decode returns the next document's value, nil for an empty document,
	// or io.EOF after the last document.
	Decode() (any, error)
}

func decodeDocuments(path string, decoder documentDecoder, flattenLists bool) ([]Document, error) {
	var out []Document
	for index := 0; ; index++ {
		value, err := decoder.Decode()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("%s document %d: decode YAML document failed: %w", path, index, err)
		}
		if value == nil {
			continue
		}
		object, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s document %d decoded to unsupported root type %T", path, index, value)
		}
		if len(object) == 0 {
			continue
		}

		obj := &unstructured.Unstructured{Object: object}
		if flattenLists {
			flattened, skip, err := flattenItemsField(path, index, obj)
			if err != nil {
				return nil, err
			}
			if skip {
				continue
			}
			if flattened != nil {
				out = append(out, flattened...)
				continue
			}
		}

		out = append(out, Document{
			Path:       path,
			Index:      index,
			Object:     obj,
			RootObject: obj,
		})
	}
}

// flattenItemsField implements the list split in Argo CD v3.6.1
// GenerateManifests (reposerver/repository/repository.go:1817-1840). It
// splits by the shape of the items field, not by kind. An object whose items
// field is an array is a list (Unstructured.IsList), and each item must be an
// object (Unstructured.EachListItem). An object whose items field is null and
// that has neither spec nor status emits nothing (isNullList). Every other
// object, including one whose items field is a scalar or a map, is emitted
// unchanged.
// Returns (flattened docs, skip, error). When flattened is non-nil the caller
// should append them. When skip is true the caller should discard the document.
func flattenItemsField(path string, index int, obj *unstructured.Unstructured) ([]Document, bool, error) {
	itemsRaw, hasItems := obj.Object["items"]
	if !hasItems {
		return nil, false, nil
	}
	if items, isList := itemsRaw.([]any); isList {
		flattened := make([]Document, 0, len(items))
		for i, item := range items {
			itemMap, ok := item.(map[string]any)
			if !ok {
				return nil, false, fmt.Errorf("%s document %d list item /items/%d is not an object", path, index, i)
			}
			flattened = append(flattened, Document{
				Path:       path,
				Index:      index,
				Object:     &unstructured.Unstructured{Object: itemMap},
				RootObject: obj,
			})
		}
		return flattened, false, nil
	}
	if itemsRaw == nil {
		_, hasSpec := obj.Object["spec"]
		_, hasStatus := obj.Object["status"]
		return nil, !hasSpec && !hasStatus, nil
	}
	return nil, false, nil
}
