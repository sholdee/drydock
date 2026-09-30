package manifest

import (
	"fmt"
	"io"
	"math"
	"reflect"
	"time"

	"go.yaml.in/yaml/v3"
)

// DecodeYAML12Documents decodes a manifest stream with YAML 1.2 rules
// (go.yaml.in/yaml/v3), the reading kustomize gives the resources it
// transforms: unquoted yes/no/on/off stay strings and timestamps become
// RFC 3339 strings. kustomize keeps metadata labels and annotations as the
// text they were written with, which this reading does not reproduce. It
// exists for manifests drydock hands to kustomize as input; everything Argo
// CD itself reads goes through DecodeDocuments or DecodeGeneratedDocuments.
func DecodeYAML12Documents(path string, reader io.Reader) ([]Document, error) {
	return decodeDocuments(path, yaml12Decoder{decoder: yaml.NewDecoder(reader)}, true)
}

type yaml12Decoder struct {
	decoder *yaml.Decoder
}

func (d yaml12Decoder) Decode() (any, error) {
	var raw any
	if err := d.decoder.Decode(&raw); err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, nil //nolint:nilnil // An empty document has no value but still takes an index.
	}
	return normalizeYAMLValue(raw)
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
		return reflect.ValueOf(typed).Int(), nil
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

func normalizeUnsignedYAMLInteger(value uint64) (int64, error) {
	if value > math.MaxInt64 {
		return 0, fmt.Errorf("YAML integer overflows int64")
	}
	return int64(value), nil
}
