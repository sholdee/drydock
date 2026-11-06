package diff

import "fmt"

type Parent struct {
	Namespace   string `json:"namespace,omitempty" yaml:"namespace,omitempty"`
	Name        string `json:"name" yaml:"name"`
	SourceIndex int    `json:"sourceIndex" yaml:"sourceIndex"`
	SourceName  string `json:"sourceName,omitempty" yaml:"sourceName,omitempty"`
	SourcePath  string `json:"sourcePath,omitempty" yaml:"sourcePath,omitempty"`
}
type Resource struct {
	Group     string `json:"group,omitempty" yaml:"group,omitempty"`
	Kind      string `json:"kind" yaml:"kind"`
	Namespace string `json:"namespace,omitempty" yaml:"namespace,omitempty"`
	Name      string `json:"name" yaml:"name"`
}
type KnownTypeField struct {
	Field string
	Type  string
}

// IgnoreRule holds the jsonPointers and jqPathExpressions of one
// ignore-differences rule that matches a resource. Argo CD v3.6.1 applies a
// rule's pointers first and then its jq expressions, one rule at a time
// (util/argo/normalizers/diff_normalizer.go:142-176). Every copy of a repeated
// pointer or expression is kept, because a repeated array-index pointer
// removes one more element each time.
type IgnoreRule struct {
	JSONPointers      []string
	JQPathExpressions []string
}

// Normalization holds the diff normalization for one resource. The ignore
// rules run in Argo CD's order: ApplicationRules, then the
// AnnotationJSONPointers rule, then CustomizationRules
// (util/argo/diff/ignore.go:118-122 and 155-159, diff_normalizer.go:122-140).
// When a resource exists on both sides, both sides use the current side's
// normalization.
type Normalization struct {
	// ApplicationRules are the matching Application spec.ignoreDifferences
	// entries, in spec order.
	ApplicationRules []IgnoreRule
	// CustomizationRules are the matching resource.customizations
	// ignoreDifferences entries, in sorted key order.
	CustomizationRules    []IgnoreRule
	ManagedFieldsManagers []string
	KnownTypeFields       []KnownTypeField
	CompareOptions        CompareOptions
	// AnnotationJSONPointers holds the jsonPointers parsed from the resource's
	// own argocd.argoproj.io/ignore-differences annotation.
	AnnotationJSONPointers []string
}
type Document struct {
	Parent        Parent        `json:"parent" yaml:"parent"`
	Resource      Resource      `json:"resource" yaml:"resource"`
	Body          string        `json:"body" yaml:"body"`
	Normalization Normalization `json:"-" yaml:"-"`
}
type Change string

const (
	ChangeAdded    Change = "added"
	ChangeRemoved  Change = "removed"
	ChangeModified Change = "modified"
)

type Result struct {
	Parent   Parent   `json:"parent" yaml:"parent"`
	Resource Resource `json:"resource" yaml:"resource"`
	Change   Change   `json:"change" yaml:"change"`
	Diff     string   `json:"diff" yaml:"diff"`
	// Before and After are the display bodies the unified Diff was built
	// from (Secret values already redacted). They feed alternate renderers
	// and are not part of the structured output.
	Before string `json:"-" yaml:"-"`
	After  string `json:"-" yaml:"-"`
}

// Header is the human-readable identity line used in the unified diff headers.
func (r Result) Header() string {
	return headerOf(Document{Parent: r.Parent, Resource: r.Resource})
}

type Options struct {
	Unified           int
	StripAttrs        []string
	ShowIgnoredFields bool
}

func Run(left, right []Document, opts Options) ([]Result, error) {
	leftByKey := documentsByKey(left)
	rightByKey := documentsByKey(right)
	keys := sortedKeys(leftByKey, rightByKey)

	results := make([]Result, 0)
	for _, key := range keys {
		result, include, err := diffResultForKey(leftByKey, rightByKey, key, opts)
		if err != nil {
			return nil, err
		}
		if include {
			results = append(results, result)
		}
	}

	return results, nil
}
func diffResultForKey(leftByKey, rightByKey map[string]Document, key string, opts Options) (Result, bool, error) {
	left, hasLeft := leftByKey[key]
	right, hasRight := rightByKey[key]
	left, right = documentsWithSharedNormalization(left, right, hasLeft, hasRight)
	if hasLeft && hasRight && left.Body == right.Body {
		// Both sides share one merged normalization, so identical bodies always
		// normalize identically and can never produce a diff.
		return Result{}, false, nil
	}
	leftBody, rightBody, err := normalizedDocumentBodies(left, right, hasLeft, hasRight, opts)
	if err != nil {
		return Result{}, false, err
	}
	doc, change, include := changedDocument(left, right, hasLeft, hasRight, leftBody, rightBody)
	if !include {
		return Result{}, false, nil
	}
	result, err := resultFor(doc, change, leftBody, rightBody, opts)
	return result, true, err
}

// documentsWithSharedNormalization gives both sides the normalization of the
// current (right) side. Argo CD compares the desired state with the live
// state under one rule list: the ignore rules of the Application and the
// settings it runs with when it compares the new desired state, which are
// the current side's rules. It reads the ignore-differences annotation from
// the desired (target) objects only (util/argo/diff/normalize.go:14-15,
// diff.go:290-291, ignore.go:130-131). drydock compares desired against
// desired, so the current side stands in for the target. A resource that
// exists only on the baseline side has no target. It keeps its own rules
// for the removed-resource diff, but its own annotation does not normalize
// it.
func documentsWithSharedNormalization(left, right Document, hasLeft, hasRight bool) (Document, Document) {
	var normalization Normalization
	if hasRight {
		normalization = right.Normalization
	} else {
		normalization = left.Normalization
		normalization.AnnotationJSONPointers = nil
	}
	if hasLeft {
		left.Normalization = cloneNormalization(normalization)
	}
	if hasRight {
		right.Normalization = cloneNormalization(normalization)
	}
	return left, right
}
func changedDocument(left, right Document, hasLeft, hasRight bool, leftBody, rightBody string) (Document, Change, bool) {
	if leftBody == rightBody {
		return Document{}, "", false
	}
	switch {
	case hasLeft && hasRight:
		return right, ChangeModified, true
	case hasLeft:
		return left, ChangeRemoved, true
	case hasRight:
		return right, ChangeAdded, true
	default:
		return Document{}, "", false
	}
}
func resultFor(doc Document, change Change, from, to string, opts Options) (Result, error) {
	header := headerOf(doc)
	displayFrom, displayTo, err := displayBodies(doc, from, to)
	if err != nil {
		return Result{}, fmt.Errorf("diff %s: %w", header, err)
	}
	diff, err := unified(header, displayFrom, displayTo, opts)
	if err != nil {
		return Result{}, err
	}
	return Result{
		Parent:   doc.Parent,
		Resource: doc.Resource,
		Change:   change,
		Diff:     diff,
		Before:   displayFrom,
		After:    displayTo,
	}, nil
}
