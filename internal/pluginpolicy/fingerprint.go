package pluginpolicy

import (
	"fmt"
	"maps"
	"sort"
	"strings"
)

type fingerprintPolicy struct {
	APIVersion string                       `json:"apiVersion"`
	Kind       string                       `json:"kind"`
	Bootstrap  *fingerprintBootstrap        `json:"bootstrap,omitempty"`
	Plugins    map[string]fingerprintPlugin `json:"plugins"`
}

type fingerprintBootstrap struct {
	Entrypoints []fingerprintBootstrapEntrypoint `json:"entrypoints"`
}

type fingerprintBootstrapEntrypoint struct {
	Name       string                 `json:"name"`
	Plugin     string                 `json:"plugin"`
	SourcePath string                 `json:"sourcePath"`
	Parameters []fingerprintParameter `json:"parameters,omitempty"`
}

type fingerprintParameter struct {
	Name   string                         `json:"name"`
	String *string                        `json:"string,omitempty"`
	Array  *fingerprintParameterArray     `json:"array,omitempty"`
	Map    *fingerprintParameterStringMap `json:"map,omitempty"`
}

type fingerprintParameterArray struct {
	Values []string `json:"values"`
}

type fingerprintParameterStringMap struct {
	Values map[string]string `json:"values"`
}

type fingerprintPlugin struct {
	Engine                 Engine                             `json:"engine"`
	Match                  *fingerprintPluginMatch            `json:"match,omitempty"`
	ConfigManagementPlugin *fingerprintConfigManagementPlugin `json:"configManagementPlugin,omitempty"`
	Exec                   *fingerprintLifecycleConfig        `json:"exec,omitempty"`
	Container              *fingerprintContainerConfig        `json:"container,omitempty"`
}

type fingerprintPluginMatch struct {
	Discover fingerprintPluginDiscoverMatch `json:"discover"`
}

type fingerprintPluginDiscoverMatch struct {
	FileName string               `json:"fileName,omitempty"`
	Find     *fingerprintFindGlob `json:"find,omitempty"`
}

type fingerprintFindGlob struct {
	Glob string `json:"glob"`
}

type fingerprintConfigManagementPlugin struct {
	Discover *fingerprintPluginDiscoverMatch       `json:"discover,omitempty"`
	Generate *fingerprintConfigManagementPluginGen `json:"generate,omitempty"`
}

type fingerprintConfigManagementPluginGen struct {
	Command []string `json:"command,omitempty"`
	Args    []string `json:"args,omitempty"`
}

type fingerprintLifecycleConfig struct {
	Workdir       string               `json:"workdir"`
	Copy          ExecCopy             `json:"copy"`
	Init          *fingerprintCommand  `json:"init,omitempty"`
	Generate      fingerprintCommand   `json:"generate"`
	PostRenderers []fingerprintCommand `json:"postRenderers,omitempty"`
	Env           ExecEnv              `json:"env"`
	Parameters    ExecParameters       `json:"parameters"`
	Output        ExecOutput           `json:"output"`
}

type fingerprintCommand struct {
	Command []string `json:"command"`
	Timeout string   `json:"timeout"`
}

type fingerprintContainerConfig struct {
	Runtime              ContainerRuntime           `json:"runtime"`
	Image                string                     `json:"image"`
	AllowMutableImageTag bool                       `json:"allowMutableImageTag"`
	Network              ContainerNetwork           `json:"network"`
	CacheMounts          []fingerprintCacheMount    `json:"cacheMounts,omitempty"`
	Lifecycle            fingerprintLifecycleConfig `json:"lifecycle"`
}

type fingerprintCacheMount struct {
	Name   string `json:"name"`
	Target string `json:"target"`
}

func newFingerprintPlugin(plugin Plugin) (fingerprintPlugin, error) {
	out := fingerprintPlugin{
		Engine:                 plugin.Engine,
		Match:                  clonePluginMatch(plugin.Match),
		ConfigManagementPlugin: cloneConfigManagementPluginSeed(plugin.ConfigManagementPlugin),
	}
	switch plugin.Engine {
	case EngineAVPCompat, EngineNativeKustomize:
		return out, nil
	case EngineExec:
		if plugin.Exec == nil {
			if plugin.ConfigManagementPlugin != nil {
				return out, nil
			}
			return fingerprintPlugin{}, fmt.Errorf("exec config is required")
		}
		out.Exec = newFingerprintLifecycleConfig(plugin.Exec)
	case EngineContainer:
		if plugin.Container == nil {
			return fingerprintPlugin{}, fmt.Errorf("container config is required")
		}
		out.Container = &fingerprintContainerConfig{
			Runtime:              plugin.Container.Runtime,
			Image:                plugin.Container.Image,
			AllowMutableImageTag: plugin.Container.AllowMutableImageTag,
			Network:              plugin.Container.Network,
			CacheMounts:          newFingerprintContainerCacheMounts(plugin.Container.CacheMounts),
			Lifecycle:            *newFingerprintLifecycleConfig(&plugin.Container.Lifecycle),
		}
	default:
		return fingerprintPlugin{}, fmt.Errorf("unsupported engine %q", plugin.Engine)
	}
	return out, nil
}

func newFingerprintContainerCacheMounts(input []ContainerCacheMount) []fingerprintCacheMount {
	if len(input) == 0 {
		return nil
	}
	out := make([]fingerprintCacheMount, 0, len(input))
	for _, mount := range input {
		out = append(out, fingerprintCacheMount{Name: mount.Name, Target: mount.Target})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out
}

func newFingerprintLifecycleConfig(config *ExecConfig) *fingerprintLifecycleConfig {
	out := &fingerprintLifecycleConfig{
		Workdir: config.Workdir,
		Copy: ExecCopy{
			Scope:   config.Copy.Scope,
			Include: append([]string(nil), config.Copy.Include...),
		},
		Generate: fingerprintCommand{
			Command: append([]string(nil), config.Generate.Command...),
			Timeout: config.Generate.Timeout.String(),
		},
		Env: ExecEnv{
			Allow: append([]string(nil), config.Env.Allow...),
		},
		Parameters: cloneExecParameters(config.Parameters),
		Output:     config.Output,
	}
	if config.Init != nil {
		out.Init = &fingerprintCommand{
			Command: append([]string(nil), config.Init.Command...),
			Timeout: config.Init.Timeout.String(),
		}
	}
	if len(config.PostRenderers) > 0 {
		out.PostRenderers = make([]fingerprintCommand, 0, len(config.PostRenderers))
		for _, command := range config.PostRenderers {
			out.PostRenderers = append(out.PostRenderers, fingerprintCommand{
				Command: append([]string(nil), command.Command...),
				Timeout: command.Timeout.String(),
			})
		}
	}
	return out
}

func newFingerprintBootstrap(bootstrap Bootstrap, plugins map[string]Plugin) (fingerprintBootstrap, bool, error) {
	if len(bootstrap.Entrypoints) == 0 {
		return fingerprintBootstrap{}, false, nil
	}
	seen := map[string]struct{}{}
	entrypoints := make([]fingerprintBootstrapEntrypoint, 0, len(bootstrap.Entrypoints))
	for _, entrypoint := range bootstrap.Entrypoints {
		name := strings.TrimSpace(entrypoint.Name)
		if name == "" {
			return fingerprintBootstrap{}, false, fmt.Errorf("bootstrap entrypoint name is empty")
		}
		if len(name) > 63 || !bootstrapEntrypointNamePattern.MatchString(name) {
			return fingerprintBootstrap{}, false, fmt.Errorf("bootstrap entrypoint %q is invalid", name)
		}
		if _, ok := seen[name]; ok {
			return fingerprintBootstrap{}, false, fmt.Errorf("duplicate bootstrap entrypoint name %q", name)
		}
		seen[name] = struct{}{}
		pluginName := strings.TrimSpace(entrypoint.Plugin)
		plugin, ok := plugins[pluginName]
		if !ok {
			return fingerprintBootstrap{}, false, fmt.Errorf("bootstrap entrypoint %q references unknown plugin %q", name, pluginName)
		}
		if _, _, ok := bootstrapPluginDiscoverRule(plugin); !ok {
			return fingerprintBootstrap{}, false, fmt.Errorf("bootstrap entrypoint %q plugin %q must define match.discover or configManagementPlugin.discover", name, pluginName)
		}
		sourcePath, err := cleanBootstrapSourcePath(entrypoint.SourcePath)
		if err != nil {
			return fingerprintBootstrap{}, false, fmt.Errorf("bootstrap entrypoint %q sourcePath: %w", name, err)
		}
		parameters, err := cloneBootstrapParameters(entrypoint.Parameters)
		if err != nil {
			return fingerprintBootstrap{}, false, fmt.Errorf("bootstrap entrypoint %q: %w", name, err)
		}
		entrypoints = append(entrypoints, fingerprintBootstrapEntrypoint{
			Name:       name,
			Plugin:     pluginName,
			SourcePath: sourcePath,
			Parameters: parameters,
		})
	}
	sort.Slice(entrypoints, func(i, j int) bool {
		return entrypoints[i].Name < entrypoints[j].Name
	})
	return fingerprintBootstrap{Entrypoints: entrypoints}, true, nil
}

func cloneBootstrapParameters(input []BootstrapParameter) ([]fingerprintParameter, error) {
	if len(input) == 0 {
		return nil, nil
	}
	seen := map[string]struct{}{}
	out := make([]fingerprintParameter, 0, len(input))
	for _, parameter := range input {
		name := strings.TrimSpace(parameter.Name)
		if name == "" {
			return nil, fmt.Errorf("bootstrap parameter name is empty")
		}
		if !parameterNamePattern.MatchString(name) {
			return nil, fmt.Errorf("bootstrap parameter name %q is invalid", name)
		}
		if _, ok := seen[name]; ok {
			return nil, fmt.Errorf("duplicate bootstrap parameter name %q", name)
		}
		seen[name] = struct{}{}
		count := 0
		cloned := fingerprintParameter{Name: name}
		if parameter.String != nil {
			value := *parameter.String
			cloned.String = &value
			count++
		}
		if parameter.Array != nil {
			cloned.Array = &fingerprintParameterArray{Values: append([]string(nil), parameter.Array.Values...)}
			count++
		}
		if parameter.Map != nil {
			values := map[string]string{}
			maps.Copy(values, parameter.Map.Values)
			cloned.Map = &fingerprintParameterStringMap{Values: values}
			count++
		}
		if count != 1 {
			return nil, fmt.Errorf("bootstrap parameter %q must contain exactly one of string, array, or map", name)
		}
		out = append(out, cloned)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func cloneConfigManagementPluginSeed(input *ConfigManagementPluginSeed) *fingerprintConfigManagementPlugin {
	if input == nil {
		return nil
	}
	out := &fingerprintConfigManagementPlugin{}
	if input.Discover != nil {
		discover := clonePluginDiscoverMatch(*input.Discover)
		out.Discover = &discover
	}
	if input.Generate != nil {
		out.Generate = &fingerprintConfigManagementPluginGen{
			Command: append([]string(nil), input.Generate.Command...),
			Args:    append([]string(nil), input.Generate.Args...),
		}
	}
	return out
}

func clonePluginMatch(input *PluginMatch) *fingerprintPluginMatch {
	if input == nil {
		return nil
	}
	out := &fingerprintPluginMatch{
		Discover: clonePluginDiscoverMatch(input.Discover),
	}
	return out
}

func clonePluginDiscoverMatch(input PluginDiscoverMatch) fingerprintPluginDiscoverMatch {
	out := fingerprintPluginDiscoverMatch{
		FileName: input.FileName,
	}
	if input.FindGlob != "" {
		out.Find = &fingerprintFindGlob{Glob: input.FindGlob}
	}
	return out
}

func cloneExecParameters(input ExecParameters) ExecParameters {
	out := ExecParameters{Allow: make([]ExecParameter, 0, len(input.Allow))}
	for _, parameter := range input.Allow {
		cloned := ExecParameter{
			Name:     parameter.Name,
			Type:     parameter.Type,
			Required: parameter.Required,
		}
		if parameter.Path != nil {
			cloned.Path = &ExecParameterPath{
				Base:  parameter.Path.Base,
				Allow: append([]string(nil), parameter.Path.Allow...),
			}
		}
		out.Allow = append(out.Allow, cloned)
	}
	return out
}
