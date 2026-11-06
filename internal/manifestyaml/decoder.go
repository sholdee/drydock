// Package manifestyaml decodes YAML and JSON manifest streams the way kubectl
// and Argo CD read them.
package manifestyaml

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"k8s.io/apimachinery/pkg/runtime"
	utiljson "k8s.io/apimachinery/pkg/util/json"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
)

// jsonGuessSize is how far into a stream kubectl and gitops-engine let
// NewYAMLOrJSONDecoder look to tell JSON from YAML.
const jsonGuessSize = 4096

// Decoder reads the documents of a manifest stream as kubectl's StreamVisitor
// (k8s.io/cli-runtime pkg/resource/visitor.go) and Argo CD's directory
// source do. apimachinery's YAMLOrJSONDecoder splits the stream on
// "---" lines and converts each YAML document to JSON through
// sigs.k8s.io/yaml, which parses YAML 1.1: unquoted y/yes/on and n/no/off are
// booleans, integer, float and boolean map keys become strings, timestamps
// keep their text, and a duplicate key keeps its last value. The JSON is then
// decoded as unstructured objects are, so integers stay int64.
type Decoder struct {
	decoder *utilyaml.YAMLOrJSONDecoder
}

// NewDecoder returns a Decoder for manifests Argo CD receives through kubectl
// or reads from a directory source.
func NewDecoder(reader io.Reader) *Decoder {
	return &Decoder{decoder: utilyaml.NewYAMLOrJSONDecoder(reader, jsonGuessSize)}
}

// Decode returns the next document's value: a map[string]any, []any, string,
// int64, float64 or bool, or nil for an empty or null document. It returns
// io.EOF after the last document. Each call that returns a nil error consumes
// one document, empty ones included, so callers can address a document by
// the number of calls that preceded it.
func (d *Decoder) Decode() (any, error) {
	var ext runtime.RawExtension
	if err := d.decoder.Decode(&ext); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, io.EOF
		}
		return nil, SanitizeDecodeError(err)
	}
	raw := bytes.TrimSpace(ext.Raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, nil //nolint:nilnil // An empty document has no value but still takes an index.
	}
	var value any
	if err := utiljson.Unmarshal(raw, &value); err != nil {
		return nil, SanitizeDecodeError(err)
	}
	return value, nil
}

// SanitizeDecodeError drops the manifest content that apimachinery,
// sigs.k8s.io/yaml and sigs.k8s.io/json copy into some errors: the text after
// a "---" separator, a map key JSON cannot represent and its entry's value,
// an out-of-range number, and the offending character of a JSON syntax error.
func SanitizeDecodeError(err error) error {
	message := err.Error()
	switch {
	case strings.Contains(message, "invalid Yaml document separator"):
		return errors.New("invalid YAML document separator: only a comment may follow --- on its line")
	case strings.Contains(message, "unsupported map key of type"), strings.Contains(message, "invalid map key"):
		return errors.New("YAML object key is not a string, number, or boolean")
	case strings.Contains(message, "cannot unmarshal number"):
		return errors.New("JSON number is out of range")
	}
	// apimachinery wraps a JSON syntax error in JSONSyntaxError, except once
	// a stream has yielded more than one JSON object, when it returns the
	// *json.SyntaxError as is.
	if syntaxErr, ok := errors.AsType[utilyaml.JSONSyntaxError](err); ok {
		return fmt.Errorf("invalid JSON at offset %d", syntaxErr.Offset)
	}
	if syntaxErr, ok := errors.AsType[*json.SyntaxError](err); ok {
		return fmt.Errorf("invalid JSON at offset %d", syntaxErr.Offset)
	}
	return err
}
