package pluginpolicy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseValidPolicyNormalizesPluginNames(t *testing.T) {
	policy, err := Parse("policy.yaml", []byte(`apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
plugins:
  " avp ":
    engine: avp-compat
  native:
    engine: native-kustomize
`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(policy.Plugins) != 2 {
		t.Fatalf("Plugins = %#v, want two plugins", policy.Plugins)
	}
	if _, ok := policy.Plugins[" avp "]; ok {
		t.Fatalf("Plugins retained unnormalized key: %#v", policy.Plugins)
	}
	plugin, ok := policy.Plugin(" avp ")
	if !ok {
		t.Fatalf("Plugin() did not find trimmed name")
	}
	if plugin.Engine != EngineAVPCompat {
		t.Fatalf("Plugin().Engine = %q", plugin.Engine)
	}
	if policy.Plugins["native"].Engine != EngineNativeKustomize {
		t.Fatalf("native engine = %q", policy.Plugins["native"].Engine)
	}
}

func TestPluginPolicySchemaMatchesCurrentContract(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "schemas", "plugin-policy.schema.json"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var schema map[string]any
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatalf("schema JSON is invalid: %v", err)
	}

	properties := schemaObject(t, schema, "properties")
	assertSchemaConst(t, schemaObject(t, properties, "apiVersion"), apiVersion)
	assertSchemaConst(t, schemaObject(t, properties, "kind"), kind)

	defs := schemaObject(t, schema, "$defs")
	for name, engine := range map[string]Engine{
		"avpCompatPlugin":       EngineAVPCompat,
		"nativeKustomizePlugin": EngineNativeKustomize,
	} {
		def := schemaObject(t, defs, name)
		engineProp := schemaObject(t, schemaObject(t, def, "properties"), "engine")
		assertSchemaConst(t, engineProp, string(engine))
	}
	execDef := schemaObject(t, defs, "execPlugin")
	engineProp := schemaObject(t, schemaObject(t, execDef, "properties"), "engine")
	assertSchemaConst(t, engineProp, string(EngineExec))

	timeout := schemaObject(t, schemaObject(t, schemaObject(t, defs, "command"), "properties"), "timeout")
	if _, ok := timeout["pattern"]; ok {
		t.Fatalf("schema timeout has pattern %#v, want parser-authoritative Go duration string", timeout["pattern"])
	}
	if _, err := Parse("policy.yaml", []byte(`apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
plugins:
  exec:
    engine: exec
    generate:
      command: ["renderer"]
      timeout: 1.5s
`)); err != nil {
		t.Fatalf("Parse() fractional duration error = %v", err)
	}
}

func TestParseAllowsOmittedAndEmptyPlugins(t *testing.T) {
	for _, tt := range []struct {
		name string
		data string
	}{
		{
			name: "omitted",
			data: `apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
`,
		},
		{
			name: "empty mapping",
			data: `apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
plugins: {}
`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			policy, err := Parse("policy.yaml", []byte(tt.data))
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if policy.Plugins == nil || len(policy.Plugins) != 0 {
				t.Fatalf("Plugins = %#v, want empty non-nil map", policy.Plugins)
			}
		})
	}
}

func TestParseExecPolicyAppliesDefaults(t *testing.T) {
	policy, err := Parse("policy.yaml", []byte(`apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
plugins:
  avp-directory-include:
    engine: exec
    generate:
      command: ["argocd-vault-plugin", "generate", "."]
`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	plugin, ok := policy.Plugin("avp-directory-include")
	if !ok {
		t.Fatal("Plugin() did not find exec plugin")
	}
	if plugin.Engine != EngineExec {
		t.Fatalf("Engine = %q, want %q", plugin.Engine, EngineExec)
	}
	if plugin.Exec == nil {
		t.Fatal("Exec = nil, want config")
	}
	if plugin.Exec.Workdir != ExecWorkdirSource {
		t.Fatalf("Workdir = %q, want %q", plugin.Exec.Workdir, ExecWorkdirSource)
	}
	if got := plugin.Exec.Generate.Timeout; got != DefaultGenerateTimeout {
		t.Fatalf("Generate.Timeout = %s, want %s", got, DefaultGenerateTimeout)
	}
	if got := plugin.Exec.Output.MaxStdoutBytes; got != DefaultMaxStdoutBytes {
		t.Fatalf("MaxStdoutBytes = %d, want %d", got, DefaultMaxStdoutBytes)
	}
	if got := plugin.Exec.Output.MaxStderrBytes; got != DefaultMaxStderrBytes {
		t.Fatalf("MaxStderrBytes = %d, want %d", got, DefaultMaxStderrBytes)
	}
}

func TestParseExecPolicyIsRuntimeGateNeutral(t *testing.T) {
	policy, err := Parse("policy.yaml", []byte(`apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
plugins:
  disabled-at-runtime:
    engine: exec
    generate:
      command: ["argocd-vault-plugin", "generate", "."]
`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	plugin, ok := policy.Plugin("disabled-at-runtime")
	if !ok {
		t.Fatal("Plugin() did not find exec plugin")
	}
	if plugin.Engine != EngineExec || plugin.Exec == nil {
		t.Fatalf("Plugin() = %#v, want parsed exec plugin", plugin)
	}
}

func TestParseExecPolicyExplicitFields(t *testing.T) {
	policy, err := Parse("policy.yaml", []byte(`apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
plugins:
  renderer:
    engine: exec
    workdir: source
    init:
      command: ["argocd-vault-plugin", "version"]
      timeout: 2s
    generate:
      command: ["argocd-vault-plugin", "generate", "."]
      timeout: 3s
    postRenderers:
      - command: ["post-render", "--add-labels"]
        timeout: 4s
    env:
      allow: [" OP_CONNECT_HOST ", "AVP_TYPE"]
    output:
      maxStdoutBytes: 1024
      maxStderrBytes: 128
`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	plugin, ok := policy.Plugin("renderer")
	if !ok || plugin.Exec == nil || plugin.Exec.Init == nil {
		t.Fatalf("Plugin(renderer) = %#v, want exec plugin with init", plugin)
	}
	if got := plugin.Exec.Init.Timeout.String(); got != "2s" {
		t.Fatalf("Init timeout = %s, want 2s", got)
	}
	if got := plugin.Exec.Generate.Timeout.String(); got != "3s" {
		t.Fatalf("Generate timeout = %s, want 3s", got)
	}
	if len(plugin.Exec.PostRenderers) != 1 {
		t.Fatalf("len(PostRenderers) = %d, want 1", len(plugin.Exec.PostRenderers))
	}
	if got := strings.Join(plugin.Exec.PostRenderers[0].Command, " "); got != "post-render --add-labels" {
		t.Fatalf("PostRenderers[0].Command = %q, want post-render --add-labels", got)
	}
	if got := plugin.Exec.PostRenderers[0].Timeout.String(); got != "4s" {
		t.Fatalf("PostRenderers[0].Timeout = %s, want 4s", got)
	}
	if got := strings.Join(plugin.Exec.Env.Allow, ","); got != "AVP_TYPE,OP_CONNECT_HOST" {
		t.Fatalf("Env allow = %q, want AVP_TYPE,OP_CONNECT_HOST", got)
	}
	if plugin.Exec.Output.MaxStdoutBytes != 1024 || plugin.Exec.Output.MaxStderrBytes != 128 {
		t.Fatalf("Output = %#v, want explicit limits", plugin.Exec.Output)
	}
}

func TestParseFixturePolicies(t *testing.T) {
	tests := []struct {
		path           string
		plugin         string
		engine         Engine
		postRenderers  int
		allowedEnvVars []string
	}{
		{
			path:   "avp-placeholder.yaml",
			plugin: "avp-directory-include",
			engine: EngineAVPCompat,
		},
		{
			path:           "exec-post-renderer.yaml",
			plugin:         "ytt-render",
			engine:         EngineExec,
			postRenderers:  1,
			allowedEnvVars: []string{"CLUSTER_NAME", "ENVIRONMENT"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			policyPath := filepath.Join("..", "..", "testdata", "plugin-policy", tt.path)
			data, err := os.ReadFile(policyPath)
			if err != nil {
				t.Fatalf("ReadFile(%q) error = %v", policyPath, err)
			}
			policy, err := Parse(policyPath, data)
			if err != nil {
				t.Fatalf("Parse(%q) error = %v", policyPath, err)
			}
			plugin, ok := policy.Plugin(tt.plugin)
			if !ok {
				t.Fatalf("Plugin(%q) missing in fixture", tt.plugin)
			}
			if plugin.Engine != tt.engine {
				t.Fatalf("Engine = %q, want %q", plugin.Engine, tt.engine)
			}
			if plugin.Engine == EngineExec {
				if plugin.Exec == nil {
					t.Fatal("Exec = nil, want parsed exec config")
				}
				if got := len(plugin.Exec.PostRenderers); got != tt.postRenderers {
					t.Fatalf("len(PostRenderers) = %d, want %d", got, tt.postRenderers)
				}
				if got := strings.Join(plugin.Exec.Env.Allow, ","); got != strings.Join(tt.allowedEnvVars, ",") {
					t.Fatalf("Env.Allow = %q, want %q", got, strings.Join(tt.allowedEnvVars, ","))
				}
			}
			if fingerprint, err := Fingerprint(policy); err != nil {
				t.Fatalf("Fingerprint(%q) error = %v", policyPath, err)
			} else if fingerprint == NoPolicyFingerprint {
				t.Fatalf("Fingerprint(%q) = NoPolicyFingerprint, want stable non-empty fingerprint", policyPath)
			}
		})
	}
}

func TestParseExecPolicyPostRendererDefaultTimeout(t *testing.T) {
	policy, err := Parse("policy.yaml", []byte(`apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
plugins:
  renderer:
    engine: exec
    generate:
      command: ["argocd-vault-plugin", "generate", "."]
    postRenderers:
      - command: ["post-render", "normalize"]
`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	plugin, ok := policy.Plugin("renderer")
	if !ok || plugin.Exec == nil {
		t.Fatalf("Plugin(renderer) = %#v, want exec plugin", plugin)
	}
	if got := len(plugin.Exec.PostRenderers); got != 1 {
		t.Fatalf("len(PostRenderers) = %d, want 1", got)
	}
	if got := plugin.Exec.PostRenderers[0].Timeout; got != DefaultPostRendererTimeout {
		t.Fatalf("PostRenderers[0].Timeout = %s, want %s", got, DefaultPostRendererTimeout)
	}
}

func TestParseExecPolicyPostRenderers(t *testing.T) {
	policy, err := Parse("policy.yaml", []byte(`apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
plugins:
  renderer:
    engine: exec
    generate:
      command: ["argocd-vault-plugin", "generate", "."]
    postRenderers:
      - command: ["post-render", "normalize"]
        timeout: 4s
      - command: ["/usr/local/bin/post-filter", "--strict"]
        timeout: 5s
`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	plugin, ok := policy.Plugin("renderer")
	if !ok || plugin.Exec == nil {
		t.Fatalf("Plugin(renderer) = %#v, want exec plugin", plugin)
	}
	if got := len(plugin.Exec.PostRenderers); got != 2 {
		t.Fatalf("len(PostRenderers) = %d, want 2", got)
	}
	if got := strings.Join(plugin.Exec.PostRenderers[0].Command, " "); got != "post-render normalize" {
		t.Fatalf("PostRenderers[0].Command = %q, want post-render normalize", got)
	}
	if got := plugin.Exec.PostRenderers[0].Timeout.String(); got != "4s" {
		t.Fatalf("PostRenderers[0].Timeout = %s, want 4s", got)
	}
	if got := strings.Join(plugin.Exec.PostRenderers[1].Command, " "); got != "/usr/local/bin/post-filter --strict" {
		t.Fatalf("PostRenderers[1].Command = %q, want /usr/local/bin/post-filter --strict", got)
	}
	if got := plugin.Exec.PostRenderers[1].Timeout.String(); got != "5s" {
		t.Fatalf("PostRenderers[1].Timeout = %s, want 5s", got)
	}
}

func TestParseExecPolicyRejectsUnsafeFields(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
		want string
	}{
		{
			name: "missing generate",
			body: `    engine: exec
`,
			want: "missing required field $.plugins.renderer.generate",
		},
		{
			name: "unsupported workdir",
			body: `    engine: exec
    workdir: repo
    generate:
      command: ["argocd-vault-plugin"]
`,
			want: "workdir",
		},
		{
			name: "empty command",
			body: `    engine: exec
    generate:
      command: []
`,
			want: "must not be empty",
		},
		{
			name: "empty command token",
			body: `    engine: exec
    generate:
      command: ["argocd-vault-plugin", ""]
`,
			want: "must not be empty",
		},
		{
			name: "shell string command value",
			body: `    engine: exec
    generate:
      command: argocd-vault-plugin generate .
`,
			want: "must be a sequence",
		},
		{
			name: "non string command token",
			body: `    engine: exec
    generate:
      command: ["argocd-vault-plugin", 1]
`,
			want: "must be a string",
		},
		{
			name: "bad duration",
			body: `    engine: exec
    generate:
      command: ["argocd-vault-plugin"]
      timeout: soon
`,
			want: "must be a duration",
		},
		{
			name: "relative command path",
			body: `    engine: exec
    generate:
      command: ["./render.sh"]
`,
			want: "basename or absolute path",
		},
		{
			name: "shell command",
			body: `    engine: exec
    generate:
      command: ["sh", "./render.sh"]
`,
			want: "not permitted",
		},
		{
			name: "reserved env",
			body: `    engine: exec
    generate:
      command: ["argocd-vault-plugin"]
    env:
      allow: ["PATH"]
`,
			want: "reserved",
		},
		{
			name: "duplicate env after trim",
			body: `    engine: exec
    generate:
      command: ["argocd-vault-plugin"]
    env:
      allow: ["AVP_TYPE", " AVP_TYPE "]
`,
			want: "duplicate env name",
		},
		{
			name: "empty env after trim",
			body: `    engine: exec
    generate:
      command: ["argocd-vault-plugin"]
    env:
      allow: [" "]
`,
			want: "must not be empty",
		},
		{
			name: "non string env value",
			body: `    engine: exec
    generate:
      command: ["argocd-vault-plugin"]
    env:
      allow: [1]
`,
			want: "must be a string",
		},
		{
			name: "empty post renderers",
			body: `    engine: exec
    postRenderers: []
    generate:
      command: ["argocd-vault-plugin"]
`,
			want: "must not be empty",
		},
		{
			name: "null post renderers",
			body: `    engine: exec
    generate:
      command: ["argocd-vault-plugin"]
    postRenderers: null
`,
			want: "postRenderers must be a sequence",
		},
		{
			name: "invalid post renderer item",
			body: `    engine: exec
    generate:
      command: ["argocd-vault-plugin"]
    postRenderers:
      - post-render normalize
`,
			want: "postRenderers[0] must be a mapping",
		},
		{
			name: "invalid post renderer",
			body: `    engine: exec
    generate:
      command: ["argocd-vault-plugin"]
    postRenderers:
      - command: ["sh", "-c", "cat"]
`,
			want: "not permitted",
		},
		{
			name: "unknown exec field",
			body: `    engine: exec
    random: true
    generate:
      command: ["argocd-vault-plugin"]
`,
			want: "unknown field",
		},
		{
			name: "bad output limit",
			body: `    engine: exec
    generate:
      command: ["argocd-vault-plugin"]
    output:
      maxStdoutBytes: 0
`,
			want: "greater than zero",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse("policy.yaml", []byte(`apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
plugins:
  renderer:
`+tt.body))
			if err == nil {
				t.Fatalf("Parse() succeeded, want error containing %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Parse() error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestFingerprintExecPolicyIsDeterministic(t *testing.T) {
	left, err := Parse("policy.yaml", []byte(`apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
plugins:
  zeta:
    engine: avp-compat
  renderer:
    engine: exec
    generate:
      timeout: 3s
      command: ["argocd-vault-plugin", "generate", "."]
    init:
      timeout: 2s
      command: ["argocd-vault-plugin", "version"]
    env:
      allow: [" OP_CONNECT_HOST ", "AVP_TYPE"]
`))
	if err != nil {
		t.Fatalf("Parse(left) error = %v", err)
	}
	right, err := Parse("policy.yaml", []byte(`kind: PluginPolicy
apiVersion: drydock.sholdee.dev/v1alpha1
plugins:
  renderer:
    env:
      allow: ["AVP_TYPE", "OP_CONNECT_HOST"]
    init:
      command: ["argocd-vault-plugin", "version"]
      timeout: 2s
    generate:
      command: ["argocd-vault-plugin", "generate", "."]
      timeout: 3s
    engine: exec
  zeta:
    engine: avp-compat
`))
	if err != nil {
		t.Fatalf("Parse(right) error = %v", err)
	}
	leftFingerprint, err := Fingerprint(left)
	if err != nil {
		t.Fatalf("Fingerprint(left) error = %v", err)
	}
	rightFingerprint, err := Fingerprint(right)
	if err != nil {
		t.Fatalf("Fingerprint(right) error = %v", err)
	}
	if leftFingerprint != rightFingerprint {
		t.Fatalf("fingerprints differ:\nleft:  %s\nright: %s", leftFingerprint, rightFingerprint)
	}
}

func TestFingerprintExecPostRenderersAreDeterministic(t *testing.T) {
	left, err := Parse("policy.yaml", []byte(`apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
plugins:
  renderer:
    engine: exec
    generate:
      command: ["argocd-vault-plugin", "generate", "."]
    postRenderers:
      - timeout: 2s
        command: ["post-render", "normalize"]
      - command: ["post-render", "filter"]
        timeout: 3s
`))
	if err != nil {
		t.Fatalf("Parse(left) error = %v", err)
	}
	right, err := Parse("policy.yaml", []byte(`kind: PluginPolicy
apiVersion: drydock.sholdee.dev/v1alpha1
plugins:
  renderer:
    postRenderers:
      - command: ["post-render", "normalize"]
        timeout: 2s
      - timeout: 3s
        command: ["post-render", "filter"]
    generate:
      command: ["argocd-vault-plugin", "generate", "."]
    engine: exec
`))
	if err != nil {
		t.Fatalf("Parse(right) error = %v", err)
	}
	leftFingerprint, err := Fingerprint(left)
	if err != nil {
		t.Fatalf("Fingerprint(left) error = %v", err)
	}
	rightFingerprint, err := Fingerprint(right)
	if err != nil {
		t.Fatalf("Fingerprint(right) error = %v", err)
	}
	if leftFingerprint != rightFingerprint {
		t.Fatalf("fingerprints differ:\nleft:  %s\nright: %s", leftFingerprint, rightFingerprint)
	}
}

func TestFingerprintIncludesExecFields(t *testing.T) {
	left, err := Parse("policy.yaml", []byte(`apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
plugins:
  renderer:
    engine: exec
    generate:
      command: ["argocd-vault-plugin", "generate", "."]
      timeout: 3s
`))
	if err != nil {
		t.Fatalf("Parse(left) error = %v", err)
	}
	right, err := Parse("policy.yaml", []byte(`apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
plugins:
  renderer:
    engine: exec
    generate:
      command: ["argocd-vault-plugin", "generate", "."]
      timeout: 4s
`))
	if err != nil {
		t.Fatalf("Parse(right) error = %v", err)
	}
	leftFingerprint, err := Fingerprint(left)
	if err != nil {
		t.Fatalf("Fingerprint(left) error = %v", err)
	}
	rightFingerprint, err := Fingerprint(right)
	if err != nil {
		t.Fatalf("Fingerprint(right) error = %v", err)
	}
	if leftFingerprint == rightFingerprint {
		t.Fatalf("fingerprints match for different exec timeouts: %s", leftFingerprint)
	}
}

func TestFingerprintChangesForExecPostRenderers(t *testing.T) {
	base := `apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
plugins:
  renderer:
    engine: exec
    generate:
      command: ["argocd-vault-plugin", "generate", "."]
    postRenderers:
      - command: ["post-render", "--mode", "default"]
        timeout: 5s
`
	baseFingerprint := mustPolicyFingerprint(t, base)
	for _, tt := range []struct {
		name string
		data string
	}{
		{
			name: "command argv",
			data: strings.Replace(base, `"default"`, `"changed"`, 1),
		},
		{
			name: "timeout",
			data: strings.Replace(base, `timeout: 5s`, `timeout: 6s`, 1),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fingerprint := mustPolicyFingerprint(t, tt.data)
			if fingerprint == baseFingerprint {
				t.Fatalf("fingerprint did not change for %s: %s", tt.name, fingerprint)
			}
		})
	}
}

func TestFingerprintChangesForExecFields(t *testing.T) {
	base := `apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
plugins:
  renderer:
    engine: exec
    init:
      command: ["argocd-vault-plugin", "version"]
      timeout: 2s
    generate:
      command: ["argocd-vault-plugin", "generate", "."]
      timeout: 3s
    env:
      allow: ["AVP_TYPE"]
    output:
      maxStdoutBytes: 1024
      maxStderrBytes: 128
`
	baseFingerprint := mustPolicyFingerprint(t, base)
	for _, tt := range []struct {
		name string
		data string
	}{
		{
			name: "init argv",
			data: strings.Replace(base, `"version"`, `"--version"`, 1),
		},
		{
			name: "generate argv",
			data: strings.Replace(base, `"generate"`, `"render"`, 1),
		},
		{
			name: "env allowlist",
			data: strings.Replace(base, `allow: ["AVP_TYPE"]`, `allow: ["AVP_TYPE", "OP_CONNECT_HOST"]`, 1),
		},
		{
			name: "output limits",
			data: strings.Replace(base, `maxStdoutBytes: 1024`, `maxStdoutBytes: 2048`, 1),
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fingerprint := mustPolicyFingerprint(t, tt.data)
			if fingerprint == baseFingerprint {
				t.Fatalf("fingerprint did not change for %s: %s", tt.name, fingerprint)
			}
		})
	}
}

func TestParseRejectsInvalidPolicyShape(t *testing.T) {
	validHeader := `apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
`
	for _, tt := range []struct {
		name string
		data string
		want string
	}{
		{
			name: "empty file",
			data: "",
			want: "empty",
		},
		{
			name: "empty document",
			data: "---\n",
			want: "empty",
		},
		{
			name: "multiple documents",
			data: validHeader + "---\n" + validHeader,
			want: "exactly one YAML document",
		},
		{
			name: "wrong apiVersion",
			data: strings.Replace(validHeader, "drydock.sholdee.dev/v1alpha1", "v1", 1),
			want: "apiVersion",
		},
		{
			name: "wrong kind",
			data: strings.Replace(validHeader, "PluginPolicy", "ConfigMap", 1),
			want: "kind",
		},
		{
			name: "metadata unknown",
			data: validHeader + "metadata: {}\n",
			want: "unknown top-level field",
		},
		{
			name: "plugins list",
			data: validHeader + "plugins: []\n",
			want: "$.plugins must be a mapping",
		},
		{
			name: "plugin entry scalar",
			data: validHeader + `plugins:
  demo: avp-compat
`,
			want: "$.plugins.demo must be a mapping",
		},
		{
			name: "plugin key is not string scalar",
			data: validHeader + `plugins:
  42:
    engine: avp-compat
`,
			want: "mapping keys must be strings",
		},
		{
			name: "empty normalized plugin key",
			data: validHeader + `plugins:
  "   ":
    engine: avp-compat
`,
			want: "plugin name must not be empty",
		},
		{
			name: "duplicate normalized plugin name",
			data: validHeader + `plugins:
  " demo ":
    engine: avp-compat
  demo:
    engine: native-kustomize
`,
			want: "duplicate plugin name",
		},
		{
			name: "missing plugin engine",
			data: validHeader + `plugins:
  demo: {}
`,
			want: "missing required field $.plugins.demo.engine",
		},
		{
			name: "invalid engine",
			data: validHeader + `plugins:
  demo:
    engine: shell
`,
			want: "unsupported engine",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse("policy.yaml", []byte(tt.data))
			if err == nil {
				t.Fatalf("Parse() succeeded, want error containing %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Parse() error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestParseRejectsCommandLikePluginFieldsAsUnknown(t *testing.T) {
	for _, field := range []string{"init", "generate", "command", "postRenderers"} {
		t.Run(field, func(t *testing.T) {
			_, err := Parse("policy.yaml", []byte(`apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
plugins:
  demo:
    engine: avp-compat
    `+field+`: {}
`))
			if err == nil {
				t.Fatalf("Parse() succeeded, want unknown field error")
			}
			if !strings.Contains(err.Error(), "unknown field") || !strings.Contains(err.Error(), field) {
				t.Fatalf("Parse() error = %v, want unknown %q field", err, field)
			}
		})
	}
}

func TestParseRejectsUnsafeYAMLFeatures(t *testing.T) {
	for _, tt := range []struct {
		name string
		data string
		want string
	}{
		{
			name: "duplicate raw key",
			data: `apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
plugins:
  demo:
    engine: avp-compat
  demo:
    engine: native-kustomize
`,
			want: "duplicate mapping key",
		},
		{
			name: "duplicate nested key",
			data: `apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
metadata:
  name: one
  name: two
`,
			want: "duplicate mapping key",
		},
		{
			name: "alias",
			data: `apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
plugins:
  base: &base
    engine: avp-compat
  demo: *base
`,
			want: "aliases are not allowed",
		},
		{
			name: "merge key",
			data: `apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
plugins:
  demo:
    <<:
      engine: avp-compat
`,
			want: "merge keys are not allowed",
		},
		{
			name: "custom scalar tag",
			data: `apiVersion: !custom drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
`,
			want: "custom YAML tag",
		},
		{
			name: "custom map tag",
			data: `apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
plugins:
  demo: !plugin
    engine: avp-compat
`,
			want: "custom YAML tag",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse("policy.yaml", []byte(tt.data))
			if err == nil {
				t.Fatalf("Parse() succeeded, want error containing %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Parse() error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestFingerprintUsesCanonicalNormalizedPolicy(t *testing.T) {
	paddedName := " zeta "
	left := Policy{Plugins: map[string]Plugin{
		paddedName: {Engine: EngineAVPCompat},
		"alpha":    {Engine: EngineNativeKustomize},
	}}
	right := Policy{Plugins: map[string]Plugin{
		"alpha": {Engine: EngineNativeKustomize},
		"zeta":  {Engine: EngineAVPCompat},
	}}

	leftFingerprint, err := Fingerprint(left)
	if err != nil {
		t.Fatalf("Fingerprint(left) error = %v", err)
	}
	rightFingerprint, err := Fingerprint(right)
	if err != nil {
		t.Fatalf("Fingerprint(right) error = %v", err)
	}
	if leftFingerprint != rightFingerprint {
		t.Fatalf("fingerprints differ:\nleft:  %s\nright: %s", leftFingerprint, rightFingerprint)
	}

	wantJSON := `{"apiVersion":"drydock.sholdee.dev/v1alpha1","kind":"PluginPolicy","plugins":{"alpha":{"engine":"native-kustomize"},"zeta":{"engine":"avp-compat"}}}`
	if want := sha256Hex(wantJSON); leftFingerprint != want {
		t.Fatalf("Fingerprint() = %s, want %s", leftFingerprint, want)
	}
}

func TestFingerprintPresentEmptyPolicyIsNonEmpty(t *testing.T) {
	policy, err := Parse("policy.yaml", []byte(`apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
plugins: {}
`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	fingerprint, err := Fingerprint(policy)
	if err != nil {
		t.Fatalf("Fingerprint() error = %v", err)
	}
	if fingerprint == NoPolicyFingerprint {
		t.Fatalf("Fingerprint() = NoPolicyFingerprint, want non-empty for present empty policy")
	}
	wantJSON := `{"apiVersion":"drydock.sholdee.dev/v1alpha1","kind":"PluginPolicy","plugins":{}}`
	if want := sha256Hex(wantJSON); fingerprint != want {
		t.Fatalf("Fingerprint() = %s, want %s", fingerprint, want)
	}
}

func TestFingerprintRejectsInvalidInputPolicy(t *testing.T) {
	paddedDemoName := " demo "
	for _, tt := range []struct {
		name   string
		policy Policy
		want   string
	}{
		{
			name: "empty normalized name",
			policy: Policy{Plugins: map[string]Plugin{
				" ": {Engine: EngineAVPCompat},
			}},
			want: "plugin name is empty",
		},
		{
			name: "duplicate normalized name",
			policy: Policy{Plugins: map[string]Plugin{
				paddedDemoName: {Engine: EngineAVPCompat},
				"demo":         {Engine: EngineNativeKustomize},
			}},
			want: "duplicate plugin name",
		},
		{
			name: "invalid engine",
			policy: Policy{Plugins: map[string]Plugin{
				"demo": {Engine: "shell"},
			}},
			want: "unsupported engine",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Fingerprint(tt.policy)
			if err == nil {
				t.Fatalf("Fingerprint() succeeded, want error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Fingerprint() error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func schemaObject(t *testing.T, object map[string]any, name string) map[string]any {
	t.Helper()
	value, ok := object[name]
	if !ok {
		t.Fatalf("schema missing object %q", name)
	}
	child, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("schema %q = %#v, want object", name, value)
	}
	return child
}

func assertSchemaConst(t *testing.T, object map[string]any, want string) {
	t.Helper()
	got, ok := object["const"].(string)
	if !ok {
		t.Fatalf("schema object = %#v, want string const", object)
	}
	if got != want {
		t.Fatalf("schema const = %q, want %q", got, want)
	}
}

func mustPolicyFingerprint(t *testing.T, data string) string {
	t.Helper()
	policy, err := Parse("policy.yaml", []byte(data))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	fingerprint, err := Fingerprint(policy)
	if err != nil {
		t.Fatalf("Fingerprint() error = %v", err)
	}
	return fingerprint
}
