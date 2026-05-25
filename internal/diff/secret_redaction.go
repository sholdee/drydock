package diff

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
)

func redactedSecretBodies(from, to string) (string, string, error) {
	fromObject, err := decodeDiffYAML(from)
	if err != nil {
		if !errors.Is(err, errEmptyDiffBody) {
			return "", "", fmt.Errorf("redact Secret before body: %w", err)
		}
	}
	toObject, err := decodeDiffYAML(to)
	if err != nil {
		if !errors.Is(err, errEmptyDiffBody) {
			return "", "", fmt.Errorf("redact Secret after body: %w", err)
		}
	}

	redactSecretFieldPair(fromObject, toObject, "data")
	redactSecretFieldPair(fromObject, toObject, "stringData")
	redactSecretFieldPair(fromObject, toObject, "binaryData")

	redactedFrom, err := encodeDiffYAML(fromObject)
	if err != nil {
		return "", "", fmt.Errorf("encode redacted Secret before body: %w", err)
	}
	redactedTo, err := encodeDiffYAML(toObject)
	if err != nil {
		return "", "", fmt.Errorf("encode redacted Secret after body: %w", err)
	}
	return redactedFrom, redactedTo, nil
}
func redactSecretFieldPair(fromObject, toObject map[string]any, field string) {
	fromField := secretStringMapField(fromObject, field)
	toField := secretStringMapField(toObject, field)
	if !fromField.present && !toField.present {
		return
	}

	if fromField.malformed || toField.malformed {
		redactMalformedSecretFieldPair(fromObject, toObject, field, fromField, toField)
		return
	}

	keys := mapKeys(fromField.values, toField.values)
	for _, key := range keys {
		fromValue, hasFrom := fromField.values[key]
		toValue, hasTo := toField.values[key]
		switch {
		case hasFrom && hasTo && reflect.DeepEqual(fromValue, toValue):
			fromField.values[key] = "<redacted>"
			toField.values[key] = "<redacted>"
		case hasFrom && hasTo:
			fromField.values[key] = "<redacted-before>"
			toField.values[key] = "<redacted-after>"
		case hasFrom:
			fromField.values[key] = "<redacted-removed>"
		case hasTo:
			toField.values[key] = "<redacted-added>"
		}
	}
}

type secretField struct {
	present   bool
	malformed bool
	values    map[string]any
	raw       any
}

func secretStringMapField(object map[string]any, field string) secretField {
	if object == nil {
		return secretField{}
	}
	values, ok := object[field]
	if !ok {
		return secretField{}
	}

	switch typed := values.(type) {
	case map[string]any:
		if !allStringValues(typed) {
			return secretField{present: true, malformed: true, raw: values}
		}
		return secretField{present: true, values: typed, raw: values}
	case map[any]any:
		converted := make(map[string]any, len(typed))
		for key, value := range typed {
			stringKey, ok := key.(string)
			if !ok {
				return secretField{present: true, malformed: true, raw: values}
			}
			if _, ok := value.(string); !ok {
				return secretField{present: true, malformed: true, raw: values}
			}
			converted[stringKey] = value
		}
		object[field] = converted
		return secretField{present: true, values: converted, raw: values}
	default:
		return secretField{present: true, malformed: true, raw: values}
	}
}
func allStringValues(values map[string]any) bool {
	for _, value := range values {
		if _, ok := value.(string); !ok {
			return false
		}
	}
	return true
}
func redactMalformedSecretFieldPair(fromObject, toObject map[string]any, field string, fromField, toField secretField) {
	if fromField.present && toField.present {
		if fromField.malformed && toField.malformed && reflect.DeepEqual(fromField.raw, toField.raw) {
			fromObject[field] = "<redacted-malformed>"
			toObject[field] = "<redacted-malformed>"
			return
		}
		fromObject[field] = "<redacted-malformed-before>"
		toObject[field] = "<redacted-malformed-after>"
		return
	}
	if fromField.present {
		fromObject[field] = "<redacted-malformed-removed>"
		return
	}
	toObject[field] = "<redacted-malformed-added>"
}
func mapKeys(left, right map[string]any) []string {
	seen := make(map[string]struct{}, len(left)+len(right))
	for key := range left {
		seen[key] = struct{}{}
	}
	for key := range right {
		seen[key] = struct{}{}
	}
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
