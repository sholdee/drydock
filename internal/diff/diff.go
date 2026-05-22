package diff

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/pmezard/go-difflib/difflib"
)

type Parent struct {
	Kind      string `json:"kind,omitempty" yaml:"kind,omitempty"`
	Namespace string `json:"namespace,omitempty" yaml:"namespace,omitempty"`
	Name      string `json:"name" yaml:"name"`
}

type Resource struct {
	Group     string `json:"group,omitempty" yaml:"group,omitempty"`
	Kind      string `json:"kind" yaml:"kind"`
	Namespace string `json:"namespace,omitempty" yaml:"namespace,omitempty"`
	Name      string `json:"name" yaml:"name"`
}

type Document struct {
	Parent      Parent   `json:"parent" yaml:"parent"`
	SourceIndex int      `json:"sourceIndex" yaml:"sourceIndex"`
	SourceName  string   `json:"sourceName,omitempty" yaml:"sourceName,omitempty"`
	SourcePath  string   `json:"sourcePath,omitempty" yaml:"sourcePath,omitempty"`
	Resource    Resource `json:"resource" yaml:"resource"`
	Body        string   `json:"body" yaml:"body"`
}

type Change string

const (
	ChangeAdded    Change = "added"
	ChangeRemoved  Change = "removed"
	ChangeModified Change = "modified"
)

type Result struct {
	Parent      Parent   `json:"parent" yaml:"parent"`
	SourceIndex int      `json:"sourceIndex" yaml:"sourceIndex"`
	SourceName  string   `json:"sourceName,omitempty" yaml:"sourceName,omitempty"`
	SourcePath  string   `json:"sourcePath,omitempty" yaml:"sourcePath,omitempty"`
	Resource    Resource `json:"resource" yaml:"resource"`
	Change      Change   `json:"change" yaml:"change"`
	Diff        string   `json:"diff" yaml:"diff"`
}

type Options struct {
	Unified int
}

func Run(left, right []Document, opts Options) ([]Result, error) {
	leftByKey := documentsByKey(left)
	rightByKey := documentsByKey(right)
	keys := sortedKeys(leftByKey, rightByKey)

	var results []Result
	for _, key := range keys {
		l, hasLeft := leftByKey[key]
		r, hasRight := rightByKey[key]

		switch {
		case hasLeft && hasRight && l.Body == r.Body:
			continue
		case hasLeft && hasRight:
			result, err := resultFor(r, ChangeModified, l.Body, r.Body, opts)
			if err != nil {
				return nil, err
			}
			results = append(results, result)
		case hasLeft:
			result, err := resultFor(l, ChangeRemoved, l.Body, "", opts)
			if err != nil {
				return nil, err
			}
			results = append(results, result)
		case hasRight:
			result, err := resultFor(r, ChangeAdded, "", r.Body, opts)
			if err != nil {
				return nil, err
			}
			results = append(results, result)
		}
	}

	return results, nil
}

func keyOf(doc Document) string {
	return strings.Join([]string{
		parentKind(doc.Parent),
		doc.Parent.Namespace,
		doc.Parent.Name,
		strconv.Itoa(doc.SourceIndex),
		doc.SourceName,
		doc.SourcePath,
		doc.Resource.Group,
		doc.Resource.Kind,
		doc.Resource.Namespace,
		doc.Resource.Name,
	}, "\x00")
}

func documentsByKey(docs []Document) map[string]Document {
	out := make(map[string]Document, len(docs))
	for _, doc := range docs {
		out[keyOf(doc)] = doc
	}
	return out
}

func sortedKeys(left, right map[string]Document) []string {
	seen := make(map[string]struct{}, len(left)+len(right))
	for key := range left {
		seen[key] = struct{}{}
	}
	for key := range right {
		seen[key] = struct{}{}
	}

	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func resultFor(doc Document, change Change, from, to string, opts Options) (Result, error) {
	diff, err := unified(doc, from, to, opts)
	if err != nil {
		return Result{}, err
	}
	return Result{
		Parent:      doc.Parent,
		SourceIndex: doc.SourceIndex,
		SourceName:  doc.SourceName,
		SourcePath:  doc.SourcePath,
		Resource:    doc.Resource,
		Change:      change,
		Diff:        diff,
	}, nil
}

func unified(doc Document, from, to string, opts Options) (string, error) {
	header := headerOf(doc)
	diff, err := difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A:        difflib.SplitLines(from),
		B:        difflib.SplitLines(to),
		FromFile: header,
		ToFile:   header,
		Context:  opts.Unified,
	})
	if err != nil {
		return "", fmt.Errorf("diff %s: %w", header, err)
	}
	return diff, nil
}

func headerOf(doc Document) string {
	parts := []string{
		fmt.Sprintf("%s: %s", parentKind(doc.Parent), parentName(doc.Parent)),
		fmt.Sprintf("Source: %d", doc.SourceIndex),
	}
	if doc.SourceName != "" {
		parts = append(parts, fmt.Sprintf("name=%q", doc.SourceName))
	}
	if doc.SourcePath != "" {
		parts = append(parts, doc.SourcePath)
	}
	parts = append(parts, resourceName(doc.Resource))
	return strings.Join(parts, " ")
}

func parentKind(parent Parent) string {
	if parent.Kind == "" {
		return "Application"
	}
	return parent.Kind
}

func parentName(parent Parent) string {
	if parent.Namespace == "" {
		return parent.Name
	}
	return parent.Namespace + "/" + parent.Name
}

func resourceName(resource Resource) string {
	kind := resource.Kind
	if resource.Group != "" {
		kind = resource.Group + "/" + kind
	}
	name := resource.Name
	if resource.Namespace != "" {
		name = resource.Namespace + "/" + name
	}
	return kind + ": " + name
}
