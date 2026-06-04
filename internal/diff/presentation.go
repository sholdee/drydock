package diff

import (
	"fmt"
	"strings"

	"github.com/pmezard/go-difflib/difflib"
)

func unified(doc Document, from, to string, opts Options) (string, error) {
	header := headerOf(doc)
	displayFrom, displayTo, err := displayBodies(doc, from, to)
	if err != nil {
		return "", fmt.Errorf("diff %s: %w", header, err)
	}
	diff, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A:        splitUnifiedLines(displayFrom),
		B:        splitUnifiedLines(displayTo),
		FromFile: header,
		ToFile:   header,
		Context:  opts.Unified,
	})
	if err != nil {
		return "", fmt.Errorf("diff %s: %w", header, err)
	}
	return diff, nil
}

func splitUnifiedLines(text string) []string {
	if text == "" {
		return nil
	}
	lines := difflib.SplitLines(text)
	if strings.HasSuffix(text, "\n") && len(lines) > 0 && lines[len(lines)-1] == "\n" {
		return lines[:len(lines)-1]
	}
	return lines
}

func displayBodies(doc Document, from, to string) (string, string, error) {
	if doc.Resource.Kind == "Secret" {
		return redactedSecretBodies(from, to)
	}
	return from, to, nil
}
