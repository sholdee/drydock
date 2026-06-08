package avpcompat

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

const redactedPrefix = "drydock-redacted-"

// ContainsPlaceholder reports whether value contains a supported AVP placeholder.
func ContainsPlaceholder(value string) bool {
	_, changed := ReplaceString(value)
	return changed
}

// ReplaceString replaces supported AVP placeholders with stable redacted values.
func ReplaceString(value string) (string, bool) {
	return replaceString(value, "")
}

// ReplaceStringWithPath replaces inline path placeholders and annotation-scoped
// generic placeholders with stable redacted values.
func ReplaceStringWithPath(value string, defaultPath string) (string, bool) {
	return replaceString(value, strings.TrimSpace(defaultPath))
}

func replaceString(value string, defaultPath string) (string, bool) {
	if !strings.Contains(value, "<") || !strings.Contains(value, ">") {
		return value, false
	}
	if defaultPath == "" && !strings.Contains(value, "path:") {
		return value, false
	}

	var out strings.Builder
	changed := false
	last := 0
	scan := 0

	for scan < len(value) {
		startRel := strings.IndexByte(value[scan:], '<')
		if startRel == -1 {
			break
		}
		start := scan + startRel
		endRel := strings.IndexByte(value[start+1:], '>')
		if endRel == -1 {
			break
		}
		end := start + 1 + endRel
		token := value[start : end+1]

		identity, ok := placeholderIdentity(token, defaultPath)
		if ok {
			if !changed {
				out.Grow(len(value))
			}
			out.WriteString(value[last:start])
			out.WriteString(redactedValue(identity))
			last = end + 1
			changed = true
		}

		scan = end + 1
	}

	if !changed {
		return value, false
	}
	out.WriteString(value[last:])
	return out.String(), true
}

// ReplaceValue replaces supported AVP placeholders through decoded YAML/JSON values.
func ReplaceValue(value any) (any, bool) {
	return replaceValue(value, "")
}

// ReplaceValueWithPath replaces supported AVP placeholders through decoded
// YAML/JSON values, including generic placeholders scoped to an AVP path
// annotation.
func ReplaceValueWithPath(value any, defaultPath string) (any, bool) {
	return replaceValue(value, strings.TrimSpace(defaultPath))
}

func replaceValue(value any, defaultPath string) (any, bool) {
	switch typed := value.(type) {
	case string:
		return replaceString(typed, defaultPath)
	case []any:
		return replaceAnySlice(typed, defaultPath)
	case []string:
		return replaceStringSlice(typed, defaultPath)
	case map[string]any:
		return replaceStringAnyMap(typed, defaultPath)
	case map[string]string:
		return replaceStringStringMap(typed, defaultPath)
	case map[any]any:
		return replaceAnyMap(typed, defaultPath)
	default:
		return value, false
	}
}

func replaceAnySlice(values []any, defaultPath string) (any, bool) {
	replaced := make([]any, len(values))
	changed := false
	for i, item := range values {
		next, itemChanged := replaceValue(item, defaultPath)
		replaced[i] = next
		changed = changed || itemChanged
	}
	if !changed {
		return values, false
	}
	return replaced, true
}

func replaceStringSlice(values []string, defaultPath string) (any, bool) {
	replaced := make([]string, len(values))
	changed := false
	for i, item := range values {
		next, itemChanged := replaceString(item, defaultPath)
		replaced[i] = next
		changed = changed || itemChanged
	}
	if !changed {
		return values, false
	}
	return replaced, true
}

func replaceStringAnyMap(values map[string]any, defaultPath string) (any, bool) {
	replaced := make(map[string]any, len(values))
	changed := false
	for key, item := range values {
		next, itemChanged := replaceValue(item, defaultPath)
		replaced[key] = next
		changed = changed || itemChanged
	}
	if !changed {
		return values, false
	}
	return replaced, true
}

func replaceStringStringMap(values map[string]string, defaultPath string) (any, bool) {
	replaced := make(map[string]string, len(values))
	changed := false
	for key, item := range values {
		next, itemChanged := replaceString(item, defaultPath)
		replaced[key] = next
		changed = changed || itemChanged
	}
	if !changed {
		return values, false
	}
	return replaced, true
}

func replaceAnyMap(values map[any]any, defaultPath string) (any, bool) {
	replaced := make(map[any]any, len(values))
	changed := false
	for key, item := range values {
		next, itemChanged := replaceValue(item, defaultPath)
		replaced[key] = next
		changed = changed || itemChanged
	}
	if !changed {
		return values, false
	}
	return replaced, true
}

func placeholderIdentity(token string, defaultPath string) (string, bool) {
	if identity, ok := pathPlaceholderIdentity(token); ok {
		return identity, true
	}
	if defaultPath == "" {
		return "", false
	}
	key, ok := genericPlaceholderKey(token)
	if !ok {
		return "", false
	}
	return "path:" + defaultPath + "#" + key, true
}

func pathPlaceholderIdentity(token string) (string, bool) {
	if len(token) < len("<path:a#b>") || token[0] != '<' || token[len(token)-1] != '>' {
		return "", false
	}

	body := strings.TrimSpace(token[1 : len(token)-1])
	if !strings.HasPrefix(body, "path:") {
		return "", false
	}

	selector := strings.TrimSpace(strings.TrimPrefix(body, "path:"))
	pathPart, keyPart, ok := strings.Cut(selector, "#")
	if !ok || strings.TrimSpace(pathPart) == "" || strings.TrimSpace(keyPart) == "" {
		return "", false
	}
	return "path:" + selector, true
}

func genericPlaceholderKey(token string) (string, bool) {
	if len(token) < len("<a>") || token[0] != '<' || token[len(token)-1] != '>' {
		return "", false
	}
	key := strings.TrimSpace(token[1 : len(token)-1])
	if key == "" || strings.HasPrefix(key, "path:") {
		return "", false
	}
	for _, r := range key {
		if !isGenericPlaceholderKeyRune(r) {
			return "", false
		}
	}
	return key, true
}

func isGenericPlaceholderKeyRune(r rune) bool {
	return r >= 'a' && r <= 'z' ||
		r >= 'A' && r <= 'Z' ||
		r >= '0' && r <= '9' ||
		r == '_' ||
		r == '-' ||
		r == '.' ||
		r == '/'
}

func redactedValue(identity string) string {
	sum := sha256.Sum256([]byte(identity))
	return redactedPrefix + hex.EncodeToString(sum[:])[:12]
}
