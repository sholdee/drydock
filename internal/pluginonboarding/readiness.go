package pluginonboarding

import (
	"fmt"
	"slices"
	"sort"

	"github.com/sholdee/drydock/internal/pluginpolicy"
)

func Readiness(report Report, policy *pluginpolicy.Policy, opts DoctorOptions) ReadinessReport {
	var out ReadinessReport
	appIssues := map[string][]ReadinessIssue{}
	if policy == nil {
		out.Status = StatusFail
		out.Recommendations = append(out.Recommendations, issue(IssuePolicyMissing, StatusFail, "", "plugin policy is missing"))
		return out
	}

	for _, plugin := range report.Plugins {
		pluginReadiness := PluginReadiness{Name: plugin.Name, Status: StatusPass}
		policyPlugin, ok := pluginFromPolicy(policy, plugin.Name)
		if !ok {
			pluginReadiness.Issues = append(pluginReadiness.Issues, issue(IssuePolicyMissing, StatusFail, plugin.Name, fmt.Sprintf("plugin %q has no matching PluginPolicy entry", plugin.Name)))
		} else {
			pluginReadiness.Issues = append(pluginReadiness.Issues, policyGateIssues(plugin, policyPlugin, opts)...)
		}
		if plugin.Sidecar.Confidence == SidecarConfidenceAmbiguous {
			pluginReadiness.Issues = append(pluginReadiness.Issues, issue(IssueImageAmbiguous, StatusFail, plugin.Name, fmt.Sprintf("plugin %q has multiple possible sidecar images", plugin.Name)))
		}
		pluginReadiness.Status = issuesStatus(pluginReadiness.Issues)
		out.Plugins = append(out.Plugins, pluginReadiness)
		for _, use := range plugin.Uses {
			key := use.AppNamespace + "/" + use.AppName
			appIssues[key] = append(appIssues[key], pluginReadiness.Issues...)
		}
	}

	for _, plugin := range report.Plugins {
		for _, use := range plugin.Uses {
			key := use.AppNamespace + "/" + use.AppName
			if _, ok := appIssues[key]; !ok {
				appIssues[key] = nil
			}
		}
	}
	keys := make([]string, 0, len(appIssues))
	for key := range appIssues {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		namespace, name := splitAppKey(key)
		issues := dedupeIssues(appIssues[key])
		out.Applications = append(out.Applications, ApplicationReadiness{
			Namespace: namespace,
			Name:      name,
			Status:    issuesStatus(issues),
			Issues:    issues,
		})
	}
	for _, entrypoint := range report.BootstrapEntrypoints {
		if !policyHasBootstrapEntrypoint(policy, entrypoint) {
			out.Recommendations = append(out.Recommendations, issue(IssueBootstrapMissing, StatusFail, entrypoint.Plugin, fmt.Sprintf("bootstrap entrypoint %s=%s is not present in policy", entrypoint.Plugin, entrypoint.SourcePath)))
		}
	}
	out.Status = combineStatuses(out.Status, pluginReadinessStatus(out.Plugins))
	out.Status = combineStatuses(out.Status, applicationReadinessStatus(out.Applications))
	out.Status = combineStatuses(out.Status, issuesStatus(out.Recommendations))
	if out.Status == "" {
		out.Status = StatusPass
	}
	return out
}

func pluginFromPolicy(policy *pluginpolicy.Policy, name string) (pluginpolicy.Plugin, bool) {
	if policy == nil {
		return pluginpolicy.Plugin{}, false
	}
	return policy.Plugin(name)
}

func policyGateIssues(report PluginReport, plugin pluginpolicy.Plugin, opts DoctorOptions) []ReadinessIssue {
	name := report.Name
	var issues []ReadinessIssue
	switch plugin.Engine {
	case pluginpolicy.EngineExec:
		issues = append(issues, commandBackedGateIssues(name, opts)...)
		if plugin.Exec == nil {
			issues = append(issues, issue(IssuePolicyMissing, StatusFail, name, fmt.Sprintf("plugin %q has invalid exec policy", name)))
			return issues
		}
		if commandHasPlaceholder(plugin.Exec.Generate.Command) {
			issues = append(issues, issue(IssueCommandPlaceholder, StatusFail, name, fmt.Sprintf("plugin %q generate command is still a placeholder", name)))
		}
		issues = append(issues, allowlistIssues(report, plugin.Exec.Parameters.Allow, plugin.Exec.Env.Allow)...)
	case pluginpolicy.EngineContainer:
		issues = append(issues, commandBackedGateIssues(name, opts)...)
		if plugin.Container == nil {
			issues = append(issues, issue(IssuePolicyMissing, StatusFail, name, fmt.Sprintf("plugin %q has invalid container policy", name)))
			return issues
		}
		if plugin.Container.Image == PlaceholderImage {
			issues = append(issues, issue(IssueImagePlaceholder, StatusFail, name, fmt.Sprintf("plugin %q image is still a placeholder", name)))
		}
		if plugin.Container.AllowMutableImageTag {
			status := StatusWarn
			if opts.Strict {
				status = StatusFail
			}
			issues = append(issues, issue(IssueImageMutable, status, name, fmt.Sprintf("plugin %q uses a mutable image tag; digest pinning is recommended", name)))
		}
		if commandHasPlaceholder(plugin.Container.Lifecycle.Generate.Command) {
			issues = append(issues, issue(IssueCommandPlaceholder, StatusFail, name, fmt.Sprintf("plugin %q generate command is still a placeholder", name)))
		}
		issues = append(issues, allowlistIssues(report, plugin.Container.Lifecycle.Parameters.Allow, plugin.Container.Lifecycle.Env.Allow)...)
	case pluginpolicy.EngineAVPCompat, pluginpolicy.EngineNativeKustomize:
	default:
	}
	return issues
}

func commandBackedGateIssues(name string, opts DoctorOptions) []ReadinessIssue {
	var issues []ReadinessIssue
	if !opts.EnablePlugins {
		issues = append(issues, issue(IssuePluginsDisabled, StatusFail, name, fmt.Sprintf("plugin %q requires --enable-plugins for command-backed rendering", name)))
	}
	if !opts.TrustedPolicy {
		issues = append(issues, issue(IssuePolicyUntrusted, StatusFail, name, fmt.Sprintf("plugin %q requires trusted policy provenance such as --plugin-policy-ref", name)))
	}
	return issues
}

func allowlistIssues(report PluginReport, allowedParams []pluginpolicy.ExecParameter, allowedEnv []string) []ReadinessIssue {
	var issues []ReadinessIssue
	params := map[string]struct{}{}
	for _, allowed := range allowedParams {
		params[allowed.Name] = struct{}{}
	}
	for _, observed := range report.Parameters {
		if _, ok := params[observed.Name]; !ok {
			issues = append(issues, issue(IssueParamsMissingAllow, StatusFail, report.Name, fmt.Sprintf("plugin %q observes Application parameter %q that is not allowed by policy", report.Name, observed.Name)))
		}
	}
	env := map[string]struct{}{}
	for _, allowed := range allowedEnv {
		env[allowed] = struct{}{}
	}
	for _, observed := range report.Env {
		if _, ok := env[observed]; !ok {
			issues = append(issues, issue(IssueEnvMissingAllow, StatusFail, report.Name, fmt.Sprintf("plugin %q observes Application env %q that is not allowed by policy", report.Name, observed)))
		}
	}
	return issues
}

func commandHasPlaceholder(command []string) bool {
	return slices.Contains(command, PlaceholderCommandToken)
}

func policyHasBootstrapEntrypoint(policy *pluginpolicy.Policy, hint BootstrapEntrypointHint) bool {
	if policy == nil {
		return false
	}
	sourcePath := cleanBootstrapSourcePath(hint.SourcePath)
	if sourcePath == "" {
		sourcePath = hint.SourcePath
	}
	for _, entrypoint := range policy.Bootstrap.Entrypoints {
		if entrypoint.Plugin == hint.Plugin && entrypoint.SourcePath == sourcePath {
			return true
		}
	}
	return false
}

func issue(code, status, plugin, message string) ReadinessIssue {
	return ReadinessIssue{Code: code, Status: status, Plugin: plugin, Message: message}
}

func issuesStatus(issues []ReadinessIssue) string {
	status := StatusPass
	for _, item := range issues {
		status = combineStatuses(status, item.Status)
	}
	return status
}

func pluginReadinessStatus(items []PluginReadiness) string {
	status := StatusPass
	for _, item := range items {
		status = combineStatuses(status, item.Status)
	}
	return status
}

func applicationReadinessStatus(items []ApplicationReadiness) string {
	status := StatusPass
	for _, item := range items {
		status = combineStatuses(status, item.Status)
	}
	return status
}

func combineStatuses(left, right string) string {
	if left == StatusFail || right == StatusFail {
		return StatusFail
	}
	if left == StatusWarn || right == StatusWarn {
		return StatusWarn
	}
	if left == StatusInfo || right == StatusInfo {
		return StatusInfo
	}
	if left == "" {
		return right
	}
	return left
}

func splitAppKey(key string) (string, string) {
	for i, r := range key {
		if r == '/' {
			return key[:i], key[i+1:]
		}
	}
	return "", key
}

func dedupeIssues(issues []ReadinessIssue) []ReadinessIssue {
	seen := map[string]struct{}{}
	var out []ReadinessIssue
	for _, item := range issues {
		key := item.Code + "\x00" + item.Plugin + "\x00" + item.Message
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Plugin != out[j].Plugin {
			return out[i].Plugin < out[j].Plugin
		}
		return out[i].Code < out[j].Code
	})
	return out
}
