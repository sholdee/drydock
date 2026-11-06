package format

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"reflect"
	"sort"
	"strings"
	"text/tabwriter"

	"go.yaml.in/yaml/v3"
)

type Output string

const (
	OutputTable Output = "table"
	OutputYAML  Output = "yaml"
	OutputJSON  Output = "json"
	OutputName  Output = "name"
)

type Column struct {
	Header string
	Key    string
}

func ParseOutput(value string) (Output, error) {
	switch output := Output(strings.TrimSpace(value)); output {
	case OutputTable, OutputYAML, OutputJSON, OutputName:
		return output, nil
	case "diff":
		return "", fmt.Errorf("diff output is only supported for diff commands")
	default:
		return "", fmt.Errorf("unsupported output %q", value)
	}
}

func Table(w io.Writer, columns []Column, rows []map[string]string) error {
	tableRows := cloneTableRows(rows)
	sort.Slice(tableRows, func(i, j int) bool {
		for _, column := range columns {
			left := tableRows[i][column.Key]
			right := tableRows[j][column.Key]
			if left == right {
				continue
			}
			return left < right
		}
		return false
	})

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	headers := make([]string, 0, len(columns))
	for _, column := range columns {
		headers = append(headers, column.Header)
	}
	if _, err := fmt.Fprintln(tw, strings.Join(headers, "\t")); err != nil {
		return err
	}
	for _, row := range tableRows {
		values := make([]string, 0, len(columns))
		for _, column := range columns {
			values = append(values, row[column.Key])
		}
		if _, err := fmt.Fprintln(tw, strings.Join(values, "\t")); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// MarshalYAML returns value encoded as YAML indented by 2 spaces. It is the
// only go.yaml.in/yaml/v3 encoder in drydock (a forbidigo rule keeps it that
// way). The width is not only style: at yaml.v3's default of 4, a block
// scalar inside a sequence item whose text starts with a newline or a space
// gets an indentation indicator that disagrees with the column its content is
// written at. Parsers reject such a document, or, when every line starts with
// the same spaces, silently read the text back without them.
//
// A string that starts with a tab and holds a line break is written in
// double-quoted style. yaml.v3 would write it as a literal block whose first
// content line starts with a tab, and yaml.v3, yaml.v2 and sigs.k8s.io/yaml
// (which kubectl uses) all reject that line with "found a tab character where
// an indentation space is expected".
func MarshalYAML(value any) ([]byte, error) {
	data, err := encodeYAML(value)
	if err != nil || !bytes.Contains(data, []byte{'\t'}) {
		return data, err
	}
	quoted, changed := quoteTabLeadingStrings(reflect.ValueOf(value))
	if !changed {
		return data, nil
	}
	return encodeYAML(quoted)
}

func encodeYAML(value any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := yaml.NewEncoder(&buffer)
	encoder.SetIndent(2)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

// doubleQuotedYAMLString marshals as a double-quoted YAML string.
type doubleQuotedYAMLString string

func (s doubleQuotedYAMLString) MarshalYAML() (any, error) {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Style: yaml.DoubleQuotedStyle, Value: string(s)}, nil
}

// quoteTabLeadingStrings returns value with every string that starts with a
// tab and holds a line break replaced by a doubleQuotedYAMLString, and
// reports whether it replaced any. It walks maps, slices, arrays, pointers
// and interfaces, the shapes of unstructured manifests and decoded YAML, and
// copies only the containers it changes. Structs are left as they are.
func quoteTabLeadingStrings(value reflect.Value) (any, bool) {
	if !value.IsValid() {
		return nil, false
	}
	kind := value.Kind()
	if kind == reflect.Interface || kind == reflect.Pointer {
		if value.IsNil() {
			return value.Interface(), false
		}
		if inner, changed := quoteTabLeadingStrings(value.Elem()); changed {
			return inner, true
		}
		return value.Interface(), false
	}
	if kind == reflect.String {
		text := value.String()
		if strings.HasPrefix(text, "\t") && strings.ContainsAny(text, "\n\r") {
			return doubleQuotedYAMLString(text), true
		}
		return value.Interface(), false
	}
	if kind == reflect.Map {
		return quoteTabLeadingMapValues(value)
	}
	isByteSlice := kind == reflect.Slice && value.Type().Elem().Kind() == reflect.Uint8
	if (kind == reflect.Slice || kind == reflect.Array) && !isByteSlice {
		return quoteTabLeadingElements(value)
	}
	// Scalars other than strings, byte slices, and structs stay as they are.
	return value.Interface(), false
}

// quoteTabLeadingMapValues applies quoteTabLeadingStrings to each map value.
// When one changes, it returns a map[K]any copy with the changed values.
func quoteTabLeadingMapValues(value reflect.Value) (any, bool) {
	var out reflect.Value
	iter := value.MapRange()
	for iter.Next() {
		element, changed := quoteTabLeadingStrings(iter.Value())
		if !changed {
			continue
		}
		if !out.IsValid() {
			out = reflect.MakeMapWithSize(reflect.MapOf(value.Type().Key(), anyType), value.Len())
			copyIter := value.MapRange()
			for copyIter.Next() {
				out.SetMapIndex(copyIter.Key(), copyIter.Value())
			}
		}
		out.SetMapIndex(iter.Key(), reflect.ValueOf(&element).Elem())
	}
	if !out.IsValid() {
		return value.Interface(), false
	}
	return out.Interface(), true
}

// quoteTabLeadingElements applies quoteTabLeadingStrings to each element of a
// slice or array. When one changes, it returns a []any copy.
func quoteTabLeadingElements(value reflect.Value) (any, bool) {
	var out []any
	for i := range value.Len() {
		element, changed := quoteTabLeadingStrings(value.Index(i))
		if !changed {
			continue
		}
		if out == nil {
			out = make([]any, value.Len())
			for j := range value.Len() {
				out[j] = value.Index(j).Interface()
			}
		}
		out[i] = element
	}
	if out == nil {
		return value.Interface(), false
	}
	return out, true
}

var anyType = reflect.TypeFor[any]()

// YAML writes value as YAML (see MarshalYAML) in a single write, so an
// encoding error leaves nothing behind and large documents do not reach w in
// yaml.v3's 128-byte flushes.
func YAML(w io.Writer, value any) error {
	data, err := MarshalYAML(value)
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

func YAMLMulti(w io.Writer, values []any) error {
	for _, value := range values {
		if _, err := fmt.Fprintln(w, "---"); err != nil {
			return err
		}
		if err := YAML(w, value); err != nil {
			return err
		}
	}
	return nil
}

func JSON(w io.Writer, value any) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func Name(w io.Writer, names []string) error {
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	for _, name := range sorted {
		if _, err := fmt.Fprintln(w, name); err != nil {
			return err
		}
	}
	return nil
}

func cloneTableRows(rows []map[string]string) []map[string]string {
	out := make([]map[string]string, len(rows))
	for i, row := range rows {
		cloned := make(map[string]string, len(row))
		maps.Copy(cloned, row)
		out[i] = cloned
	}
	return out
}
