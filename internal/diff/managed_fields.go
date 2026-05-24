package diff

import (
	"encoding/json"
	"fmt"

	"github.com/argoproj/argo-cd/v3/util/argo/managedfields"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/structured-merge-diff/v6/typed"
)

func normalizeManagedFieldsPair(left, right map[string]any, managers []string) (map[string]any, map[string]any, error) {
	if len(managers) == 0 || left == nil || right == nil {
		return left, right, nil
	}
	pt := typed.DeducedParseableType
	leftClone, err := cloneMap(left)
	if err != nil {
		return nil, nil, err
	}
	rightClone, err := cloneMap(right)
	if err != nil {
		return nil, nil, err
	}
	leftObj := &unstructured.Unstructured{Object: leftClone}
	rightObj := &unstructured.Unstructured{Object: rightClone}

	nextLeft, nextRight, err := managedfields.Normalize(leftObj, rightObj, managers, &pt)
	if err != nil {
		return nil, nil, err
	}
	if nextLeft != nil && nextRight != nil {
		leftObj = nextLeft
		rightObj = nextRight
	}

	nextRight, nextLeft, err = managedfields.Normalize(rightObj, leftObj, managers, &pt)
	if err != nil {
		return nil, nil, err
	}
	if nextLeft != nil && nextRight != nil {
		leftObj = nextLeft
		rightObj = nextRight
	}
	return leftObj.Object, rightObj.Object, nil
}

func cloneMap(in map[string]any) (map[string]any, error) {
	if in == nil {
		return nil, nil
	}
	data, err := json.Marshal(in)
	if err != nil {
		return nil, fmt.Errorf("clone map marshal: %w", err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("clone map unmarshal: %w", err)
	}
	return out, nil
}

func stripManagedFields(object map[string]any) {
	metadata, ok := stringMapField(object, "metadata")
	if !ok {
		return
	}
	delete(metadata, "managedFields")
}
