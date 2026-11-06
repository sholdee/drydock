package diff

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/itchyny/gojq"
)

// defaultJQExecutionTimeout is the time one jq deletion may run when
// Options.JQExecutionTimeout is zero. It is Argo CD v3.6.1's
// normalizers.DefaultJQExecutionTimeout.
const defaultJQExecutionTimeout = time.Second

// jqTimeout returns the jq timeout of opts.
func jqTimeout(opts Options) time.Duration {
	if opts.JQExecutionTimeout <= 0 {
		return defaultJQExecutionTimeout
	}
	return opts.JQExecutionTimeout
}

// removeJQPathExpressions deletes each jq path expression from object in
// order. An expression that does not parse or compile is an error, as Argo CD
// v3.6.1 fails the whole comparison when it builds such a patch. An expression
// that fails while it runs on this object is skipped, and the next one runs
// on the unchanged object: Argo CD logs the failed patch and keeps going
// (util/argo/normalizers/diff_normalizer.go:205-220). A typical case is a
// containers expression that a wildcard rule applies to a ConfigMap.
func removeJQPathExpressions(object map[string]any, expressions []string, timeout time.Duration) (map[string]any, error) {
	if len(expressions) == 0 {
		return object, nil
	}
	current := object
	for _, expression := range expressions {
		code, err := compileJQPathExpression(expression)
		if err != nil {
			return nil, err
		}
		next, err := runJQPathExpression(code, current, expression, timeout)
		if err != nil {
			continue
		}
		typed, ok := next.(map[string]any)
		if !ok {
			// The expression removed the whole object, as "." does. See
			// errObjectRemoved.
			return nil, errObjectRemoved
		}
		current = typed
	}
	return current, nil
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
func runJQPathExpression(code *gojq.Code, object any, expression string, timeout time.Duration) (any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	iter := code.RunWithContext(ctx, object)
	first, ok := iter.Next()
	if !ok {
		return nil, fmt.Errorf("jq path expression %q did not return any data", expression)
	}
	if errValue, ok := first.(error); ok {
		return nil, jqResultError(expression, errValue, timeout)
	}
	if second, ok := iter.Next(); ok {
		if errValue, ok := second.(error); ok {
			return nil, jqResultError(expression, errValue, timeout)
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

func jqResultError(expression string, err error, timeout time.Duration) error {
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("jq path expression %q timed out after %s", expression, timeout)
	}
	return fmt.Errorf("jq path expression %q returned error: %w", expression, err)
}

// ValidateJQPathExpression reports whether Argo CD v3.6.1 can build the jq
// deletion patch for expression. NewIgnoreNormalizer parses and compiles
// every expression before it matches any resource, and a failure there fails
// the whole comparison of the Application
// (util/argo/normalizers/diff_normalizer.go:159-168).
func ValidateJQPathExpression(expression string) error {
	_, err := compileJQPathExpression(expression)
	return err
}
