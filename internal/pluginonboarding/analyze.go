package pluginonboarding

import (
	"path/filepath"
	"sort"
	"strings"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/bmatcuk/doublestar/v4"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/pathsafety"
	"github.com/sholdee/drydock/internal/pluginpolicy"
)

func Analyze(root string, inputs []ApplicationInput, settings config.ArgoSettings, existing *pluginpolicy.Policy, opts AnalyzeOptions) (Report, error) {
	if root == "" {
		root = "."
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return Report{}, err
	}
	report := Report{
		Root:                  absRoot,
		BootstrapEntrypoints:  append([]BootstrapEntrypointHint(nil), opts.BootstrapEntrypoints...),
		ExistingPolicyPresent: existing != nil,
	}

	sidecars, err := collectSidecarCandidates(absRoot)
	if err != nil {
		return report, err
	}
	report.Sidecars = sidecars

	plugins := map[string]*PluginReport{}
	for name, plugin := range settings.ConfigManagementPlugins {
		effectiveName := plugin.EffectiveName()
		if effectiveName == "" {
			effectiveName = strings.TrimSpace(name)
		}
		if effectiveName == "" {
			continue
		}
		entry := ensurePluginReport(plugins, effectiveName)
		copied := plugin
		entry.CMP = &copied
		entry.Discover = discoverMatchFromCMP(plugin)
		entry.Generate, entry.GenerateSafe = lifecycleCommandFromCMP(plugin)
	}

	seedExistingPolicyReports(existing, plugins)

	for _, input := range inputs {
		recordApplicationUses(absRoot, input, settings, plugins)
	}
	for _, hint := range opts.BootstrapEntrypoints {
		name := strings.TrimSpace(hint.Plugin)
		if name != "" {
			ensurePluginReport(plugins, name).Used = true
		}
	}

	names := make([]string, 0, len(plugins))
	for name, plugin := range plugins {
		if plugin.Used || opts.IncludeUnused {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	commandBackedCount := len(names)
	for _, name := range names {
		plugin := plugins[name]
		plugin.Sidecar = matchSidecar(name, commandBackedCount, sidecars)
		report.Plugins = append(report.Plugins, *plugin)
	}
	return report, nil
}

func seedExistingPolicyReports(policy *pluginpolicy.Policy, plugins map[string]*PluginReport) {
	if policy == nil {
		return
	}
	names := make([]string, 0, len(policy.Plugins))
	for name := range policy.Plugins {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		policyPlugin, ok := policy.Plugin(name)
		if !ok {
			continue
		}
		entry := ensurePluginReport(plugins, name)
		entry.Used = true
		entry.Discover = discoverMatchFromPolicy(policyPlugin)
		entry.Generate, entry.GenerateSafe = lifecycleCommandFromPolicy(policyPlugin)
	}
	for _, entrypoint := range policy.Bootstrap.Entrypoints {
		name := strings.TrimSpace(entrypoint.Plugin)
		if name != "" {
			ensurePluginReport(plugins, name).Used = true
		}
	}
}

func discoverMatchFromPolicy(plugin pluginpolicy.Plugin) *pluginpolicy.PluginDiscoverMatch {
	if plugin.Match != nil {
		discover := plugin.Match.Discover
		return &discover
	}
	if plugin.ConfigManagementPlugin != nil && plugin.ConfigManagementPlugin.Discover != nil {
		discover := *plugin.ConfigManagementPlugin.Discover
		return &discover
	}
	return nil
}

func lifecycleCommandFromPolicy(plugin pluginpolicy.Plugin) ([]string, bool) {
	if plugin.ConfigManagementPlugin == nil || plugin.ConfigManagementPlugin.Generate == nil {
		return nil, false
	}
	command := append([]string(nil), plugin.ConfigManagementPlugin.Generate.Command...)
	command = append(command, plugin.ConfigManagementPlugin.Generate.Args...)
	return command, len(command) > 0
}

func ensurePluginReport(plugins map[string]*PluginReport, name string) *PluginReport {
	name = strings.TrimSpace(name)
	entry, ok := plugins[name]
	if ok {
		return entry
	}
	entry = &PluginReport{Name: name}
	plugins[name] = entry
	return entry
}

func recordApplicationUses(root string, input ApplicationInput, settings config.ArgoSettings, plugins map[string]*PluginReport) {
	app := input.Application
	sources := applicationSources(app)
	for index, source := range sources {
		sourcePath := filepath.ToSlash(strings.TrimSpace(source.Path))
		if source.Plugin != nil {
			name := strings.TrimSpace(source.Plugin.Name)
			if name == "" {
				matches := staticDiscoveryPluginNames(root, sourcePath, settings)
				if len(matches) == 1 {
					name = matches[0]
				}
			}
			if name == "" {
				continue
			}
			entry := ensurePluginReport(plugins, name)
			entry.Used = true
			entry.Uses = append(entry.Uses, PluginUse{
				AppNamespace: app.Namespace,
				AppName:      app.Name,
				SourceIndex:  index,
				SourcePath:   sourcePath,
				Explicit:     strings.TrimSpace(source.Plugin.Name) != "",
				StaticMatch:  strings.TrimSpace(source.Plugin.Name) == "",
			})
			recordPluginParameters(source.Plugin.Parameters, entry)
			recordPluginEnv(source.Plugin.Env, entry)
			continue
		}
		matches := staticDiscoveryPluginNames(root, sourcePath, settings)
		if len(matches) == 1 {
			name := matches[0]
			entry := ensurePluginReport(plugins, name)
			entry.Used = true
			entry.Uses = append(entry.Uses, PluginUse{
				AppNamespace: app.Namespace,
				AppName:      app.Name,
				SourceIndex:  index,
				SourcePath:   sourcePath,
				StaticMatch:  true,
			})
		}
	}
}

func applicationSources(app argoappv1.Application) []argoappv1.ApplicationSource {
	if len(app.Spec.Sources) > 0 {
		return append([]argoappv1.ApplicationSource(nil), app.Spec.Sources...)
	}
	if app.Spec.Source != nil {
		return []argoappv1.ApplicationSource{*app.Spec.Source}
	}
	return nil
}

func recordPluginParameters(params argoappv1.ApplicationSourcePluginParameters, entry *PluginReport) {
	seen := map[string]struct{}{}
	for _, existing := range entry.Parameters {
		seen[existing.Name] = struct{}{}
	}
	for _, param := range params {
		name := strings.TrimSpace(param.Name)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		entry.Parameters = append(entry.Parameters, ParameterEvidence{Name: name, Type: parameterType(param)})
	}
	sort.Slice(entry.Parameters, func(i, j int) bool { return entry.Parameters[i].Name < entry.Parameters[j].Name })
}

func parameterType(param argoappv1.ApplicationSourcePluginParameter) pluginpolicy.ExecParameterType {
	switch {
	case param.OptionalArray != nil:
		return pluginpolicy.ExecParameterTypeArray
	case param.OptionalMap != nil:
		return pluginpolicy.ExecParameterTypeMap
	default:
		return pluginpolicy.ExecParameterTypeString
	}
}

func recordPluginEnv(env argoappv1.Env, entry *PluginReport) {
	seen := map[string]struct{}{}
	for _, existing := range entry.Env {
		seen[existing] = struct{}{}
	}
	for _, item := range env {
		if item == nil {
			continue
		}
		name := strings.TrimSpace(item.Name)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		entry.Env = append(entry.Env, name)
	}
	sort.Strings(entry.Env)
}

func staticDiscoveryPluginNames(root, sourcePath string, settings config.ArgoSettings) []string {
	if sourcePath == "" {
		return nil
	}
	names := make([]string, 0, len(settings.ConfigManagementPlugins))
	for name := range settings.ConfigManagementPlugins {
		names = append(names, name)
	}
	sort.Strings(names)
	var matches []string
	for _, name := range names {
		plugin := settings.ConfigManagementPlugins[name]
		if discoverPatternMatches(root, sourcePath, plugin.Discover) {
			if effectiveName := plugin.EffectiveName(); effectiveName != "" {
				matches = append(matches, effectiveName)
				continue
			}
			matches = append(matches, name)
		}
	}
	return matches
}

func discoverPatternMatches(root, sourcePath string, discover config.ConfigManagementPluginDiscovery) bool {
	sourcePath = filepath.ToSlash(strings.TrimSpace(sourcePath))
	sourceDir := root
	if sourcePath != "." {
		clean, ok := cleanRelativePath(sourcePath)
		if !ok {
			return false
		}
		sourceDir = filepath.Join(root, filepath.FromSlash(clean))
	}
	switch {
	case discover.FileName != "":
		pattern, ok := cleanRelativePath(discover.FileName)
		if !ok {
			return false
		}
		matches, err := filepath.Glob(filepath.Join(sourceDir, filepath.FromSlash(pattern)))
		return err == nil && len(matches) > 0
	case discover.FindGlob != "":
		pattern, ok := cleanRelativePath(discover.FindGlob)
		if !ok {
			return false
		}
		matches, err := doublestar.FilepathGlob(filepath.Join(sourceDir, filepath.FromSlash(pattern)))
		return err == nil && len(matches) > 0
	default:
		return false
	}
}

func cleanRelativePath(value string) (string, bool) {
	value = filepath.ToSlash(strings.TrimSpace(value))
	if value == "" || filepath.IsAbs(value) {
		return "", false
	}
	clean, ok := pathsafety.CleanRelative(value)
	if !ok || clean == "." {
		return "", false
	}
	return clean, true
}

func discoverMatchFromCMP(plugin config.ConfigManagementPlugin) *pluginpolicy.PluginDiscoverMatch {
	switch {
	case strings.TrimSpace(plugin.Discover.FileName) != "":
		discover := &pluginpolicy.PluginDiscoverMatch{FileName: strings.TrimSpace(plugin.Discover.FileName)}
		if discoverAccepted(discover) {
			return discover
		}
	case strings.TrimSpace(plugin.Discover.FindGlob) != "":
		discover := &pluginpolicy.PluginDiscoverMatch{FindGlob: strings.TrimSpace(plugin.Discover.FindGlob)}
		if discoverAccepted(discover) {
			return discover
		}
	}
	return nil
}

func discoverAccepted(discover *pluginpolicy.PluginDiscoverMatch) bool {
	if discover == nil {
		return false
	}
	switch {
	case discover.FileName != "":
		pattern, ok := cleanRelativePath(discover.FileName)
		if !ok {
			return false
		}
		_, err := filepath.Match(pattern, "")
		return err == nil
	case discover.FindGlob != "":
		pattern, ok := cleanRelativePath(discover.FindGlob)
		return ok && doublestar.ValidatePattern(pattern)
	default:
		return false
	}
}

func lifecycleCommandFromCMP(plugin config.ConfigManagementPlugin) ([]string, bool) {
	command := append([]string(nil), plugin.GenerateCommand...)
	command = append(command, plugin.GenerateArgs...)
	if len(command) == 0 {
		return nil, false
	}
	return command, commandAccepted(command)
}

func isDeniedCommand(command string) bool {
	switch strings.ToLower(filepath.Base(command)) {
	case "sh", "bash", "zsh", "dash", "ksh", "fish", "env", "python", "python3", "node", "ruby", "perl", "pwsh", "powershell":
		return true
	default:
		return false
	}
}

func matchSidecar(pluginName string, commandBackedCount int, sidecars []SidecarCandidate) SidecarMatch {
	var structural []SidecarCandidate
	for _, candidate := range sidecars {
		if sidecarStructurallyMatches(pluginName, candidate) {
			structural = append(structural, candidate)
		}
	}
	if len(structural) == 1 {
		candidate := structural[0]
		return SidecarMatch{Confidence: SidecarConfidenceStructural, Candidate: &candidate, Candidates: structural}
	}
	if len(structural) > 1 {
		return SidecarMatch{Confidence: SidecarConfidenceAmbiguous, Candidates: structural}
	}
	if commandBackedCount == 1 && len(sidecars) == 1 {
		candidate := sidecars[0]
		return SidecarMatch{Confidence: SidecarConfidenceSingle, Candidate: &candidate, Candidates: sidecars}
	}
	if len(sidecars) > 1 {
		return SidecarMatch{Confidence: SidecarConfidenceAmbiguous, Candidates: sidecars}
	}
	return SidecarMatch{Confidence: SidecarConfidenceNone}
}

func sidecarStructurallyMatches(pluginName string, candidate SidecarCandidate) bool {
	needle := strings.ToLower(pluginName)
	if strings.ToLower(candidate.Name) == needle {
		return true
	}
	for _, signal := range candidate.Signals {
		if strings.Contains(strings.ToLower(signal), needle) {
			return true
		}
	}
	return false
}
