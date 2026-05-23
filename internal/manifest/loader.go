package manifest

import (
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"time"

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
		var raw any
		err := dec.Decode(&raw)
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("%s document %d: decode YAML document failed", path, index)
		}
		if raw == nil {
			index++
			continue
		}

		normalized, err := normalizeYAMLValue(raw)
		if err != nil {
			return nil, fmt.Errorf("%s document %d: %w", path, index, err)
		}
		normalizedMap, ok := normalized.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s document %d decoded to unsupported root type %T", path, index, normalized)
		}
		if len(normalizedMap) == 0 {
			index++
			continue
		}

		obj := &unstructured.Unstructured{Object: normalizedMap}
		if obj.GetKind() == "List" {
			itemsRaw, ok := obj.Object["items"]
			if ok {
				items, ok := itemsRaw.([]any)
				if !ok {
					return nil, fmt.Errorf("%s document %d /items is not a list", path, index)
				}
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

func normalizeYAMLValue(value any) (any, error) {
	switch typed := value.(type) {
	case map[string]any:
		return normalizeYAMLStringMap(typed)
	case map[any]any:
		return normalizeYAMLAnyMap(typed)
	case []any:
		return normalizeYAMLSlice(typed)
	default:
		return normalizeYAMLScalar(typed)
	}
}

func normalizeYAMLStringMap(values map[string]any) (map[string]any, error) {
	normalized := make(map[string]any, len(values))
	for key, child := range values {
		value, err := normalizeYAMLValue(child)
		if err != nil {
			return nil, err
		}
		normalized[key] = value
	}
	return normalized, nil
}

func normalizeYAMLAnyMap(values map[any]any) (map[string]any, error) {
	normalized := make(map[string]any, len(values))
	for key, child := range values {
		stringKey, ok := key.(string)
		if !ok {
			return nil, fmt.Errorf("YAML object key has unsupported type %T", key)
		}
		value, err := normalizeYAMLValue(child)
		if err != nil {
			return nil, err
		}
		normalized[stringKey] = value
	}
	return normalized, nil
}

func normalizeYAMLSlice(values []any) ([]any, error) {
	normalized := make([]any, len(values))
	for i, child := range values {
		value, err := normalizeYAMLValue(child)
		if err != nil {
			return nil, err
		}
		normalized[i] = value
	}
	return normalized, nil
}

func normalizeYAMLScalar(value any) (any, error) {
	switch typed := value.(type) {
	case int, int8, int16, int32, int64:
		return normalizeSignedYAMLInteger(reflect.ValueOf(typed).Int()), nil
	case uint, uint8, uint16, uint32, uint64:
		return normalizeUnsignedYAMLInteger(reflect.ValueOf(typed).Uint())
	case float32:
		return float64(typed), nil
	case time.Time:
		return typed.Format(time.RFC3339Nano), nil
	case float64, string, bool, nil:
		return typed, nil
	default:
		return nil, fmt.Errorf("YAML value has unsupported type %T", value)
	}
}

func normalizeSignedYAMLInteger(value int64) int64 {
	return value
}

func normalizeUnsignedYAMLInteger(value uint64) (int64, error) {
	if value > math.MaxInt64 {
		return 0, fmt.Errorf("YAML integer overflows int64")
	}
	return int64(value), nil
}
