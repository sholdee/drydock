package render

import (
	"context"
	"fmt"
	"os"
	"strings"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/remote"
	"helm.sh/helm/v4/pkg/strvals"
)

// applyHelmParameters applies helm.parameters and helm.fileParameters the way
// Argo CD's helm template call does. The repo-server collects --set,
// --set-string and --set-file values into maps keyed by the exact parameter
// name, so only the last entry per name of each kind reaches helm, and helm
// applies every --set, then every --set-string, then every --set-file,
// whatever their order on the command line. Winning entries apply in list
// order; Argo CD's own order within one kind follows Go's randomized map
// iteration order.
func applyHelmParameters(ctx context.Context, source ResolvedSource, opts RenderOptions, values map[string]any) error {
	lastParameters := lastHelmParameters(opts.HelmParameters)
	for _, forceString := range []bool{false, true} {
		for i, parameter := range opts.HelmParameters {
			if parameter.ForceString != forceString || !lastParameters[i] {
				continue
			}
			if err := applyHelmParameter(parameter, opts, values); err != nil {
				return err
			}
		}
	}

	if len(opts.HelmFileParameters) == 0 {
		return nil
	}
	reader := helmFileParameterReader(ctx, source, opts)
	lastFileParameters := lastHelmFileParameters(opts.HelmFileParameters)
	for i, parameter := range opts.HelmFileParameters {
		rawPath := parameter.Path
		filePath := envsubstHelmValueFilePath(rawPath, opts, opts.RefRoots)
		if !lastFileParameters[i] {
			if err := resolveOverriddenHelmFileParameter(source, opts, filePath); err != nil {
				return fmt.Errorf("helm file parameter %q: %s", parameter.Name, redactHelmParameterError(err.Error(), rawPath, filePath))
			}
			continue
		}
		if err := strvals.ParseIntoFile(parameter.Name+"="+cleanHelmSetParameter(filePath), values, reader); err != nil {
			return fmt.Errorf("helm file parameter %q failed to parse: %s", parameter.Name, redactHelmParameterError(err.Error(), rawPath, filePath))
		}
	}
	return nil
}

func applyHelmParameter(parameter argoappv1.HelmParameter, opts RenderOptions, values map[string]any) error {
	rawValue := parameter.Value
	value := opts.ArgoEnv.Envsubst(rawValue)
	expression := parameter.Name + "=" + cleanHelmSetParameter(value)
	var err error
	if parameter.ForceString {
		err = strvals.ParseIntoString(expression, values)
	} else {
		err = strvals.ParseInto(expression, values)
	}
	if err != nil {
		return fmt.Errorf("helm parameter %q failed to parse: %s", parameter.Name, redactHelmParameterError(err.Error(), rawValue, value))
	}
	return nil
}

type helmParameterKey struct {
	forceString bool
	name        string
}

// lastHelmParameters reports which helm.parameters entries reach helm: the
// last entry for each exact name among --set parameters and, separately,
// among --set-string parameters.
func lastHelmParameters(parameters []argoappv1.HelmParameter) []bool {
	return lastEntryPerKey(parameters, func(parameter argoappv1.HelmParameter) helmParameterKey {
		return helmParameterKey{forceString: parameter.ForceString, name: parameter.Name}
	})
}

// lastHelmFileParameters reports which helm.fileParameters entries reach
// helm: the last entry for each exact name.
func lastHelmFileParameters(parameters []argoappv1.HelmFileParameter) []bool {
	return lastEntryPerKey(parameters, func(parameter argoappv1.HelmFileParameter) string {
		return parameter.Name
	})
}

// EffectiveHelmFileParameters returns the helm.fileParameters entries whose
// files helm reads, the last entry for each exact name, in list order.
func EffectiveHelmFileParameters(parameters []argoappv1.HelmFileParameter) []argoappv1.HelmFileParameter {
	last := lastHelmFileParameters(parameters)
	effective := make([]argoappv1.HelmFileParameter, 0, len(parameters))
	for i, parameter := range parameters {
		if last[i] {
			effective = append(effective, parameter)
		}
	}
	return effective
}

func lastEntryPerKey[T any, K comparable](entries []T, key func(T) K) []bool {
	last := make([]bool, len(entries))
	seen := make(map[K]struct{}, len(entries))
	for i := len(entries) - 1; i >= 0; i-- {
		k := key(entries[i])
		if _, ok := seen[k]; ok {
			continue
		}
		seen[k] = struct{}{}
		last[i] = true
	}
	return last
}

// resolveOverriddenHelmFileParameter checks the path of a file parameter that
// a later entry with the same name overrides. Argo CD resolves every file
// parameter path before it runs helm, so a path that escapes its root or a URL
// with a disallowed scheme still fails, but the file is never read. Unlike the
// repo-server, drydock does not follow a symlink at the path, which keeps the
// render independent of files the persistent cache does not digest.
func resolveOverriddenHelmFileParameter(source ResolvedSource, opts RenderOptions, file string) error {
	if parsed, ok := parseRemoteHelmValueFile(file); ok {
		if !helmValueFileSchemeAllowed(parsed.Scheme, opts) {
			return fmt.Errorf("URL scheme %q is not allowed", parsed.Scheme)
		}
		return nil
	}
	_, _, err := resolveHelmValueFile(source.RepoRoot, helmValueFilesBaseDir(source, opts), helmValueFilesBoundaryRoot(source, opts), opts.RefRoots, file)
	return err
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
