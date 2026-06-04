package app

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/bmatcuk/doublestar/v4"
	"github.com/sholdee/drydock/internal/pathsafety"
	"github.com/sholdee/drydock/internal/pluginpolicy"
	"github.com/sholdee/drydock/internal/render"
)

const maxPluginParameterValueBytes = 16 * 1024

var pluginParameterTemplatePattern = regexp.MustCompile(`\{\{param:([A-Za-z0-9_.-]+)\}\}`)
var pluginParameterNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

type validatedPluginParameters struct {
	byName         map[string]argoappv1.ApplicationSourcePluginParameter
	templateByName map[string]string
	extraEnv       []string
	sensitive      []string
	envNames       map[string]string
	policyByName   map[string]pluginpolicy.ExecParameter
}

func validateExecPluginParameters(name string, policy pluginpolicy.Plugin, application *render.PluginConfig, repoRoot, sourcePath string) (validatedPluginParameters, string) {
	out := newValidatedPluginParameters()
	if policy.Exec == nil {
		return out, fmt.Sprintf("config management plugin %s has invalid exec policy", pluginDisplayName(name))
	}
	return validatePolicyPluginParameters(name, policy.Exec.Parameters, policy.Exec.Copy, application, repoRoot, sourcePath, out)
}

func validateContainerPluginParameters(name string, policy pluginpolicy.Plugin, application *render.PluginConfig, repoRoot, sourcePath string) (validatedPluginParameters, string) {
	out := newValidatedPluginParameters()
	if policy.Container == nil {
		return out, fmt.Sprintf("config management plugin %s has invalid container policy", pluginDisplayName(name))
	}
	return validatePolicyPluginParameters(name, policy.Container.Lifecycle.Parameters, policy.Container.Lifecycle.Copy, application, repoRoot, sourcePath, out)
}

func validatePolicyPluginParameters(name string, parameters pluginpolicy.ExecParameters, copyPolicy pluginpolicy.ExecCopy, application *render.PluginConfig, repoRoot, sourcePath string, out validatedPluginParameters) (validatedPluginParameters, string) {
	for _, allowed := range parameters.Allow {
		out.policyByName[allowed.Name] = allowed
	}
	if application == nil {
		application = &render.PluginConfig{}
	}
	if message := recordApplicationPluginParameters(name, application.Parameters, copyPolicy, repoRoot, sourcePath, &out); message != "" {
		return out, message
	}
	if message := requireApplicationPluginParameters(name, parameters.Allow, out.byName); message != "" {
		return out, message
	}
	if message := recordApplicationPluginParameterEnv(name, application.Parameters, &out); message != "" {
		return out, message
	}
	recordSensitivePluginParameterValues(application.Parameters, &out)
	return out, ""
}

func newValidatedPluginParameters() validatedPluginParameters {
	return validatedPluginParameters{
		byName:         map[string]argoappv1.ApplicationSourcePluginParameter{},
		templateByName: map[string]string{},
		envNames:       map[string]string{},
		policyByName:   map[string]pluginpolicy.ExecParameter{},
	}
}

func recordApplicationPluginParameters(name string, params argoappv1.ApplicationSourcePluginParameters, copyPolicy pluginpolicy.ExecCopy, repoRoot, sourcePath string, out *validatedPluginParameters) string {
	for _, param := range params {
		if message := recordApplicationPluginParameter(name, param, copyPolicy, repoRoot, sourcePath, out); message != "" {
			return message
		}
	}
	return ""
}

func recordApplicationPluginParameter(name string, param argoappv1.ApplicationSourcePluginParameter, copyPolicy pluginpolicy.ExecCopy, repoRoot, sourcePath string, out *validatedPluginParameters) string {
	paramName := param.Name
	if paramName == "" {
		return fmt.Sprintf("config management plugin %s has an unnamed Application plugin parameter", pluginDisplayName(name))
	}
	if strings.TrimSpace(paramName) != paramName || !pluginParameterNamePattern.MatchString(paramName) {
		return fmt.Sprintf("config management plugin %s has invalid Application plugin parameter name %q", pluginDisplayName(name), paramName)
	}
	if _, ok := out.byName[paramName]; ok {
		return fmt.Sprintf("config management plugin %s has duplicate Application plugin parameter %q", pluginDisplayName(name), paramName)
	}
	allowed, ok := out.policyByName[paramName]
	if !ok {
		return fmt.Sprintf("config management plugin %s uses Application plugin parameter %q, which is not allowed by trusted plugin policy", pluginDisplayName(name), paramName)
	}
	templateValue, message := validateApplicationPluginParameterValue(name, allowed, param, copyPolicy, repoRoot, sourcePath)
	if message != "" {
		return message
	}
	out.byName[paramName] = param
	if allowed.Type == pluginpolicy.ExecParameterTypeString {
		out.templateByName[paramName] = templateValue
	}
	return ""
}

func requireApplicationPluginParameters(name string, allowed []pluginpolicy.ExecParameter, byName map[string]argoappv1.ApplicationSourcePluginParameter) string {
	for _, param := range allowed {
		if param.Required {
			if _, ok := byName[param.Name]; !ok {
				return fmt.Sprintf("config management plugin %s is missing required Application plugin parameter %q", pluginDisplayName(name), param.Name)
			}
		}
	}
	return ""
}

func recordApplicationPluginParameterEnv(name string, params argoappv1.ApplicationSourcePluginParameters, out *validatedPluginParameters) string {
	if len(params) == 0 {
		return ""
	}
	extraEnv, err := params.Environ()
	if err != nil {
		return fmt.Sprintf("config management plugin %s has invalid Application plugin parameters", pluginDisplayName(name))
	}
	for _, entry := range extraEnv {
		envName, _, _ := strings.Cut(entry, "=")
		if message := recordApplicationPluginParameterEnvEntry(name, envName, entry, out); message != "" {
			return message
		}
	}
	out.extraEnv = extraEnv
	return ""
}

func recordApplicationPluginParameterEnvEntry(name, envName, entry string, out *validatedPluginParameters) string {
	if envName != "ARGOCD_APP_PARAMETERS" {
		if _, ok := out.envNames[envName]; ok {
			return fmt.Sprintf("config management plugin %s has Application plugin parameters that collide in environment variable %q", pluginDisplayName(name), envName)
		}
	}
	if existing, ok := out.envNames[envName]; ok && existing != entry {
		return fmt.Sprintf("config management plugin %s has Application plugin parameters that collide in environment variable %q", pluginDisplayName(name), envName)
	}
	out.envNames[envName] = entry
	return ""
}

func recordSensitivePluginParameterValues(params argoappv1.ApplicationSourcePluginParameters, out *validatedPluginParameters) {
	for _, param := range params {
		out.sensitive = append(out.sensitive, pluginParameterValues(param)...)
	}
	for _, value := range out.templateByName {
		if value != "" {
			out.sensitive = append(out.sensitive, value)
		}
	}
}

func validateApplicationPluginParameterValue(name string, allowed pluginpolicy.ExecParameter, param argoappv1.ApplicationSourcePluginParameter, copyPolicy pluginpolicy.ExecCopy, repoRoot, sourcePath string) (string, string) {
	if applicationPluginParameterValueKinds(param) != 1 {
		return "", fmt.Sprintf("config management plugin %s parameter %q must set exactly one value type", pluginDisplayName(name), param.Name)
	}
	switch allowed.Type {
	case pluginpolicy.ExecParameterTypeString:
		return validateApplicationPluginStringParameter(name, allowed, param, copyPolicy, repoRoot, sourcePath)
	case pluginpolicy.ExecParameterTypeArray:
		return validateApplicationPluginArrayParameter(name, param)
	case pluginpolicy.ExecParameterTypeMap:
		return validateApplicationPluginMapParameter(name, param)
	default:
		return "", ""
	}
}

func applicationPluginParameterValueKinds(param argoappv1.ApplicationSourcePluginParameter) int {
	valueKinds := 0
	if param.String_ != nil {
		valueKinds++
	}
	if param.OptionalArray != nil {
		valueKinds++
	}
	if param.OptionalMap != nil {
		valueKinds++
	}
	return valueKinds
}

func validateApplicationPluginStringParameter(name string, allowed pluginpolicy.ExecParameter, param argoappv1.ApplicationSourcePluginParameter, copyPolicy pluginpolicy.ExecCopy, repoRoot, sourcePath string) (string, string) {
	if param.String_ == nil {
		return "", fmt.Sprintf("config management plugin %s parameter %q must be a string", pluginDisplayName(name), param.Name)
	}
	if len(*param.String_) > maxPluginParameterValueBytes {
		return "", fmt.Sprintf("config management plugin %s parameter %q value is too large", pluginDisplayName(name), param.Name)
	}
	if allowed.Path != nil {
		templateValue, err := validatePluginPathParameter(*param.String_, allowed.Path, copyPolicy, repoRoot, sourcePath)
		if err != nil {
			return "", fmt.Sprintf("config management plugin %s parameter %q path is not allowed: %s", pluginDisplayName(name), param.Name, err)
		}
		return templateValue, ""
	}
	return *param.String_, ""
}

func validateApplicationPluginArrayParameter(name string, param argoappv1.ApplicationSourcePluginParameter) (string, string) {
	if param.OptionalArray == nil {
		return "", fmt.Sprintf("config management plugin %s parameter %q must be an array", pluginDisplayName(name), param.Name)
	}
	for _, value := range param.Array {
		if len(value) > maxPluginParameterValueBytes {
			return "", fmt.Sprintf("config management plugin %s parameter %q value is too large", pluginDisplayName(name), param.Name)
		}
	}
	return "", ""
}

func validateApplicationPluginMapParameter(name string, param argoappv1.ApplicationSourcePluginParameter) (string, string) {
	if param.OptionalMap == nil {
		return "", fmt.Sprintf("config management plugin %s parameter %q must be a map", pluginDisplayName(name), param.Name)
	}
	envNames := map[string]string{}
	for key, value := range param.Map {
		if len(value) > maxPluginParameterValueBytes {
			return "", fmt.Sprintf("config management plugin %s parameter %q value is too large", pluginDisplayName(name), param.Name)
		}
		envName := argoPluginParameterEnvName(param.Name) + "_" + argoPluginParameterEnvName(key)
		if existing, ok := envNames[envName]; ok && existing != key {
			return "", fmt.Sprintf("config management plugin %s parameter %q map keys collide in environment variable %q", pluginDisplayName(name), param.Name, envName)
		}
		envNames[envName] = key
	}
	return "", ""
}

func validatePluginPathParameter(value string, pathPolicy *pluginpolicy.ExecParameterPath, copyPolicy pluginpolicy.ExecCopy, repoRoot, sourcePath string) (string, error) {
	if value == "" {
		return "", fmt.Errorf("path is empty")
	}
	value = filepath.ToSlash(value)
	if filepath.IsAbs(value) {
		return "", fmt.Errorf("absolute paths are not allowed")
	}
	clean, ok := pathsafety.CleanRelative(value)
	if !ok || clean == "." {
		return "", fmt.Errorf("path escapes the plugin workdir")
	}
	if len(pathPolicy.Allow) == 0 {
		return execPluginPathTemplateValue(clean, pathPolicy, copyPolicy, repoRoot, sourcePath)
	}
	for _, pattern := range pathPolicy.Allow {
		matched, err := doublestar.Match(pattern, filepath.ToSlash(clean))
		if err == nil && matched {
			return execPluginPathTemplateValue(clean, pathPolicy, copyPolicy, repoRoot, sourcePath)
		}
	}
	return "", fmt.Errorf("path does not match allowlist")
}

func execPluginPathTemplateValue(clean string, pathPolicy *pluginpolicy.ExecParameterPath, copyPolicy pluginpolicy.ExecCopy, repoRoot, sourcePath string) (string, error) {
	if pathPolicy.Base != pluginpolicy.ExecParameterPathBaseRepository {
		return filepath.ToSlash(clean), nil
	}
	if copyPolicy.Scope != pluginpolicy.ExecCopyScopeRepository {
		return "", fmt.Errorf("repository paths require copy.scope repository")
	}
	if !execPluginPathIsCopied(clean, sourcePath, copyPolicy.Include) {
		return "", fmt.Errorf("path is not included by copy.include")
	}
	if exists, err := localPathExists(filepath.Join(repoRoot, clean)); err != nil {
		return "", err
	} else if !exists {
		return "", fmt.Errorf("path does not exist")
	}
	sourceBase := filepath.Clean(sourcePath)
	if sourceBase == "." {
		return filepath.ToSlash(clean), nil
	}
	rel, err := filepath.Rel(sourceBase, clean)
	if err != nil {
		return "", err
	}
	return filepath.ToSlash(rel), nil
}

func execPluginPathIsCopied(clean, sourcePath string, include []string) bool {
	sourceBase := filepath.ToSlash(filepath.Clean(sourcePath))
	clean = filepath.ToSlash(clean)
	if sourceBase == "." || clean == sourceBase || strings.HasPrefix(clean, sourceBase+"/") {
		return true
	}
	for _, pattern := range include {
		matched, err := doublestar.Match(pattern, clean)
		if err == nil && matched {
			return true
		}
	}
	return false
}

func expandExecPluginCommandTemplates(config pluginpolicy.ExecConfig, params validatedPluginParameters) (pluginpolicy.ExecConfig, []string, string) {
	out := config
	if commandHasExecutableTemplate(config.Generate) {
		return out, nil, "uses parameter template in command executable, which is not allowed"
	}
	missing := ""
	out.Generate = expandExecCommand(config.Generate, params, &missing)
	if missing != "" {
		return out, nil, fmt.Sprintf("references missing string parameter %q", missing)
	}
	var sensitive []string
	if out.Generate.Command == nil {
		return out, nil, "generate command is empty after parameter expansion"
	}
	for _, value := range params.sensitive {
		if value != "" {
			sensitive = append(sensitive, value)
		}
	}
	if config.Init != nil {
		if commandHasExecutableTemplate(*config.Init) {
			return out, nil, "uses parameter template in command executable, which is not allowed"
		}
		init := expandExecCommand(*config.Init, params, &missing)
		if missing != "" {
			return out, nil, fmt.Sprintf("references missing string parameter %q", missing)
		}
		out.Init = &init
	}
	if len(config.PostRenderers) > 0 {
		out.PostRenderers = make([]pluginpolicy.ExecCommand, 0, len(config.PostRenderers))
		for _, command := range config.PostRenderers {
			if commandHasExecutableTemplate(command) {
				return out, nil, "uses parameter template in command executable, which is not allowed"
			}
			out.PostRenderers = append(out.PostRenderers, expandExecCommand(command, params, &missing))
			if missing != "" {
				return out, nil, fmt.Sprintf("references missing string parameter %q", missing)
			}
		}
	}
	sortSensitiveValues(sensitive)
	return out, sensitive, ""
}

func sortSensitiveValues(values []string) {
	sort.SliceStable(values, func(i, j int) bool {
		if len(values[i]) == len(values[j]) {
			return values[i] < values[j]
		}
		return len(values[i]) > len(values[j])
	})
}

func commandHasExecutableTemplate(command pluginpolicy.ExecCommand) bool {
	return len(command.Command) > 0 && pluginParameterTemplatePattern.MatchString(command.Command[0])
}

func expandExecCommand(command pluginpolicy.ExecCommand, params validatedPluginParameters, missing *string) pluginpolicy.ExecCommand {
	out := command
	out.Command = make([]string, 0, len(command.Command))
	for _, token := range command.Command {
		expanded := pluginParameterTemplatePattern.ReplaceAllStringFunc(token, func(match string) string {
			name := pluginParameterTemplatePattern.FindStringSubmatch(match)[1]
			value, ok := params.templateByName[name]
			if !ok {
				if missing != nil && *missing == "" {
					*missing = name
				}
				return ""
			}
			return value
		})
		out.Command = append(out.Command, expanded)
	}
	return out
}

func pluginParameterValues(param argoappv1.ApplicationSourcePluginParameter) []string {
	var out []string
	if param.String_ != nil {
		out = append(out, *param.String_)
	}
	if param.OptionalArray != nil {
		out = append(out, param.Array...)
	}
	if param.OptionalMap != nil {
		for _, value := range param.Map {
			out = append(out, value)
		}
	}
	return out
}

func argoPluginParameterEnvName(name string) string {
	name = strings.ToUpper(name)
	return regexp.MustCompile(`[^A-Z0-9_]`).ReplaceAllString(name, "_")
}
