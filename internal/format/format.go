package format

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"go.yaml.in/yaml/v4"
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

func YAML(w io.Writer, value any) error {
	data, err := yaml.Marshal(value)
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
		for key, value := range row {
			cloned[key] = value
		}
		out[i] = cloned
	}
	return out
}
