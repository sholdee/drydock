package pluginpolicy

import "fmt"

type fingerprintPolicy struct {
	APIVersion string                       `json:"apiVersion"`
	Kind       string                       `json:"kind"`
	Plugins    map[string]fingerprintPlugin `json:"plugins"`
}

type fingerprintPlugin struct {
	Engine Engine                 `json:"engine"`
	Exec   *fingerprintExecConfig `json:"exec,omitempty"`
}

type fingerprintExecConfig struct {
	Workdir       string               `json:"workdir"`
	Init          *fingerprintCommand  `json:"init,omitempty"`
	Generate      fingerprintCommand   `json:"generate"`
	PostRenderers []fingerprintCommand `json:"postRenderers,omitempty"`
	Env           ExecEnv              `json:"env"`
	Output        ExecOutput           `json:"output"`
}

type fingerprintCommand struct {
	Command []string `json:"command"`
	Timeout string   `json:"timeout"`
}

func newFingerprintPlugin(plugin Plugin) (fingerprintPlugin, error) {
	out := fingerprintPlugin{Engine: plugin.Engine}
	if plugin.Engine != EngineExec {
		return out, nil
	}
	if plugin.Exec == nil {
		return fingerprintPlugin{}, fmt.Errorf("exec config is required")
	}
	out.Exec = &fingerprintExecConfig{
		Workdir: plugin.Exec.Workdir,
		Generate: fingerprintCommand{
			Command: append([]string(nil), plugin.Exec.Generate.Command...),
			Timeout: plugin.Exec.Generate.Timeout.String(),
		},
		Env: ExecEnv{
			Allow: append([]string(nil), plugin.Exec.Env.Allow...),
		},
		Output: plugin.Exec.Output,
	}
	if plugin.Exec.Init != nil {
		out.Exec.Init = &fingerprintCommand{
			Command: append([]string(nil), plugin.Exec.Init.Command...),
			Timeout: plugin.Exec.Init.Timeout.String(),
		}
	}
	if len(plugin.Exec.PostRenderers) > 0 {
		out.Exec.PostRenderers = make([]fingerprintCommand, 0, len(plugin.Exec.PostRenderers))
		for _, command := range plugin.Exec.PostRenderers {
			out.Exec.PostRenderers = append(out.Exec.PostRenderers, fingerprintCommand{
				Command: append([]string(nil), command.Command...),
				Timeout: command.Timeout.String(),
			})
		}
	}
	return out, nil
}
