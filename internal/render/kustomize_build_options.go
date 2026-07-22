package render

import (
	"fmt"
	"path/filepath"
	"strings"

	"sigs.k8s.io/kustomize/api/types"
)

type kustomizeBuildSettings struct {
	LoadRestrictions types.LoadRestrictions
	APIVersions      []string
	// EnableAlphaPlugins and EnableExec are intentionally write-only: drydock
	// accepts the Argo CD build options so option-bearing repos parse, but the
	// booleans never reach krusty (which always runs builtins-only) and enable
	// no execution. They exist to record parsed intent — do not delete as dead
	// fields.
	EnableAlphaPlugins bool
	EnableExec         bool
}

func defaultKustomizeBuildSettings() kustomizeBuildSettings {
	return kustomizeBuildSettings{LoadRestrictions: types.LoadRestrictionsRootOnly}
}

func ValidateKustomizeBuildOptions(options []string) error {
	_, err := parseKustomizeBuildOptions(options)
	return err
}

// NormalizeKustomizeBuildTokens validates that tokens form a plain
// "kustomize build" invocation and returns just the supported build options,
// rejecting unsupported flags, paths, and remote operands.
func NormalizeKustomizeBuildTokens(tokens []string) ([]string, error) {
	if len(tokens) < 2 || filepath.Base(tokens[0]) != "kustomize" || tokens[1] != "build" {
		return nil, fmt.Errorf("generate command is not kustomize build")
	}
	options := make([]string, 0, len(tokens)-2)
	for i := 2; i < len(tokens); i++ {
		token := strings.TrimSpace(tokens[i])
		if token == "" || token == "." {
			continue
		}
		switch {
		case token == "--enable-helm",
			token == "--enable-alpha-plugins",
			token == "--enable-exec",
			strings.HasPrefix(token, "--helm-api-versions="),
			strings.HasPrefix(token, "--load-restrictor="):
			options = append(options, token)
		case token == "--helm-api-versions" || token == "--load-restrictor":
			if i+1 >= len(tokens) {
				return nil, fmt.Errorf("kustomize build option requires a value")
			}
			i++
			value := strings.TrimSpace(tokens[i])
			if value == "" || strings.HasPrefix(value, "-") {
				return nil, fmt.Errorf("kustomize build option requires a value")
			}
			options = append(options, token, value)
		default:
			if strings.HasPrefix(token, "-") {
				return nil, fmt.Errorf("unsupported kustomize build option")
			}
			return nil, fmt.Errorf("unsupported kustomize build path or remote operand")
		}
	}
	if err := ValidateKustomizeBuildOptions(options); err != nil {
		return nil, fmt.Errorf("unsupported kustomize build option")
	}
	return options, nil
}

func parseKustomizeBuildOptions(options []string) (kustomizeBuildSettings, error) {
	settings := defaultKustomizeBuildSettings()
	for i := 0; i < len(options); i++ {
		option := strings.TrimSpace(options[i])
		if option == "" {
			continue
		}
		switch {
		case option == "--enable-helm":
			continue
		// Argo CD accepts --enable-alpha-plugins and --enable-exec repo-wide via
		// argocd-cm; rejecting the options failed every kustomize app in such
		// repos, including apps with no generators at all. Accepting them does
		// NOT enable arbitrary exec: drydock still executes nothing — generator
		// entries are classified and either emulated natively (KSOPS under
		// --enable-ksops-compat), left to krusty's statically linked builtins,
		// or rejected with a diagnostic.
		case option == "--enable-alpha-plugins":
			settings.EnableAlphaPlugins = true
		case option == "--enable-exec":
			settings.EnableExec = true
		case option == "--helm-api-versions":
			if i+1 >= len(options) {
				return settings, fmt.Errorf("kustomize build option %q requires a value", option)
			}
			i++
			settings.APIVersions = append(settings.APIVersions, parseKustomizeHelmAPIVersions(options[i])...)
		case strings.HasPrefix(option, "--helm-api-versions="):
			settings.APIVersions = append(settings.APIVersions, parseKustomizeHelmAPIVersions(strings.TrimPrefix(option, "--helm-api-versions="))...)
		case option == "--load-restrictor":
			if i+1 >= len(options) {
				return settings, fmt.Errorf("kustomize build option %q requires a value", option)
			}
			i++
			loadRestrictions, err := parseKustomizeLoadRestrictions(options[i])
			if err != nil {
				return settings, err
			}
			settings.LoadRestrictions = loadRestrictions
		case strings.HasPrefix(option, "--load-restrictor="):
			loadRestrictions, err := parseKustomizeLoadRestrictions(strings.TrimPrefix(option, "--load-restrictor="))
			if err != nil {
				return settings, err
			}
			settings.LoadRestrictions = loadRestrictions
		default:
			return settings, fmt.Errorf("unsupported kustomize build option %q", option)
		}
	}
	return settings, nil
}

func parseKustomizeHelmAPIVersions(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func parseKustomizeLoadRestrictions(value string) (types.LoadRestrictions, error) {
	switch strings.TrimSpace(value) {
	case "LoadRestrictionsRootOnly":
		return types.LoadRestrictionsRootOnly, nil
	case "LoadRestrictionsNone":
		return types.LoadRestrictionsNone, nil
	default:
		return types.LoadRestrictionsUnknown, fmt.Errorf("unsupported kustomize load restrictor %q", value)
	}
}
