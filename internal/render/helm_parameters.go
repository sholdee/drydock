package render

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/sholdee/drydock/internal/remote"
	"helm.sh/helm/v4/pkg/strvals"
)

func applyHelmParameters(ctx context.Context, source ResolvedSource, opts RenderOptions, values map[string]any) error {
	for _, parameter := range opts.HelmParameters {
		name := strings.TrimSpace(parameter.Name)
		if name == "" {
			return fmt.Errorf("helm parameter name is required")
		}
		rawValue := parameter.Value
		value := opts.ArgoEnv.Envsubst(rawValue)
		expression := name + "=" + cleanHelmSetParameter(value)
		var err error
		if parameter.ForceString {
			err = strvals.ParseIntoString(expression, values)
		} else {
			err = strvals.ParseInto(expression, values)
		}
		if err != nil {
			return fmt.Errorf("helm parameter %q failed to parse: %s", name, redactHelmParameterError(err.Error(), rawValue, value))
		}
	}

	if len(opts.HelmFileParameters) == 0 {
		return nil
	}
	reader := helmFileParameterReader(ctx, source, opts)
	for _, parameter := range opts.HelmFileParameters {
		name := strings.TrimSpace(parameter.Name)
		if name == "" {
			return fmt.Errorf("helm file parameter name is required")
		}
		rawPath := parameter.Path
		filePath := envsubstHelmValueFilePath(rawPath, opts, opts.RefRoots)
		if err := strvals.ParseIntoFile(name+"="+cleanHelmSetParameter(filePath), values, reader); err != nil {
			return fmt.Errorf("helm file parameter %q failed to parse: %s", name, redactHelmParameterError(err.Error(), rawPath, filePath))
		}
	}
	return nil
}

func helmFileParameterReader(ctx context.Context, source ResolvedSource, opts RenderOptions) strvals.RunesValueReader {
	return func(input []rune) (any, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		file := string(input)
		if isRemoteHelmValueFile(file) {
			return nil, fmt.Errorf("helm file parameter %q must reference a local or $ref file", remote.RedactURL(file))
		}
		root, resolved, err := resolveHelmValueFile(source.RepoRoot, helmValueFilesBaseDir(source, opts), helmValueFilesBoundaryRoot(source, opts), opts.RefRoots, file)
		if err != nil {
			return nil, err
		}
		if err := rejectSymlinkedPath(root, resolved); err != nil {
			return nil, fmt.Errorf("helm file parameter %q: %w", file, err)
		}
		data, err := os.ReadFile(resolved)
		if err != nil {
			return nil, fmt.Errorf("read helm file parameter %q: %w", file, err)
		}
		return string(data), nil
	}
}

func redactHelmParameterError(message string, sensitiveValues ...string) string {
	for _, value := range sensitiveValues {
		if value == "" {
			continue
		}
		message = strings.ReplaceAll(message, value, "[redacted]")
	}
	return message
}

func cleanHelmSetParameter(value string) string {
	if strings.HasPrefix(value, "{") && strings.HasSuffix(value, "}") {
		return value
	}
	return replaceRuneWithLookbehind(value, ',', `\,`, '\\')
}

func replaceRuneWithLookbehind(value string, old rune, replacement string, lookbehind rune) string {
	var out strings.Builder
	var previous rune
	for _, current := range value {
		if current == old {
			if previous != lookbehind {
				out.WriteString(replacement)
			} else {
				out.WriteRune(current)
			}
		} else {
			out.WriteRune(current)
		}
		previous = current
	}
	return out.String()
}
