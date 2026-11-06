package diff

import (
	"errors"
	"fmt"
	"time"
)

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
	// ServerSideDiff reports that Argo CD compares the resource's
	// Application with server-side diff. It decides what happens when an
	// ignore rule removes a whole object (see errObjectRemoved).
	ServerSideDiff bool
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
	// JQExecutionTimeout limits how long one jq ignore-rule deletion may run,
	// as the Argo CD controller setting controller.ignore.normalizer.jq.timeout
	// does. Zero means Argo CD's default of 1 second. An expression that runs
	// longer is skipped for that resource.
	JQExecutionTimeout time.Duration
}

// ComparisonFailure is an Application whose comparison Argo CD v3.6.1
// fails, so it has no diff results.
type ComparisonFailure struct {
	Parent  Parent
	Message string
}

// Run diffs the documents. It returns an error for an Application whose
// comparison fails; use RunWithFailures to diff the other Applications.
func Run(left, right []Document, opts Options) ([]Result, error) {
	results, failures, err := RunWithFailures(left, right, opts)
	if err != nil {
		return nil, err
	}
	if len(failures) > 0 {
		return nil, errors.New(failures[0].Message)
	}
	return results, nil
}

// RunWithFailures diffs the documents. An Application whose comparison
// fails, as Argo CD fails it, gets one ComparisonFailure and no results.
func RunWithFailures(left, right []Document, opts Options) ([]Result, []ComparisonFailure, error) {
	leftByKey := documentsByKey(left)
	rightByKey := documentsByKey(right)
	keys := sortedKeys(leftByKey, rightByKey)

	results := make([]Result, 0)
	var failures []ComparisonFailure
	failed := map[string]struct{}{}
	for _, key := range keys {
		result, include, failure, err := diffResultForKey(leftByKey, rightByKey, key, opts)
		if err != nil {
			return nil, nil, err
		}
		if failure != nil {
			if _, ok := failed[parentName(failure.Parent)]; !ok {
				failed[parentName(failure.Parent)] = struct{}{}
				failures = append(failures, *failure)
			}
			continue
		}
		if include {
			results = append(results, result)
		}
	}
	if len(failed) == 0 {
		return results, nil, nil
	}
	kept := results[:0]
	for _, result := range results {
		if _, ok := failed[parentName(result.Parent)]; !ok {
			kept = append(kept, result)
		}
	}
	return kept, failures, nil
}

func diffResultForKey(leftByKey, rightByKey map[string]Document, key string, opts Options) (Result, bool, *ComparisonFailure, error) {
	left, hasLeft := leftByKey[key]
	right, hasRight := rightByKey[key]
	left, right = documentsWithSharedNormalization(left, right, hasLeft, hasRight)
	if hasLeft && hasRight && left.Body == right.Body {
		// Both sides share the current side's normalization, so identical bodies
		// always normalize identically and can never produce a diff. Under
		// client-side diff, an ignore rule that removes the whole object
		// still fails the comparison, so one side is checked for that. Other
		// errors of that check are ignored, as the resource is unchanged.
		if mayFailComparison(left.Normalization) && objectRemovedByNormalization(right, opts) {
			return Result{}, false, bothObjectsRemovedFailure(right), nil
		}
		return Result{}, false, nil, nil
	}
	leftBody, rightBody, err := normalizedDocumentBodies(left, right, hasLeft, hasRight, opts)
	if err != nil {
		return Result{}, false, nil, err
	}
	if hasLeft && hasRight && !right.Normalization.ServerSideDiff && leftBody == nullDiffBody && rightBody == nullDiffBody {
		return Result{}, false, bothObjectsRemovedFailure(right), nil
	}
	leftBody, rightBody, err = serverSideCreateOrDeleteBodies(left, right, hasLeft, hasRight, leftBody, rightBody, opts)
	if err != nil {
		return Result{}, false, nil, err
	}
	doc, change, include := changedDocument(left, right, hasLeft, hasRight, leftBody, rightBody)
	if !include {
		return Result{}, false, nil, nil
	}
	result, err := resultFor(doc, change, leftBody, rightBody, opts)
	return result, true, nil, err
}

// bothObjectsRemovedFailure is the failure of the comparison when an ignore
// rule removes the object on both sides under client-side diff.
// gitops-engine then cannot build a patch from null and null and returns an
// error, so Argo CD fails the comparison of the Application
// (pkg/diff/diff.go Diff, controller/state.go StateDiffs). When only one
// side is null, the client-side diff reports a change (with the last-applied
// annotation that a client-side sync leaves) and server-side diff always
// does, so drydock shows a change. Server-side diff compares null with null
// as unchanged.
func bothObjectsRemovedFailure(doc Document) *ComparisonFailure {
	return &ComparisonFailure{
		Parent:  doc.Parent,
		Message: fmt.Sprintf("%s: an ignore rule removed the whole object on both sides, so Argo CD's client-side diff fails the comparison of the Application", headerOf(doc)),
	}
}

// objectRemovedByNormalization reports whether the document's ignore rules
// remove its whole object. Any other normalization error counts as false.
func objectRemovedByNormalization(doc Document, opts Options) bool {
	_, err := normalizeDiffObject(doc.Body, opts, doc.Normalization, doc.Resource)
	return errors.Is(err, errObjectRemoved)
}

// serverSideCreateOrDeleteBodies replaces the null body of a resource that
// only one side has under server-side diff. Argo CD's server-side diff skips
// the ignore rules and known types for a created or deleted resource
// (gitops-engine pkg/diff/diff.go Diff and handleResourceCreateOrDeleteDiff),
// so it shows the whole object where a rule would remove it.
func serverSideCreateOrDeleteBodies(left, right Document, hasLeft, hasRight bool, leftBody, rightBody string, opts Options) (string, string, error) {
	if hasLeft == hasRight {
		return leftBody, rightBody, nil
	}
	doc, body := right, rightBody
	if hasLeft {
		doc, body = left, leftBody
	}
	if !doc.Normalization.ServerSideDiff || body != nullDiffBody {
		return leftBody, rightBody, nil
	}
	doc.Normalization.ApplicationRules = nil
	doc.Normalization.CustomizationRules = nil
	doc.Normalization.AnnotationJSONPointers = nil
	doc.Normalization.KnownTypeFields = nil
	shown, err := normalizeDocumentBody(doc, opts)
	if err != nil {
		return "", "", err
	}
	if hasLeft {
		return shown, rightBody, nil
	}
	return leftBody, shown, nil
}

// mayFailComparison reports whether normalization can remove a whole object
// under client-side diff, which fails the comparison even when both bodies
// are identical. Only a jq expression can remove the whole object.
func mayFailComparison(normalization Normalization) bool {
	if normalization.ServerSideDiff {
		return false
	}
	for _, rules := range [][]IgnoreRule{normalization.ApplicationRules, normalization.CustomizationRules} {
		for _, rule := range rules {
			if len(rule.JQPathExpressions) > 0 {
				return true
			}
		}
	}
	return false
}

// documentsWithSharedNormalization gives both sides the normalization of the
// current (right) side. Argo CD compares the desired state with the live
// state under one rule list: the ignore rules of the Application and the
// settings it runs with when it compares the new desired state, which are
// the current side's rules. It reads the ignore-differences annotation from
// the desired (target) objects only (util/argo/diff/normalize.go:14-15,
// diff.go:290-291, ignore.go:130-131). drydock compares desired against
// desired, so the current side stands in for the target. A resource that
// exists only on the baseline side has no target. It keeps the rules its
// document carries, which the caller sets to the current side's rules of the
// Application, but its own annotation does not normalize it.
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
