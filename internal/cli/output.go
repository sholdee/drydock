package cli

import (
	"fmt"
	"io"
	"strings"

	cliformat "github.com/sholdee/drydock/internal/format"
)

const (
	diffOutputUnified  = "diff"
	diffOutputMarkdown = "markdown"
)

const testOutputText = "text"

func parseDiffOutput(value, command string) (string, error) {
	output := strings.TrimSpace(value)
	switch output {
	case "", diffOutputUnified:
		return diffOutputUnified, nil
	case string(cliformat.OutputJSON), string(cliformat.OutputYAML), diffOutputMarkdown:
		return output, nil
	case string(cliformat.OutputName):
		return "", fmt.Errorf("name output is not supported for %s", command)
	default:
		return "", fmt.Errorf("unsupported output %q for %s", value, command)
	}
}

func parseImageDiffOutput(value string) (string, error) {
	output := strings.TrimSpace(value)
	if output == string(cliformat.OutputName) {
		return output, nil
	}
	return parseDiffOutput(value, "diff images")
}

func parseTestOutput(value string) (string, error) {
	output := strings.TrimSpace(value)
	switch output {
	case "", testOutputText:
		return testOutputText, nil
	case string(cliformat.OutputJSON), string(cliformat.OutputYAML):
		return output, nil
	default:
		return "", fmt.Errorf("unsupported output %q for test", value)
	}
}

func parseDiagOutput(value string) (string, error) {
	switch strings.TrimSpace(value) {
	case "", diffOutputUnified, testOutputText:
		return testOutputText, nil
	case string(cliformat.OutputJSON):
		return string(cliformat.OutputJSON), nil
	case string(cliformat.OutputYAML):
		return string(cliformat.OutputYAML), nil
	default:
		return "", fmt.Errorf("diag output supports text, json, or yaml, got %q", value)
	}
}

func writeStructuredOutput(w io.Writer, output string, value any) error {
	switch output {
	case string(cliformat.OutputJSON):
		return cliformat.JSON(w, value)
	case string(cliformat.OutputYAML):
		return cliformat.YAML(w, value)
	default:
		return fmt.Errorf("unsupported structured output %q", output)
	}
}
