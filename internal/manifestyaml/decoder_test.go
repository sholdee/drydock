package manifestyaml

import (
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
)

func decodeAll(input string) ([]any, error) {
	decoder := NewDecoder(strings.NewReader(input))
	var values []any
	for {
		value, err := decoder.Decode()
		if errors.Is(err, io.EOF) {
			return values, nil
		}
		if err != nil {
			return values, err
		}
		values = append(values, value)
	}
}

func TestDecodeJSONStreamSyntaxErrorDoesNotIncludeManifestValue(t *testing.T) {
	// After two JSON objects apimachinery treats the stream as JSON and
	// returns the third object's *json.SyntaxError without wrapping it.
	values, err := decodeAll(`{"a":1}{"b":2}{"c": secret-token-value}`)
	if err == nil {
		t.Fatal("Decode() error = nil, want syntax error")
	}
	want := []any{map[string]any{"a": int64(1)}, map[string]any{"b": int64(2)}}
	if !reflect.DeepEqual(values, want) {
		t.Fatalf("values before the error = %#v, want %#v", values, want)
	}
	if !strings.Contains(err.Error(), "invalid JSON at offset") {
		t.Fatalf("error = %q, want JSON offset message", err)
	}
	if strings.Contains(err.Error(), "'s'") || strings.Contains(err.Error(), "secret-token-value") {
		t.Fatalf("error leaked manifest data: %q", err)
	}
}

func TestDecodeComplexKeyErrorDoesNotIncludeManifestValue(t *testing.T) {
	// goyaml.v2 reports a sequence key with its value ("invalid map key:
	// []interface {}{...}"), and sigs.k8s.io/yaml passes the message on.
	_, err := decodeAll("? [secret-token-value, other]\n: value\n")
	if err == nil {
		t.Fatal("Decode() error = nil, want invalid key error")
	}
	if !strings.Contains(err.Error(), "YAML object key is not a string, number, or boolean") {
		t.Fatalf("error = %q, want unsupported key message", err)
	}
	if strings.Contains(err.Error(), "secret-token-value") {
		t.Fatalf("error leaked manifest data: %q", err)
	}
}
