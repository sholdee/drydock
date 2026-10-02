package format

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
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
func MarshalYAML(value any) ([]byte, error) {
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
