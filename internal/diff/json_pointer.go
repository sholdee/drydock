package diff

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

var errJSONPointerRemovedRoot = errors.New("json pointer removed document root")

func validateJSONPointers(pointers []string) error {
	for _, pointer := range pointers {
		if _, err := jsonPointerTokens(pointer); err != nil {
			return err
		}
	}
	return nil
}
func removeJSONPointers(object map[string]any, pointers []string) (map[string]any, error) {
	var root any = object
	for _, pointer := range pointers {
		nextRoot, removedRoot, err := removeJSONPointer(root, pointer)
		if err != nil {
			return nil, err
		}
		if removedRoot {
			return nil, errJSONPointerRemovedRoot
		}
		root = nextRoot
	}
	typed, ok := root.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("json pointer normalization produced %T root, want object", root)
	}
	return typed, nil
}

// removeAnnotationJSONPointers removes pointers taken from a resource's
// argocd.argoproj.io/ignore-differences annotation. Argo CD turns each one into
// a JSON-patch remove operation applied by github.com/evanphx/json-patch and
// only logs a failed apply, so a pointer that cannot be applied is skipped
// instead of failing the diff or dropping the whole resource.
//
// Pointers are resolved with that library's path semantics rather than strict
// RFC 6901: the first "/"-separated segment is dropped without checking it
// (so "#/spec/replicas" and "x/metadata" address /spec/replicas and
// /metadata), "~1" and "~0" are decoded but other "~" sequences stay literal,
// and a negative array index -n addresses element len-n. A root ("") pointer
// has a single segment and is skipped.
func removeAnnotationJSONPointers(object map[string]any, pointers []string) map[string]any {
	for _, pointer := range pointers {
		tokens, ok := annotationJSONPointerTokens(pointer)
		if !ok {
			continue
		}
		next, _, err := removeJSONPointerValue(object, tokens, pointer, resolveAnnotationJSONPointerIndex)
		if err != nil {
			continue
		}
		if typed, ok := next.(map[string]any); ok {
			object = typed
		}
	}
	return object
}

// annotationJSONPointerKeyDecoder matches evanphx/json-patch decodePatchKey.
var annotationJSONPointerKeyDecoder = strings.NewReplacer("~1", "/", "~0", "~")

// annotationJSONPointerTokens splits a pointer the way evanphx/json-patch
// findObject does: split on "/", drop the first segment, decode the rest.
func annotationJSONPointerTokens(pointer string) ([]string, bool) {
	split := strings.Split(pointer, "/")
	if len(split) < 2 {
		return nil, false
	}
	tokens := make([]string, 0, len(split)-1)
	for _, part := range split[1:] {
		tokens = append(tokens, annotationJSONPointerKeyDecoder.Replace(part))
	}
	return tokens, true
}

// resolveAnnotationJSONPointerIndex parses an array index like
// evanphx/json-patch with SupportNegativeIndices enabled (its default): an
// index in [-length, -1] counts from the end. Any other out-of-range index is
// returned unchanged and treated as a missing element.
func resolveAnnotationJSONPointerIndex(token, pointer string, length int) (int, error) {
	index, err := strconv.Atoi(token)
	if err != nil {
		return 0, fmt.Errorf("invalid JSON pointer %q: invalid array index %q", pointer, token)
	}
	if index < 0 && index >= -length {
		index += length
	}
	return index, nil
}

// resolveStrictJSONPointerIndex parses an RFC 6901 array index.
func resolveStrictJSONPointerIndex(token, pointer string, _ int) (int, error) {
	return parseJSONPointerArrayIndex(token, pointer)
}

func removeJSONPointer(root any, pointer string) (any, bool, error) {
	tokens, err := jsonPointerTokens(pointer)
	if err != nil {
		return nil, false, err
	}
	if len(tokens) == 0 {
		return root, true, nil
	}
	nextRoot, _, err := removeJSONPointerValue(root, tokens, pointer, resolveStrictJSONPointerIndex)
	return nextRoot, false, err
}
func jsonPointerTokens(pointer string) ([]string, error) {
	if pointer == "" {
		return nil, nil
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, fmt.Errorf("invalid JSON pointer %q: must be empty or start with /", pointer)
	}
	parts := strings.Split(pointer[1:], "/")
	tokens := make([]string, 0, len(parts))
	for _, part := range parts {
		token, err := decodeJSONPointerToken(part, pointer)
		if err != nil {
			return nil, err
		}
		tokens = append(tokens, token)
	}
	return tokens, nil
}
func decodeJSONPointerToken(token, pointer string) (string, error) {
	var out strings.Builder
	for i := 0; i < len(token); i++ {
		if token[i] != '~' {
			out.WriteByte(token[i])
			continue
		}
		if i+1 >= len(token) {
			return "", fmt.Errorf("invalid JSON pointer %q: invalid escape in token %q", pointer, token)
		}
		switch token[i+1] {
		case '0':
			out.WriteByte('~')
		case '1':
			out.WriteByte('/')
		default:
			return "", fmt.Errorf("invalid JSON pointer %q: invalid escape in token %q", pointer, token)
		}
		i++
	}
	return out.String(), nil
}

// jsonPointerIndexResolver parses an array index token for an array of the
// given length. An index outside [0, length) means a missing element.
type jsonPointerIndexResolver func(token, pointer string, length int) (int, error)

func removeJSONPointerValue(current any, tokens []string, pointer string, resolveIndex jsonPointerIndexResolver) (any, bool, error) {
	if len(tokens) == 0 {
		return current, true, nil
	}
	if object, ok := asStringMap(current); ok {
		token := tokens[0]
		if len(tokens) == 1 {
			if _, exists := object[token]; !exists {
				return current, false, nil
			}
			delete(object, token)
			return object, true, nil
		}
		child, exists := object[token]
		if !exists {
			return current, false, nil
		}
		nextChild, removed, err := removeJSONPointerValue(child, tokens[1:], pointer, resolveIndex)
		if err != nil {
			return current, false, err
		}
		if removed {
			object[token] = nextChild
		}
		return object, removed, nil
	}
	if values, ok := current.([]any); ok {
		index, err := resolveIndex(tokens[0], pointer, len(values))
		if err != nil {
			return current, false, err
		}
		if index < 0 || index >= len(values) {
			return current, false, nil
		}
		if len(tokens) == 1 {
			values = append(values[:index], values[index+1:]...)
			return values, true, nil
		}
		nextChild, removed, err := removeJSONPointerValue(values[index], tokens[1:], pointer, resolveIndex)
		if err != nil {
			return current, false, err
		}
		if removed {
			values[index] = nextChild
		}
		return values, removed, nil
	}
	return current, false, nil
}
func asStringMap(value any) (map[string]any, bool) {
	switch typed := value.(type) {
	case map[string]any:
		return typed, true
	case map[any]any:
		converted := make(map[string]any, len(typed))
		for key, value := range typed {
			stringKey, ok := key.(string)
			if !ok {
				return nil, false
			}
			converted[stringKey] = value
		}
		return converted, true
	default:
		return nil, false
	}
}
func parseJSONPointerArrayIndex(token, pointer string) (int, error) {
	if token == "" {
		return 0, fmt.Errorf("invalid JSON pointer %q: empty array index", pointer)
	}
	index, err := strconv.Atoi(token)
	if err != nil || index < 0 {
		return 0, fmt.Errorf("invalid JSON pointer %q: invalid array index %q", pointer, token)
	}
	return index, nil
}
