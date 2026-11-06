package render

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/remote"
	"helm.sh/helm/v4/pkg/strvals"
)

// applyHelmParameters applies helm.parameters and helm.fileParameters the way
// Argo CD's helm template call does. The repo-server first resolves the path
// of every file parameter (resolveHelmFileParameter). It collects --set,
// --set-string and --set-file values into maps keyed by the exact parameter
// name, so only the last entry per name of each kind reaches helm, and helm
// applies every --set, then every --set-string, then every --set-file,
// whatever their order on the command line. Winning entries apply in list
// order; Argo CD's own order within one kind follows Go's randomized map
// iteration order.
func applyHelmParameters(ctx context.Context, source ResolvedSource, opts RenderOptions, values map[string]any) error {
	targets := make([]helmFileParameterTarget, len(opts.HelmFileParameters))
	for i, parameter := range opts.HelmFileParameters {
		target, err := resolveHelmFileParameter(source, opts, parameter.Path)
		if err != nil {
			return fmt.Errorf("helm file parameter %q: %w", parameter.Name, err)
		}
		targets[i] = target
	}

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

	lastFileParameters := lastHelmFileParameters(opts.HelmFileParameters)
	for i, parameter := range opts.HelmFileParameters {
		if !lastFileParameters[i] {
			continue
		}
		target := targets[i]
		if err := strvals.ParseIntoFile(parameter.Name+"="+cleanHelmSetParameter(helmFileParameterSetValue(target)), values, helmFileParameterReader(ctx, target)); err != nil {
			return fmt.Errorf("helm file parameter %q failed to parse: %s", parameter.Name, redactHelmParameterError(err.Error(), parameter.Path, target.file))
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

// OverriddenHelmFileParameters returns the helm.fileParameters entries that a
// later entry with the same name overrides, in list order. The render
// resolves their paths but helm never reads them.
func OverriddenHelmFileParameters(parameters []argoappv1.HelmFileParameter) []argoappv1.HelmFileParameter {
	last := lastHelmFileParameters(parameters)
	var overridden []argoappv1.HelmFileParameter
	for i, parameter := range parameters {
		if !last[i] {
			overridden = append(overridden, parameter)
		}
	}
	return overridden
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

// helmFileParameterSetValue is the file that drydock passes to --set-file for
// target. The repo-server passes the absolute resolved path. drydock passes
// it relative to the root, with a leading "/", so a comma or a backslash in
// the path of drydock's checkout cannot change how helm splits the value.
func helmFileParameterSetValue(target helmFileParameterTarget) string {
	if target.url != "" {
		return target.url
	}
	rel, err := filepath.Rel(target.root, target.path)
	if err != nil {
		return target.path
	}
	return "/" + filepath.ToSlash(rel)
}

// helmFileParameterReader reads the file that helm --set-file names for
// target. helm reads the file even when the parameter name is empty.
func helmFileParameterReader(ctx context.Context, target helmFileParameterTarget) strvals.RunesValueReader {
	return func(input []rune) (any, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		value := string(input)
		if target.url != "" || isRemoteHelmValueFile(value) {
			return nil, fmt.Errorf("helm file parameter %q must reference a local or $ref file", remote.RedactURL(value))
		}
		data, err := readHelmFileParameter(target.root, filepath.Join(target.root, filepath.FromSlash(value)), target.file)
		if err != nil {
			return nil, err
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
