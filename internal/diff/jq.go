package diff

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/itchyny/gojq"
)

const jqExecutionTimeout = time.Second

// removeJQPathExpressions deletes each jq path expression from object in
// order. An expression that does not parse or compile is an error, as Argo CD
// v3.6.1 fails the whole comparison when it builds such a patch. An expression
// that fails while it runs on this object is skipped, and the next one runs
// on the unchanged object: Argo CD logs the failed patch and keeps going
// (util/argo/normalizers/diff_normalizer.go:210-224). A typical case is a
// containers expression that a wildcard rule applies to a ConfigMap.
func removeJQPathExpressions(object map[string]any, expressions []string) (map[string]any, error) {
	if len(expressions) == 0 {
		return object, nil
	}
	var current any = object
	for _, expression := range expressions {
		code, err := compileJQPathExpression(expression)
		if err != nil {
			return nil, err
		}
		next, err := runJQPathExpression(code, current, expression)
		if err != nil {
			continue
		}
		current = next
	}
	typed, ok := current.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("jq normalization produced %T root, want object", current)
	}
	return typed, nil
}

func compileJQPathExpression(expression string) (*gojq.Code, error) {
	query, err := gojq.Parse(fmt.Sprintf("del(%s)", expression))
	if err != nil {
		return nil, fmt.Errorf("invalid jq path expression %q: %w", expression, err)
	}
	code, err := gojq.Compile(query)
	if err != nil {
		return nil, fmt.Errorf("compile jq path expression %q: %w", expression, err)
	}
	return code, nil
}

// runJQPathExpression runs one compiled deletion. Like Argo CD's
// jqNormalizerPatch.Apply, it fails when the expression returns no value, an
// error, or more than one value, or when it runs longer than the timeout.
func runJQPathExpression(code *gojq.Code, object any, expression string) (any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), jqExecutionTimeout)
	defer cancel()
	iter := code.RunWithContext(ctx, object)
	first, ok := iter.Next()
	if !ok {
		return nil, fmt.Errorf("jq path expression %q did not return any data", expression)
	}
	if errValue, ok := first.(error); ok {
		return nil, jqResultError(expression, errValue)
	}
	if second, ok := iter.Next(); ok {
		if errValue, ok := second.(error); ok {
			return nil, jqResultError(expression, errValue)
		}
		return nil, fmt.Errorf("jq path expression %q returned multiple objects", expression)
	}
	data, err := json.Marshal(first)
	if err != nil {
		return nil, fmt.Errorf("marshal jq path expression %q result: %w", expression, err)
	}
	var out any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("unmarshal jq path expression %q result: %w", expression, err)
	}
	return out, nil
}

func jqResultError(expression string, err error) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("jq path expression %q timed out after %s", expression, jqExecutionTimeout)
	}
	return fmt.Errorf("jq path expression %q returned error: %w", expression, err)
}
