package diff

import (
	"fmt"
	"github.com/pmezard/go-difflib/difflib"
)

func unified(doc Document, from, to string, opts Options) (string, error) {
	header := headerOf(doc)
	displayFrom, displayTo, err := displayBodies(doc, from, to)
	if err != nil {
		return "", fmt.Errorf("diff %s: %w", header, err)
	}
	diff, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A:        difflib.SplitLines(displayFrom),
		B:        difflib.SplitLines(displayTo),
		FromFile: header,
		ToFile:   header,
		Context:  opts.Unified,
	})
	if err != nil {
		return "", fmt.Errorf("diff %s: %w", header, err)
	}
	return diff, nil
}
func displayBodies(doc Document, from, to string) (string, string, error) {
	if doc.Resource.Kind == "Secret" {
		return redactedSecretBodies(from, to)
	}
	return from, to, nil
}
