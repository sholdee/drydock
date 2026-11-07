package render

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/pathsafety"
	"github.com/sholdee/drydock/internal/remote"
	"helm.sh/helm/v4/pkg/strvals"
)

// resolveHelmFileParameters resolves the path of every helm.fileParameters
// entry in list order, as the repo-server does before it runs helm
// (resolveHelmFileParameter).
func resolveHelmFileParameters(source ResolvedSource, opts RenderOptions) ([]helmFileParameterTarget, error) {
	last := lastHelmFileParameters(opts.HelmFileParameters)
	targets := make([]helmFileParameterTarget, len(opts.HelmFileParameters))
	for i, parameter := range opts.HelmFileParameters {
		target, err := resolveHelmFileParameter(source, opts, parameter, last[i])
		if err != nil {
			return nil, fmt.Errorf("helm file parameter %q: %w", parameter.Name, err)
		}
		targets[i] = target
	}
	return targets, nil
}

// applyHelmParameters applies helm.parameters and helm.fileParameters the way
// Argo CD's helm template call does. targets are the resolved file parameter
// paths (resolveHelmFileParameters). The repo-server collects --set,
// --set-string and --set-file values into maps keyed by the exact parameter
// name, so only the last entry per name of each kind reaches helm, and helm
// applies every --set, then every --set-string, then every --set-file,
// whatever their order on the command line. Winning entries apply in list
// order; Argo CD's own order within one kind follows Go's randomized map
// iteration order.
func applyHelmParameters(ctx context.Context, opts RenderOptions, targets []helmFileParameterTarget, values map[string]any) error {
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
		value := target.url
		if value == "" {
			value = helmFileParameterSetValue(target.root, target.path)
		}
		// The path is not secret: the repo-server shows it in its errors.
		if err := strvals.ParseIntoFile(parameter.Name+"="+cleanHelmSetParameter(value), values, helmFileParameterReader(ctx, target)); err != nil {
			return fmt.Errorf("helm file parameter %q failed to parse: %w", parameter.Name, err)
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

// helmFileParameterReader reads the file that helm --set-file names for
// target. helm asks for the path it parsed from name=<path>, which must be
// target.helmPath: a name that holds "=" or "," can make helm parse another
// path, which drydock did not resolve, so that fails. drydock reads the file
// only when its real path is in the root and outside .git
// (confineHelmFileParameterRead), and then reads that real path. helm reads
// the file even when the parameter name is empty.
func helmFileParameterReader(ctx context.Context, target helmFileParameterTarget) strvals.RunesValueReader {
	return func(input []rune) (any, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		value := string(input)
		if target.url != "" || isRemoteHelmValueFile(value) {
			return nil, fmt.Errorf("helm file parameter %q must reference a local or $ref file", remote.RedactURL(value))
		}
		asked := ""
		if strings.HasPrefix(value, "/") {
			asked = filepath.Join(target.root, filepath.FromSlash(value))
		}
		if asked == "" || asked != target.helmPath {
			return nil, fmt.Errorf("helm file parameter %q: helm --set-file reads %q, which is not the resolved path", target.file, value)
		}
		realPath, err := confineHelmFileParameterRead(target.root, asked, target.file)
		if err != nil {
			return nil, err
		}
		data, err := pathsafety.ReadRegularFile(realPath)
		if errors.Is(err, pathsafety.ErrNotRegular) {
			if info, statErr := os.Stat(realPath); statErr == nil && info.IsDir() {
				// helm's os.ReadFile fails on a directory with "is a
				// directory", at once.
				_, err = os.ReadFile(realPath)
			}
		}
		if err != nil {
			return nil, fmt.Errorf("read helm file parameter %q: %w", target.file, err)
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
