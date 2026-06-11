package diff

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
type Normalization struct {
	JSONPointers          []string
	JQPathExpressions     []string
	ManagedFieldsManagers []string
	KnownTypeFields       []KnownTypeField
	CompareOptions        CompareOptions
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

func documentsWithSharedNormalization(left, right Document, hasLeft, hasRight bool) (Document, Document) {
	normalization := Normalization{}
	var compareOptions CompareOptions
	var hasCompareOptions bool
	if hasLeft {
		normalization = appendUniqueNormalization(normalization, left.Normalization)
		compareOptions = left.Normalization.CompareOptions
		hasCompareOptions = true
	}
	if hasRight {
		normalization = appendUniqueNormalization(normalization, right.Normalization)
		if hasCompareOptions {
			compareOptions = mergeCompareOptions(compareOptions, right.Normalization.CompareOptions)
		} else {
			compareOptions = right.Normalization.CompareOptions
		}
	}
	normalization.CompareOptions = compareOptions
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
	diff, err := unified(doc, from, to, opts)
	if err != nil {
		return Result{}, err
	}
	return Result{
		Parent:   doc.Parent,
		Resource: doc.Resource,
		Change:   change,
		Diff:     diff,
	}, nil
}
