package render

import (
	"fmt"
	"strings"

	"sigs.k8s.io/kustomize/api/types"
)

type kustomizeBuildSettings struct {
	LoadRestrictions types.LoadRestrictions
	APIVersions      []string
}

func defaultKustomizeBuildSettings() kustomizeBuildSettings {
	return kustomizeBuildSettings{LoadRestrictions: types.LoadRestrictionsRootOnly}
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
