package render

import (
	"fmt"
	"strings"

	"sigs.k8s.io/kustomize/api/types"
)

type kustomizeBuildSettings struct {
	LoadRestrictions types.LoadRestrictions
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
