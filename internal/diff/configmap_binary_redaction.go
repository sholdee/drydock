package diff

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

func redactedConfigMapBinaryDataBodies(from, to string) (string, string, error) {
	if !hasTopLevelBinaryDataMarker(from) && !hasTopLevelBinaryDataMarker(to) {
		return from, to, nil
	}

	fromObject, _, err := decodeOptionalDiffYAML(from, "summarize ConfigMap before body")
	if err != nil {
		return "", "", err
	}
	toObject, _, err := decodeOptionalDiffYAML(to, "summarize ConfigMap after body")
	if err != nil {
		return "", "", err
	}

	summarizedFrom := summarizeConfigMapBinaryData(fromObject)
	summarizedTo := summarizeConfigMapBinaryData(toObject)
	if !summarizedFrom && !summarizedTo {
		return from, to, nil
	}

	redactedFrom, err := encodeDiffYAML(fromObject)
	if err != nil {
		return "", "", fmt.Errorf("encode summarized ConfigMap before body: %w", err)
	}
	redactedTo, err := encodeDiffYAML(toObject)
	if err != nil {
		return "", "", fmt.Errorf("encode summarized ConfigMap after body: %w", err)
	}
	return redactedFrom, redactedTo, nil
}

func hasTopLevelBinaryDataMarker(body string) bool {
	for _, line := range strings.Split(body, "\n") {
		if line == "" || line[0] == ' ' || line[0] == '\t' {
			continue
		}
		field, _, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		field = strings.Trim(strings.TrimSpace(field), `"'`)
		if field == "binaryData" {
			return true
		}
	}
	return false
}

func summarizeConfigMapBinaryDataDocumentBodies(leftBody, rightBody string, left, right Document, hasLeft, hasRight bool) (string, string, error) {
	resource := right.Resource
	if !hasRight {
		resource = left.Resource
	}
	if resource.Group != "" || resource.Kind != "ConfigMap" {
		return leftBody, rightBody, nil
	}
	return redactedConfigMapBinaryDataBodies(leftBody, rightBody)
}

func decodeOptionalDiffYAML(body, context string) (map[string]any, bool, error) {
	object, err := decodeDiffYAML(body)
	if err != nil {
		if errors.Is(err, errEmptyDiffBody) {
			return nil, true, nil
		}
		return nil, false, fmt.Errorf("%s: %w", context, err)
	}
	return object, false, nil
}

func summarizeConfigMapBinaryData(object map[string]any) bool {
	if !isCoreConfigMapObject(object) {
		return false
	}
	raw, ok := object["binaryData"]
	if !ok {
		return false
	}

	switch typed := raw.(type) {
	case map[string]any:
		for key, value := range typed {
			typed[key] = binaryDataValueSummary(value)
		}
	case map[any]any:
		converted := make(map[string]any, len(typed))
		for key, value := range typed {
			stringKey, ok := key.(string)
			if !ok {
				object["binaryData"] = malformedBinaryDataSummary(raw, "invalid-map")
				return true
			}
			converted[stringKey] = binaryDataValueSummary(value)
		}
		object["binaryData"] = converted
	default:
		object["binaryData"] = malformedBinaryDataSummary(raw, "invalid-field")
	}
	return true
}

func isCoreConfigMapObject(object map[string]any) bool {
	if object == nil {
		return false
	}
	apiVersion, _ := object["apiVersion"].(string)
	kind, _ := object["kind"].(string)
	return apiVersion == "v1" && kind == "ConfigMap"
}

func binaryDataSummary(value string) string {
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err == nil {
		return fmt.Sprintf("<redacted binary data: %d bytes sha256:%s>", len(decoded), shortSHA256(decoded))
	}
	return fmt.Sprintf("<redacted binary data: invalid-base64 %d chars sha256:%s>", utf8.RuneCountInString(value), shortSHA256([]byte(value)))
}

func binaryDataValueSummary(value any) string {
	if text, ok := value.(string); ok {
		return binaryDataSummary(text)
	}
	return malformedBinaryDataSummary(value, "invalid-value")
}

func malformedBinaryDataSummary(value any, reason string) string {
	body := []byte(fmt.Sprintf("%#v", value))
	return fmt.Sprintf("<redacted binary data: %s %d bytes sha256:%s>", reason, len(body), shortSHA256(body))
}

func shortSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum)[:16]
}
