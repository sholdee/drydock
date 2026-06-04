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
	defs := schemaObject(t, schema, "$defs")
	assertPolicyRootSchema(t, properties)
	assertBootstrapSchema(t, defs)
	assertNativePluginSchemas(t, defs)
	assertExecPluginSchema(t, defs)
	assertContainerPluginSchema(t, defs)
	assertCMPAndParameterSchemas(t, defs)
	assertFractionalDurationPolicy(t)
}

func assertPolicyRootSchema(t *testing.T, properties map[string]any) {
	t.Helper()
	assertSchemaConst(t, schemaObject(t, properties, "apiVersion"), apiVersion)
	assertSchemaConst(t, schemaObject(t, properties, "kind"), kind)
	assertSchemaRef(t, schemaObject(t, properties, "bootstrap"), "#/$defs/bootstrap")
}

func assertBootstrapSchema(t *testing.T, defs map[string]any) {
	t.Helper()
	pluginDef := schemaObject(t, defs, "plugin")
	assertSchemaOneOfRefs(t, pluginDef, []string{
		"#/$defs/avpCompatPlugin",
		"#/$defs/nativeKustomizePlugin",
		"#/$defs/execPlugin",
		"#/$defs/containerPlugin",
	})
	bootstrap := schemaObject(t, defs, "bootstrap")
	entrypoints := schemaObject(t, schemaObject(t, bootstrap, "properties"), "entrypoints")
	if minItems, ok := entrypoints["minItems"].(float64); !ok || minItems != 1 {
		t.Fatalf("bootstrap.entrypoints minItems = %#v, want 1", entrypoints["minItems"])
	}
	bootstrapEntrypoint := schemaObject(t, defs, "bootstrapEntrypoint")
	bootstrapEntrypointProps := schemaObject(t, bootstrapEntrypoint, "properties")
	assertSchemaRef(t, schemaObject(t, bootstrapEntrypointProps, "sourcePath"), "#/$defs/repoRelativePath")
	bootstrapParameter := schemaObject(t, defs, "bootstrapEntrypointParameter")
	if _, ok := bootstrapParameter["oneOf"].([]any); !ok {
		t.Fatalf("bootstrap parameter schema oneOf = %#v, want exactly-one value constraint", bootstrapParameter["oneOf"])
	}
}

func assertNativePluginSchemas(t *testing.T, defs map[string]any) {
	t.Helper()
	for name, engine := range map[string]Engine{
		"avpCompatPlugin":       EngineAVPCompat,
		"nativeKustomizePlugin": EngineNativeKustomize,
	} {
		def := schemaObject(t, defs, name)
		props := schemaObject(t, def, "properties")
		engineProp := schemaObject(t, props, "engine")
		assertSchemaConst(t, engineProp, string(engine))
		assertSchemaRef(t, schemaObject(t, props, "match"), "#/$defs/match")
		assertSchemaRef(t, schemaObject(t, props, "configManagementPlugin"), "#/$defs/configManagementPluginSeed")
	}
}

func assertExecPluginSchema(t *testing.T, defs map[string]any) {
	t.Helper()
	execDef := schemaObject(t, defs, "execPlugin")
	execProps := schemaObject(t, execDef, "properties")
	engineProp := schemaObject(t, execProps, "engine")
	assertSchemaConst(t, engineProp, string(EngineExec))
	assertSchemaRef(t, schemaObject(t, execProps, "match"), "#/$defs/match")
	assertSchemaRef(t, schemaObject(t, execProps, "configManagementPlugin"), "#/$defs/configManagementPluginSeed")
	parametersProp := schemaObject(t, execProps, "parameters")
	if ref, ok := parametersProp["$ref"].(string); !ok || ref != "#/$defs/parameters" {
		t.Fatalf("exec parameters schema ref = %#v, want #/$defs/parameters", parametersProp["$ref"])
	}
}

func assertContainerPluginSchema(t *testing.T, defs map[string]any) {
	t.Helper()
	containerDef := schemaObject(t, defs, "containerPlugin")
	containerProps := schemaObject(t, containerDef, "properties")
	assertSchemaRequired(t, containerDef, "engine", "image", "generate")
	assertSchemaConst(t, schemaObject(t, containerProps, "engine"), string(EngineContainer))
	assertSchemaRef(t, schemaObject(t, containerProps, "match"), "#/$defs/match")
	assertSchemaRef(t, schemaObject(t, containerProps, "configManagementPlugin"), "#/$defs/configManagementPluginSeed")
	assertSchemaEnum(t, schemaObject(t, containerProps, "runtime"), "docker")
	assertSchemaDefault(t, schemaObject(t, containerProps, "runtime"), "docker")
	if got, ok := schemaObject(t, containerProps, "image")["type"].(string); !ok || got != "string" {
		t.Fatalf("container image schema type = %#v, want string", schemaObject(t, containerProps, "image")["type"])
	}
	if got, ok := schemaObject(t, containerProps, "allowMutableImageTag")["type"].(string); !ok || got != "boolean" {
		t.Fatalf("container allowMutableImageTag schema type = %#v, want boolean", schemaObject(t, containerProps, "allowMutableImageTag")["type"])
	}
	assertSchemaDefault(t, schemaObject(t, containerProps, "allowMutableImageTag"), false)
	assertSchemaEnum(t, schemaObject(t, containerProps, "network"), "none", "default")
	assertSchemaDefault(t, schemaObject(t, containerProps, "network"), "none")
	assertSchemaRef(t, schemaObject(t, containerProps, "cacheMounts"), "#/$defs/containerCacheMounts")
	assertSchemaRef(t, schemaObject(t, containerProps, "generate"), "#/$defs/command")
	assertSchemaRef(t, schemaObject(t, containerProps, "parameters"), "#/$defs/parameters")
	cacheMounts := schemaObject(t, defs, "containerCacheMounts")
	if got, ok := cacheMounts["maxItems"].(float64); !ok || int(got) != maxContainerCacheMountCount {
		t.Fatalf("container cacheMounts maxItems = %#v, want %d", cacheMounts["maxItems"], maxContainerCacheMountCount)
	}
	cacheMount := schemaObject(t, defs, "containerCacheMount")
	assertSchemaRequired(t, cacheMount, "name", "target")
	cacheMountProps := schemaObject(t, cacheMount, "properties")
	assertSchemaRef(t, schemaObject(t, cacheMountProps, "name"), "#/$defs/containerCacheMountName")
	cacheMountName := schemaObject(t, defs, "containerCacheMountName")
	if got, ok := cacheMountName["maxLength"].(float64); !ok || int(got) != 63 {
		t.Fatalf("container cache mount name maxLength = %#v, want 63", cacheMountName["maxLength"])
	}
}

func assertCMPAndParameterSchemas(t *testing.T, defs map[string]any) {
	t.Helper()
	cmpSeed := schemaObject(t, defs, "configManagementPluginSeed")
	cmpSeedProps := schemaObject(t, cmpSeed, "properties")
	assertSchemaRef(t, schemaObject(t, cmpSeedProps, "discover"), "#/$defs/discoverMatch")
	assertSchemaRef(t, schemaObject(t, cmpSeedProps, "generate"), "#/$defs/configManagementPluginGenerateSeed")
	matchDiscover := schemaObject(t, schemaObject(t, schemaObject(t, defs, "match"), "properties"), "discover")
	assertSchemaRef(t, matchDiscover, "#/$defs/discoverMatch")
	discoverMatch := schemaObject(t, defs, "discoverMatch")
	if _, ok := discoverMatch["oneOf"].([]any); !ok {
		t.Fatalf("discoverMatch schema oneOf = %#v, want exactly-one rule constraint", discoverMatch["oneOf"])
	}
	findMatch := schemaObject(t, defs, "discoverFindMatch")
	if got, ok := findMatch["additionalProperties"].(bool); !ok || got {
		t.Fatalf("discoverFindMatch additionalProperties = %#v, want false", findMatch["additionalProperties"])
	}
	parameterType := schemaObject(t, schemaObject(t, schemaObject(t, defs, "parameter"), "properties"), "type")
	enum, ok := parameterType["enum"].([]any)
	if !ok || len(enum) != 3 {
		t.Fatalf("parameter type enum = %#v, want string/array/map", parameterType["enum"])
	}

	timeout := schemaObject(t, schemaObject(t, schemaObject(t, defs, "command"), "properties"), "timeout")
	if _, ok := timeout["pattern"]; ok {
		t.Fatalf("schema timeout has pattern %#v, want parser-authoritative Go duration string", timeout["pattern"])
	}
}

func assertFractionalDurationPolicy(t *testing.T) {
	t.Helper()
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

func TestParseConfigManagementPluginSeed(t *testing.T) {
	policy, err := Parse("policy.yaml", []byte(`apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
plugins:
  cmp:
    engine: native-kustomize
    configManagementPlugin:
      discover:
        find:
          glob: "apps/**/kustomization.yaml"
      generate:
        command: ["kustomize", "build"]
        args: ["--enable-helm", "."]
`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	plugin, ok := policy.Plugin("cmp")
	if !ok || plugin.ConfigManagementPlugin == nil {
		t.Fatalf("Plugin(cmp) = %#v, want configManagementPlugin seed", plugin)
	}
	seed := plugin.ConfigManagementPlugin
	if seed.Discover == nil || seed.Discover.FindGlob != "apps/**/kustomization.yaml" {
		t.Fatalf("seed discover = %#v, want find glob", seed.Discover)
	}
	if seed.Generate == nil {
		t.Fatal("seed generate = nil, want parsed generate")
	}
	if got := strings.Join(seed.Generate.Command, " "); got != "kustomize build" {
		t.Fatalf("seed generate command = %q, want kustomize build", got)
	}
	if got := strings.Join(seed.Generate.Args, " "); got != "--enable-helm ." {
		t.Fatalf("seed generate args = %q, want --enable-helm .", got)
	}
}

func TestParseBootstrapEntrypoints(t *testing.T) {
	policy, err := Parse("policy.yaml", []byte(`apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
bootstrap:
  entrypoints:
    - name: cluster-root
      plugin: pkl
      sourcePath: .
      parameters:
        - name: path
          string: index.pkl
        - name: packages
          array: ["packages/base.pkl", "packages/extra.pkl"]
        - name: labels
          map:
            cluster: home
            environment: prod
plugins:
  pkl:
    engine: exec
    configManagementPlugin:
      discover:
        fileName: PklProject
    generate:
      command: ["pkl", "eval", "{{param:path}}"]
`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if len(policy.Bootstrap.Entrypoints) != 1 {
		t.Fatalf("len(Bootstrap.Entrypoints) = %d, want 1", len(policy.Bootstrap.Entrypoints))
	}
	entrypoint := policy.Bootstrap.Entrypoints[0]
	if entrypoint.Name != "cluster-root" || entrypoint.Plugin != "pkl" || entrypoint.SourcePath != "." {
		t.Fatalf("Bootstrap.Entrypoints[0] = %#v, want cluster-root pkl .", entrypoint)
	}
	params := bootstrapParametersByName(entrypoint.Parameters)
	if param := params["path"]; param.String == nil || *param.String != "index.pkl" {
		t.Fatalf("path parameter = %#v, want string index.pkl", param)
	}
	if param := params["packages"]; param.Array == nil || strings.Join(param.Array.Values, ",") != "packages/base.pkl,packages/extra.pkl" {
		t.Fatalf("packages parameter = %#v, want array values", param)
	}
	if param := params["labels"]; param.Map == nil || param.Map.Values["cluster"] != "home" || param.Map.Values["environment"] != "prod" {
		t.Fatalf("labels parameter = %#v, want map values", param)
	}
}

func TestParseBootstrapRejectsInvalidEntrypoints(t *testing.T) {
	base := `apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
bootstrap:
  entrypoints:
    - name: cluster-root
      plugin: pkl
      sourcePath: personal-cluster
plugins:
  pkl:
    engine: exec
    configManagementPlugin:
      discover:
        fileName: PklProject
    generate:
      command: ["pkl", "eval", "index.pkl"]
`
	for _, tt := range []struct {
		name string
		data string
		want string
	}{
		{
			name: "missing entrypoints",
			data: strings.Replace(base, "bootstrap:\n  entrypoints:\n    - name: cluster-root\n      plugin: pkl\n      sourcePath: personal-cluster\n", "bootstrap: {}\n", 1),
			want: "missing required field $.bootstrap.entrypoints",
		},
		{
			name: "duplicate name",
			data: strings.Replace(base, "plugins:", "    - name: cluster-root\n      plugin: pkl\n      sourcePath: personal-cluster\nplugins:", 1),
			want: "duplicate name",
		},
		{
			name: "invalid name",
			data: strings.Replace(base, "name: cluster-root", "name: Cluster_Root", 1),
			want: "invalid",
		},
		{
			name: "unknown plugin",
			data: strings.Replace(base, "plugin: pkl", "plugin: cue", 1),
			want: "unknown plugin",
		},
		{
			name: "plugin without discover",
			data: strings.Replace(base, "    configManagementPlugin:\n      discover:\n        fileName: PklProject\n", "", 1),
			want: "must define match.discover or configManagementPlugin.discover",
		},
		{
			name: "absolute source path",
			data: strings.Replace(base, "sourcePath: personal-cluster", "sourcePath: /personal-cluster", 1),
			want: "absolute paths",
		},
		{
			name: "parent source path",
			data: strings.Replace(base, "sourcePath: personal-cluster", "sourcePath: ../personal-cluster", 1),
			want: "parent directory segments",
		},
		{
			name: "git source path",
			data: strings.Replace(base, "sourcePath: personal-cluster", "sourcePath: .git", 1),
			want: ".git paths",
		},
		{
			name: "dot component source path",
			data: strings.Replace(base, "sourcePath: personal-cluster", "sourcePath: personal-cluster/.", 1),
			want: "dot path components",
		},
		{
			name: "repeated slash source path",
			data: strings.Replace(base, "sourcePath: personal-cluster", "sourcePath: personal-cluster//apps", 1),
			want: "empty path components",
		},
		{
			name: "parameter duplicate",
			data: strings.Replace(base, "sourcePath: personal-cluster", "sourcePath: personal-cluster\n      parameters:\n        - name: path\n          string: one\n        - name: path\n          string: two", 1),
			want: "duplicate parameter",
		},
		{
			name: "parameter no value",
			data: strings.Replace(base, "sourcePath: personal-cluster", "sourcePath: personal-cluster\n      parameters:\n        - name: path", 1),
			want: "exactly one",
		},
		{
			name: "parameter two values",
			data: strings.Replace(base, "sourcePath: personal-cluster", "sourcePath: personal-cluster\n      parameters:\n        - name: path\n          string: index.pkl\n          array: [index.pkl]", 1),
			want: "exactly one",
		},
		{
			name: "parameter map non-string value",
			data: strings.Replace(base, "sourcePath: personal-cluster", "sourcePath: personal-cluster\n      parameters:\n        - name: labels\n          map:\n            cluster: 1", 1),
			want: "must be a string",
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

func TestParseConfigManagementPluginSeedAllowsOmittedSeedGenerateForExec(t *testing.T) {
	policy, err := Parse("policy.yaml", []byte(`apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
plugins:
  cmp:
    engine: exec
    configManagementPlugin:
      discover:
        fileName: plugin.yaml
    generate:
      command: ["renderer"]
`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	plugin, ok := policy.Plugin("cmp")
	if !ok || plugin.ConfigManagementPlugin == nil {
		t.Fatalf("Plugin(cmp) = %#v, want configManagementPlugin seed", plugin)
	}
	if plugin.Exec == nil {
		t.Fatalf("Exec = nil, want exec config")
	}
	if plugin.ConfigManagementPlugin.Generate != nil {
		t.Fatalf("seed generate = %#v, want nil", plugin.ConfigManagementPlugin.Generate)
	}
}

func TestParseConfigManagementPluginSeedRejectsUnsupportedFields(t *testing.T) {
	for _, tt := range []struct {
		name string
		seed string
		want string
	}{
		{
			name: "metadata",
			seed: `    configManagementPlugin:
      metadata:
        name: cmp
`,
			want: "unknown field",
		},
		{
			name: "version",
			seed: `    configManagementPlugin:
      version: v1
`,
			want: "unknown field",
		},
		{
			name: "find command",
			seed: `    configManagementPlugin:
      discover:
        find:
          command: ["find", "."]
`,
			want: "unknown field",
		},
		{
			name: "env",
			seed: `    configManagementPlugin:
      generate:
        command: ["renderer"]
        env: []
`,
			want: "unknown field",
		},
		{
			name: "lockRepo",
			seed: `    configManagementPlugin:
      lockRepo: true
`,
			want: "unknown field",
		},
		{
			name: "preserveFileMode",
			seed: `    configManagementPlugin:
      preserveFileMode: true
`,
			want: "unknown field",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse("policy.yaml", []byte(`apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
plugins:
  cmp:
    engine: native-kustomize
`+tt.seed))
			if err == nil {
				t.Fatalf("Parse() succeeded, want error containing %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Parse() error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestParseConfigManagementPluginSeedRejectsUnsafePatterns(t *testing.T) {
	for _, tt := range []struct {
		name string
		seed string
		want string
	}{
		{
			name: "fileName absolute",
			seed: `    configManagementPlugin:
      discover:
        fileName: /plugin.yaml
`,
			want: "absolute paths",
		},
		{
			name: "fileName parent",
			seed: `    configManagementPlugin:
      discover:
        fileName: ../plugin.yaml
`,
			want: "parent directory segments",
		},
		{
			name: "fileName git",
			seed: `    configManagementPlugin:
      discover:
        fileName: .git/config
`,
			want: ".git paths",
		},
		{
			name: "find glob backslash",
			seed: `    configManagementPlugin:
      discover:
        find:
          glob: "apps\\**"
`,
			want: "backslashes",
		},
		{
			name: "find glob bad syntax",
			seed: `    configManagementPlugin:
      discover:
        find:
          glob: "apps/["
`,
			want: "valid doublestar glob",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse("policy.yaml", []byte(`apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
plugins:
  cmp:
    engine: native-kustomize
`+tt.seed))
			if err == nil {
				t.Fatalf("Parse() succeeded, want error containing %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Parse() error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestParseConfigManagementPluginSeedRejectsEmptyArgvToken(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
	}{
		{
			name: "command token",
			body: `        command: ["renderer", ""]
`,
		},
		{
			name: "args token",
			body: `        args: ["--flag", " "]
`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse("policy.yaml", []byte(`apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
plugins:
  cmp:
    engine: native-kustomize
    configManagementPlugin:
      generate:
`+tt.body))
			if err == nil {
				t.Fatal("Parse() succeeded, want empty argv token error")
			}
			if !strings.Contains(err.Error(), "must not be empty") {
				t.Fatalf("Parse() error = %v, want empty token error", err)
			}
		})
	}
}

func TestParseConfigManagementPluginSeedDiscoverMustMatchPolicyMatch(t *testing.T) {
	for _, tt := range []struct {
		name string
		body string
	}{
		{
			name: "rule type mismatch",
			body: `    match:
      discover:
        fileName: plugin.yaml
    configManagementPlugin:
      discover:
        find:
          glob: plugin.yaml
`,
		},
		{
			name: "pattern mismatch",
			body: `    match:
      discover:
        fileName: plugin.yaml
    configManagementPlugin:
      discover:
        fileName: other.yaml
`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse("policy.yaml", []byte(`apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
plugins:
  cmp:
    engine: native-kustomize
`+tt.body))
			if err == nil {
				t.Fatal("Parse() succeeded, want match-vs-seed mismatch error")
			}
			if !strings.Contains(err.Error(), "configManagementPlugin.discover must match") {
				t.Fatalf("Parse() error = %v, want discover mismatch error", err)
			}
		})
	}
}

func TestParseConfigManagementPluginSeedAllowsIdenticalNormalizedPolicyMatch(t *testing.T) {
	policy, err := Parse("policy.yaml", []byte(`apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
plugins:
  cmp:
    engine: avp-compat
    match:
      discover:
        fileName: " plugin.yaml "
    configManagementPlugin:
      discover:
        fileName: plugin.yaml
`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	plugin := policy.Plugins["cmp"]
	if plugin.Match == nil || plugin.ConfigManagementPlugin == nil || plugin.ConfigManagementPlugin.Discover == nil {
		t.Fatalf("Plugin(cmp) = %#v, want match and seed discover", plugin)
	}
	if plugin.Match.Discover.FileName != "plugin.yaml" || plugin.ConfigManagementPlugin.Discover.FileName != "plugin.yaml" {
		t.Fatalf("normalized discover = match %#v seed %#v, want plugin.yaml", plugin.Match.Discover, plugin.ConfigManagementPlugin.Discover)
	}
}

func TestParsePluginMatchDiscoverRules(t *testing.T) {
	policy, err := Parse("policy.yaml", []byte(`apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
plugins:
  avp:
    engine: avp-compat
    match:
      discover:
        fileName: "config/*.yaml"
  native:
    engine: native-kustomize
    match:
      discover:
        find:
          glob: "**/kustomization.yaml"
  exec:
    engine: exec
    match:
      discover:
        fileName: "plugin.yaml"
    generate:
      command: ["renderer"]
`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}

	avp, ok := policy.Plugin("avp")
	if !ok || avp.Match == nil {
		t.Fatalf("Plugin(avp) = %#v, want match", avp)
	}
	if avp.Match.Discover.FileName != "config/*.yaml" || avp.Match.Discover.FindGlob != "" {
		t.Fatalf("avp match = %#v, want fileName rule", avp.Match)
	}
	native, ok := policy.Plugin("native")
	if !ok || native.Match == nil {
		t.Fatalf("Plugin(native) = %#v, want match", native)
	}
	if native.Match.Discover.FindGlob != "**/kustomization.yaml" || native.Match.Discover.FileName != "" {
		t.Fatalf("native match = %#v, want find.glob rule", native.Match)
	}
	exec, ok := policy.Plugin("exec")
	if !ok || exec.Match == nil || exec.Exec == nil {
		t.Fatalf("Plugin(exec) = %#v, want exec plugin with match", exec)
	}
	if exec.Match.Discover.FileName != "plugin.yaml" {
		t.Fatalf("exec fileName = %q, want plugin.yaml", exec.Match.Discover.FileName)
	}
}

func TestParsePluginMatchRejectsUnsafeRules(t *testing.T) {
	for _, tt := range []struct {
		name  string
		match string
		want  string
	}{
		{
			name: "empty match",
			match: `    match: {}
`,
			want: "must contain exactly one static",
		},
		{
			name: "null match",
			match: `    match: null
`,
			want: "must contain exactly one static",
		},
		{
			name: "empty discover",
			match: `    match:
      discover: {}
`,
			want: "must contain exactly one static",
		},
		{
			name: "both fileName and find glob",
			match: `    match:
      discover:
        fileName: plugin.yaml
        find:
          glob: "**/plugin.yaml"
`,
			want: "exactly one static rule",
		},
		{
			name: "null fileName",
			match: `    match:
      discover:
        fileName: null
`,
			want: "fileName must not be null",
		},
		{
			name: "null find",
			match: `    match:
      discover:
        find: null
`,
			want: "find must not be null",
		},
		{
			name: "unknown match field",
			match: `    match:
      name: plugin
      discover:
        fileName: plugin.yaml
`,
			want: "unknown field",
		},
		{
			name: "unknown discover field",
			match: `    match:
      discover:
        name: plugin
`,
			want: "unknown field",
		},
		{
			name: "find command",
			match: `    match:
      discover:
        find:
          command: ["find", "."]
`,
			want: "unknown field",
		},
		{
			name: "missing find glob",
			match: `    match:
      discover:
        find: {}
`,
			want: "missing required field $.plugins.demo.match.discover.find.glob",
		},
		{
			name: "empty fileName",
			match: `    match:
      discover:
        fileName: " "
`,
			want: "must not be empty",
		},
		{
			name: "fileName backslash",
			match: `    match:
      discover:
        fileName: "config\\*.yaml"
`,
			want: "backslashes",
		},
		{
			name: "fileName absolute",
			match: `    match:
      discover:
        fileName: "/plugin.yaml"
`,
			want: "absolute paths",
		},
		{
			name: "fileName parent segment",
			match: `    match:
      discover:
        fileName: "../plugin.yaml"
`,
			want: "parent directory segments",
		},
		{
			name: "fileName git path",
			match: `    match:
      discover:
        fileName: ".git/config"
`,
			want: ".git paths",
		},
		{
			name: "fileName bad glob",
			match: `    match:
      discover:
        fileName: "apps/["
`,
			want: "valid filepath glob",
		},
		{
			name: "find glob backslash",
			match: `    match:
      discover:
        find:
          glob: "apps\\**"
`,
			want: "backslashes",
		},
		{
			name: "find glob absolute",
			match: `    match:
      discover:
        find:
          glob: "/apps/**"
`,
			want: "absolute paths",
		},
		{
			name: "find glob parent segment",
			match: `    match:
      discover:
        find:
          glob: "apps/../plugin.yaml"
`,
			want: "parent directory segments",
		},
		{
			name: "find glob git path",
			match: `    match:
      discover:
        find:
          glob: "apps/.git/**"
`,
			want: ".git paths",
		},
		{
			name: "find glob bad syntax",
			match: `    match:
      discover:
        find:
          glob: "apps/["
`,
			want: "valid doublestar glob",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse("policy.yaml", []byte(`apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
plugins:
  demo:
    engine: avp-compat
`+tt.match))
			if err == nil {
				t.Fatalf("Parse() succeeded, want error containing %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Parse() error = %v, want substring %q", err, tt.want)
			}
		})
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
    copy:
      scope: repository
      include: ["shared/**", "packages/*.pkl"]
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
	if plugin.Exec.Copy.Scope != ExecCopyScopeRepository {
		t.Fatalf("Copy.Scope = %q, want repository", plugin.Exec.Copy.Scope)
	}
	if got := strings.Join(plugin.Exec.Copy.Include, ","); got != "packages/*.pkl,shared/**" {
		t.Fatalf("Copy.Include = %q, want sorted include globs", got)
	}
	if plugin.Exec.Output.MaxStdoutBytes != 1024 || plugin.Exec.Output.MaxStderrBytes != 128 {
		t.Fatalf("Output = %#v, want explicit limits", plugin.Exec.Output)
	}
}

func TestParseExecPolicyParameters(t *testing.T) {
	policy, err := Parse("policy.yaml", []byte(`apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
plugins:
  pkl:
    engine: exec
    generate:
      command: ["pkl", "eval", "{{param:path}}"]
    parameters:
      allow:
        - name: path
          type: string
          required: true
          path:
            base: source
            allow: ["*.pkl", "components/*.pkl"]
        - name: pkl_modules
          type: array
`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	plugin, ok := policy.Plugin("pkl")
	if !ok || plugin.Exec == nil {
		t.Fatalf("Plugin(pkl) = %#v, want exec plugin", plugin)
	}
	if got := len(plugin.Exec.Parameters.Allow); got != 2 {
		t.Fatalf("len(Parameters.Allow) = %d, want 2", got)
	}
	pathParam := plugin.Exec.Parameters.Allow[0]
	if pathParam.Name != "path" || pathParam.Type != ExecParameterTypeString || !pathParam.Required || pathParam.Path == nil {
		t.Fatalf("path parameter = %#v, want required string path parameter", pathParam)
	}
	if got := strings.Join(pathParam.Path.Allow, ","); got != "*.pkl,components/*.pkl" {
		t.Fatalf("path allow = %q, want sorted allow globs", got)
	}
}

func TestParseExecPolicyRepositoryCopyParameter(t *testing.T) {
	policy, err := Parse("policy.yaml", []byte(`apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
plugins:
  pkl:
    engine: exec
    copy:
      scope: repository
      include: ["shared/**"]
    generate:
      command: ["pkl", "eval", "{{param:path}}"]
    parameters:
      allow:
        - name: path
          type: string
          required: true
          path:
            base: repository
            allow: ["shared/*.pkl"]
`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	plugin, ok := policy.Plugin("pkl")
	if !ok || plugin.Exec == nil {
		t.Fatalf("Plugin(pkl) = %#v, want exec plugin", plugin)
	}
	if plugin.Exec.Copy.Scope != ExecCopyScopeRepository || strings.Join(plugin.Exec.Copy.Include, ",") != "shared/**" {
		t.Fatalf("Copy = %#v, want repository scope with shared include", plugin.Exec.Copy)
	}
	pathParam := plugin.Exec.Parameters.Allow[0]
	if pathParam.Path == nil || pathParam.Path.Base != ExecParameterPathBaseRepository {
		t.Fatalf("path parameter = %#v, want repository path base", pathParam)
	}
}

func TestParseContainerPolicyDigestImageAppliesDefaults(t *testing.T) {
	image := "registry.example.com/drydock/renderer@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	policy, err := Parse("policy.yaml", []byte(`apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
plugins:
  renderer:
    engine: container
    image: `+image+`
    generate:
      command: ["renderer", "generate"]
`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	plugin, ok := policy.Plugin("renderer")
	if !ok {
		t.Fatal("Plugin(renderer) missing")
	}
	if plugin.Engine != EngineContainer {
		t.Fatalf("Engine = %q, want %q", plugin.Engine, EngineContainer)
	}
	if plugin.Exec != nil {
		t.Fatalf("Exec = %#v, want nil for container plugin", plugin.Exec)
	}
	if plugin.Container == nil {
		t.Fatal("Container = nil, want parsed container config")
	}
	if got := plugin.Container.Runtime; got != DefaultContainerRuntime {
		t.Fatalf("Runtime = %q, want %q", got, DefaultContainerRuntime)
	}
	if got := plugin.Container.Network; got != DefaultContainerNetwork {
		t.Fatalf("Network = %q, want %q", got, DefaultContainerNetwork)
	}
	if len(plugin.Container.CacheMounts) != 0 {
		t.Fatalf("CacheMounts = %#v, want none by default", plugin.Container.CacheMounts)
	}
	if plugin.Container.AllowMutableImageTag {
		t.Fatal("AllowMutableImageTag = true, want default false")
	}
	if plugin.Container.Image != image {
		t.Fatalf("Image = %q, want %q", plugin.Container.Image, image)
	}
	if got := plugin.Container.Lifecycle.Generate.Timeout; got != DefaultGenerateTimeout {
		t.Fatalf("Generate.Timeout = %s, want %s", got, DefaultGenerateTimeout)
	}
	if got := plugin.Container.Lifecycle.Workdir; got != ExecWorkdirSource {
		t.Fatalf("Lifecycle.Workdir = %q, want %q", got, ExecWorkdirSource)
	}
	if got := plugin.Container.Lifecycle.Output.MaxStdoutBytes; got != DefaultMaxStdoutBytes {
		t.Fatalf("MaxStdoutBytes = %d, want %d", got, DefaultMaxStdoutBytes)
	}
}

func TestParseContainerPolicyMutableTagOptIn(t *testing.T) {
	image := "ghcr.io/sholdee/drydock-renderer:v1.2.3"
	policy, err := Parse("policy.yaml", []byte(`apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
plugins:
  renderer:
    engine: container
    image: `+image+`
    allowMutableImageTag: true
    network: default
    generate:
      command: ["renderer", "generate"]
`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	plugin, ok := policy.Plugin("renderer")
	if !ok || plugin.Container == nil {
		t.Fatalf("Plugin(renderer) = %#v, want container plugin", plugin)
	}
	if !plugin.Container.AllowMutableImageTag {
		t.Fatal("AllowMutableImageTag = false, want true")
	}
	if plugin.Container.Image != image {
		t.Fatalf("Image = %q, want %q", plugin.Container.Image, image)
	}
	if plugin.Container.Network != ContainerNetworkDefault {
		t.Fatalf("Network = %q, want %q", plugin.Container.Network, ContainerNetworkDefault)
	}
}

func TestParseContainerPolicyCacheMounts(t *testing.T) {
	image := "registry.example.com/drydock/renderer@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	policy, err := Parse("policy.yaml", []byte(`apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
plugins:
  renderer:
    engine: container
    image: `+image+`
    cacheMounts:
      - name: z-cache
        target: /drydock-cache/z-cache
      - name: pkl-cache
        target: /drydock-cache/pkl-cache//module
    generate:
      command: ["renderer", "generate"]
`))
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	plugin, ok := policy.Plugin("renderer")
	if !ok || plugin.Container == nil {
		t.Fatalf("Plugin(renderer) = %#v, want container plugin", plugin)
	}
	want := []ContainerCacheMount{
		{Name: "pkl-cache", Target: "/drydock-cache/pkl-cache/module"},
		{Name: "z-cache", Target: "/drydock-cache/z-cache"},
	}
	if len(plugin.Container.CacheMounts) != len(want) {
		t.Fatalf("CacheMounts = %#v, want %#v", plugin.Container.CacheMounts, want)
	}
	for index, mount := range want {
		if plugin.Container.CacheMounts[index] != mount {
			t.Fatalf("CacheMounts[%d] = %#v, want %#v", index, plugin.Container.CacheMounts[index], mount)
		}
	}
}

func TestParseContainerPolicyRejectsUnsafeFields(t *testing.T) {
	digestImage := "registry.example.com/drydock/renderer@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	for _, tt := range []struct {
		name string
		body string
		want string
	}{
		{
			name: "missing image",
			body: `    engine: container
    generate:
      command: ["renderer"]
`,
			want: "missing required field $.plugins.renderer.image",
		},
		{
			name: "image must be string",
			body: `    engine: container
    image: 1
    generate:
      command: ["renderer"]
`,
			want: "image must be a string",
		},
		{
			name: "mutable tag without opt-in",
			body: `    engine: container
    image: registry.example.com/drydock/renderer:v1.2.3
    generate:
      command: ["renderer"]
`,
			want: "digest is required unless allowMutableImageTag is true",
		},
		{
			name: "unqualified image",
			body: `    engine: container
    image: renderer:v1.2.3
    allowMutableImageTag: true
    generate:
      command: ["renderer"]
`,
			want: "fully qualified with a registry host",
		},
		{
			name: "invalid image reference",
			body: `    engine: container
    image: registry.example.com/drydock/renderer@sha256:not-a-digest
    generate:
      command: ["renderer"]
`,
			want: "image \"registry.example.com/drydock/renderer@sha256:not-a-digest\" is invalid",
		},
		{
			name: "unsupported runtime",
			body: `    engine: container
    runtime: podman
    image: ` + digestImage + `
    generate:
      command: ["renderer"]
`,
			want: "unsupported container runtime",
		},
		{
			name: "runtime must be string",
			body: `    engine: container
    runtime: 7
    image: ` + digestImage + `
    generate:
      command: ["renderer"]
`,
			want: "runtime must be a string",
		},
		{
			name: "unsupported network",
			body: `    engine: container
    image: ` + digestImage + `
    network: host
    generate:
      command: ["renderer"]
`,
			want: "unsupported container network",
		},
		{
			name: "network must be string",
			body: `    engine: container
    image: ` + digestImage + `
    network: true
    generate:
      command: ["renderer"]
`,
			want: "network must be a string",
		},
		{
			name: "allow mutable must be boolean",
			body: `    engine: container
    image: ` + digestImage + `
    allowMutableImageTag: "true"
    generate:
      command: ["renderer"]
`,
			want: "allowMutableImageTag must be a boolean",
		},
		{
			name: "unknown field",
			body: `    engine: container
    image: ` + digestImage + `
    random: true
    generate:
      command: ["renderer"]
`,
			want: "unknown field",
		},
		{
			name: "cache mounts must be sequence",
			body: `    engine: container
    image: ` + digestImage + `
    cacheMounts: {}
    generate:
      command: ["renderer"]
`,
			want: "cacheMounts must be a sequence",
		},
		{
			name: "cache mounts must not be empty",
			body: `    engine: container
    image: ` + digestImage + `
    cacheMounts: []
    generate:
      command: ["renderer"]
`,
			want: "cacheMounts must not be empty",
		},
		{
			name: "cache mount item must be mapping",
			body: `    engine: container
    image: ` + digestImage + `
    cacheMounts:
      - pkl-cache
    generate:
      command: ["renderer"]
`,
			want: "cacheMounts[0] must be a mapping",
		},
		{
			name: "cache mount name must be path safe",
			body: `    engine: container
    image: ` + digestImage + `
    cacheMounts:
      - name: Pkl_Cache
        target: /drydock-cache/pkl-cache
    generate:
      command: ["renderer"]
`,
			want: "DNS-label-like cache name",
		},
		{
			name: "cache mount name rejects whitespace",
			body: `    engine: container
    image: ` + digestImage + `
    cacheMounts:
      - name: " pkl-cache"
        target: /drydock-cache/pkl-cache
    generate:
      command: ["renderer"]
`,
			want: "leading or trailing whitespace",
		},
		{
			name: "cache mount duplicate name",
			body: `    engine: container
    image: ` + digestImage + `
    cacheMounts:
      - name: pkl-cache
        target: /drydock-cache/pkl-cache
      - name: pkl-cache
        target: /drydock-cache/pkl-cache-2
    generate:
      command: ["renderer"]
`,
			want: "duplicate cache mount name",
		},
		{
			name: "cache mount target must be absolute",
			body: `    engine: container
    image: ` + digestImage + `
    cacheMounts:
      - name: pkl-cache
        target: drydock-cache/pkl-cache
    generate:
      command: ["renderer"]
`,
			want: "absolute Linux container path",
		},
		{
			name: "cache mount target must be under drydock cache root",
			body: `    engine: container
    image: ` + digestImage + `
    cacheMounts:
      - name: pkl-cache
        target: /cache/pkl-cache
    generate:
      command: ["renderer"]
`,
			want: "target must be under /drydock-cache",
		},
		{
			name: "cache mount target rejects drydock cache root",
			body: `    engine: container
    image: ` + digestImage + `
    cacheMounts:
      - name: pkl-cache
        target: /drydock-cache
    generate:
      command: ["renderer"]
`,
			want: "without using the root itself",
		},
		{
			name: "cache mount target rejects work mount",
			body: `    engine: container
    image: ` + digestImage + `
    cacheMounts:
      - name: pkl-cache
        target: /work/.cache
    generate:
      command: ["renderer"]
`,
			want: "must not overlap the /work source mount",
		},
		{
			name: "cache mount target rejects traversal",
			body: `    engine: container
    image: ` + digestImage + `
    cacheMounts:
      - name: pkl-cache
        target: /drydock-cache/pkl-cache/../other
    generate:
      command: ["renderer"]
`,
			want: "must not contain .. path components",
		},
		{
			name: "cache mount target rejects backslash",
			body: `    engine: container
    image: ` + digestImage + `
    cacheMounts:
      - name: pkl-cache
        target: /drydock-cache\pkl-cache
    generate:
      command: ["renderer"]
`,
			want: "must use Linux container paths",
		},
		{
			name: "cache mount target rejects comma",
			body: `    engine: container
    image: ` + digestImage + `
    cacheMounts:
      - name: pkl-cache
        target: /drydock-cache/pkl,cache
    generate:
      command: ["renderer"]
`,
			want: "must not contain commas",
		},
		{
			name: "cache mount target rejects unicode control",
			body: `    engine: container
    image: ` + digestImage + `
    cacheMounts:
      - name: pkl-cache
        target: "/drydock-cache/pkl\u0085cache"
    generate:
      command: ["renderer"]
`,
			want: "must not contain commas or control characters",
		},
		{
			name: "cache mount targets must not overlap",
			body: `    engine: container
    image: ` + digestImage + `
    cacheMounts:
      - name: pkl-cache
        target: /drydock-cache/pkl-cache
      - name: nested
        target: /drydock-cache/pkl-cache/nested
    generate:
      command: ["renderer"]
`,
			want: "overlapping cache mount targets",
		},
		{
			name: "missing generate",
			body: `    engine: container
    image: ` + digestImage + `
`,
			want: "missing required field $.plugins.renderer.generate",
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

type fixturePolicyCase struct {
	path                 string
	plugin               string
	engine               Engine
	bootstrapEntrypoint  string
	bootstrapSourcePath  string
	bootstrapParameters  int
	postRenderers        int
	allowedEnvVars       []string
	seedDiscoverFileName string
	initCommand          []string
	generateCommand      []string
	copyScope            string
	copyInclude          []string
	parameterName        string
	parameterPathBase    string
	parameterPathAllow   []string
}

func TestParseFixturePolicies(t *testing.T) {
	tests := []fixturePolicyCase{
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
		{
			path:                 "pkl-exec.yaml",
			plugin:               "pkl",
			engine:               EngineExec,
			seedDiscoverFileName: "PklProject",
			initCommand:          []string{"pkl", "project", "resolve", "--cache-dir", ".drydock-pkl-cache"},
			generateCommand:      []string{"pkl", "eval", "--cache-dir", ".drydock-pkl-cache", "{{param:path}}"},
			copyScope:            ExecCopyScopeRepository,
			copyInclude:          []string{"packages/**", "pkl-packages/**"},
			parameterName:        "path",
			parameterPathBase:    ExecParameterPathBaseRepository,
			parameterPathAllow:   []string{"personal-cluster/**/*.pkl"},
		},
		{
			path:                 "bootstrap-exec-entrypoints.yaml",
			plugin:               "pkl",
			engine:               EngineExec,
			bootstrapEntrypoint:  "pkl-root",
			bootstrapSourcePath:  ".",
			bootstrapParameters:  3,
			seedDiscoverFileName: "PklProject",
			initCommand:          []string{"pkl", "project", "resolve", "--cache-dir", ".drydock-pkl-cache"},
			generateCommand:      []string{"pkl", "eval", "--cache-dir", ".drydock-pkl-cache", "{{param:path}}"},
			copyScope:            ExecCopyScopeRepository,
			copyInclude:          []string{"packages/**", "pkl-packages/**"},
			parameterName:        "path",
			parameterPathBase:    ExecParameterPathBaseRepository,
			parameterPathAllow:   []string{"personal-cluster/**/*.pkl"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			assertFixturePolicy(t, tt)
		})
	}
}

func assertFixturePolicy(t *testing.T, tt fixturePolicyCase) {
	t.Helper()
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
	assertFixtureBootstrap(t, policy, tt)
	assertFixtureSeedDiscover(t, plugin, tt)
	assertFixtureExec(t, plugin, tt)
	assertFixtureFingerprint(t, policy, policyPath)
}

func assertFixtureBootstrap(t *testing.T, policy Policy, tt fixturePolicyCase) {
	t.Helper()
	if tt.bootstrapEntrypoint == "" {
		if len(policy.Bootstrap.Entrypoints) != 0 {
			t.Fatalf("Bootstrap.Entrypoints = %#v, want none", policy.Bootstrap.Entrypoints)
		}
		return
	}
	if len(policy.Bootstrap.Entrypoints) != 1 {
		t.Fatalf("len(Bootstrap.Entrypoints) = %d, want 1", len(policy.Bootstrap.Entrypoints))
	}
	entrypoint := policy.Bootstrap.Entrypoints[0]
	if entrypoint.Name != tt.bootstrapEntrypoint || entrypoint.Plugin != tt.plugin || entrypoint.SourcePath != tt.bootstrapSourcePath {
		t.Fatalf("Bootstrap.Entrypoints[0] = %#v, want %s/%s/%s", entrypoint, tt.bootstrapEntrypoint, tt.plugin, tt.bootstrapSourcePath)
	}
	if len(entrypoint.Parameters) != tt.bootstrapParameters {
		t.Fatalf("len(Bootstrap.Entrypoints[0].Parameters) = %d, want %d", len(entrypoint.Parameters), tt.bootstrapParameters)
	}
}

func assertFixtureSeedDiscover(t *testing.T, plugin Plugin, tt fixturePolicyCase) {
	t.Helper()
	if tt.seedDiscoverFileName == "" {
		return
	}
	if plugin.ConfigManagementPlugin == nil || plugin.ConfigManagementPlugin.Discover == nil {
		t.Fatalf("ConfigManagementPlugin = %#v, want discover seed", plugin.ConfigManagementPlugin)
	}
	if got := plugin.ConfigManagementPlugin.Discover.FileName; got != tt.seedDiscoverFileName {
		t.Fatalf("configManagementPlugin.discover.fileName = %q, want %q", got, tt.seedDiscoverFileName)
	}
}

func assertFixtureExec(t *testing.T, plugin Plugin, tt fixturePolicyCase) {
	t.Helper()
	if plugin.Engine != EngineExec {
		return
	}
	if plugin.Exec == nil {
		t.Fatal("Exec = nil, want parsed exec config")
	}
	if got := len(plugin.Exec.PostRenderers); got != tt.postRenderers {
		t.Fatalf("len(PostRenderers) = %d, want %d", got, tt.postRenderers)
	}
	if got := strings.Join(plugin.Exec.Env.Allow, ","); got != strings.Join(tt.allowedEnvVars, ",") {
		t.Fatalf("Env.Allow = %q, want %q", got, strings.Join(tt.allowedEnvVars, ","))
	}
	assertFixtureExecInit(t, plugin.Exec, tt)
	assertFixtureExecGenerate(t, plugin.Exec, tt)
	assertFixtureCopy(t, plugin.Exec, tt)
	assertFixtureParameter(t, plugin.Exec, tt)
}

func assertFixtureExecInit(t *testing.T, execConfig *ExecConfig, tt fixturePolicyCase) {
	t.Helper()
	if len(tt.initCommand) == 0 {
		return
	}
	if execConfig.Init == nil {
		t.Fatal("Init = nil, want parsed init command")
	}
	if got := strings.Join(execConfig.Init.Command, "\x00"); got != strings.Join(tt.initCommand, "\x00") {
		t.Fatalf("Init.Command = %#v, want %#v", execConfig.Init.Command, tt.initCommand)
	}
}

func assertFixtureExecGenerate(t *testing.T, execConfig *ExecConfig, tt fixturePolicyCase) {
	t.Helper()
	if len(tt.generateCommand) == 0 {
		return
	}
	if got := strings.Join(execConfig.Generate.Command, "\x00"); got != strings.Join(tt.generateCommand, "\x00") {
		t.Fatalf("Generate.Command = %#v, want %#v", execConfig.Generate.Command, tt.generateCommand)
	}
}

func assertFixtureCopy(t *testing.T, execConfig *ExecConfig, tt fixturePolicyCase) {
	t.Helper()
	if tt.copyScope == "" {
		return
	}
	if execConfig.Copy.Scope != tt.copyScope {
		t.Fatalf("Copy.Scope = %q, want %q", execConfig.Copy.Scope, tt.copyScope)
	}
	if got := strings.Join(execConfig.Copy.Include, ","); got != strings.Join(tt.copyInclude, ",") {
		t.Fatalf("Copy.Include = %q, want %q", got, strings.Join(tt.copyInclude, ","))
	}
}

func assertFixtureParameter(t *testing.T, execConfig *ExecConfig, tt fixturePolicyCase) {
	t.Helper()
	if tt.parameterName == "" {
		return
	}
	if len(execConfig.Parameters.Allow) != 1 {
		t.Fatalf("len(Parameters.Allow) = %d, want 1", len(execConfig.Parameters.Allow))
	}
	parameter := execConfig.Parameters.Allow[0]
	if parameter.Name != tt.parameterName || parameter.Type != ExecParameterTypeString || !parameter.Required {
		t.Fatalf("Parameters.Allow[0] = %#v, want required string parameter %q", parameter, tt.parameterName)
	}
	if parameter.Path == nil {
		t.Fatalf("Parameters.Allow[0].Path = nil, want path policy")
	}
	if parameter.Path.Base != tt.parameterPathBase {
		t.Fatalf("Parameters.Allow[0].Path.Base = %q, want %q", parameter.Path.Base, tt.parameterPathBase)
	}
	if got := strings.Join(parameter.Path.Allow, ","); got != strings.Join(tt.parameterPathAllow, ",") {
		t.Fatalf("Parameters.Allow[0].Path.Allow = %q, want %q", got, strings.Join(tt.parameterPathAllow, ","))
	}
}

func assertFixtureFingerprint(t *testing.T, policy Policy, policyPath string) {
	t.Helper()
	fingerprint, err := Fingerprint(policy)
	if err != nil {
		t.Fatalf("Fingerprint(%q) error = %v", policyPath, err)
	}
	if fingerprint == NoPolicyFingerprint {
		t.Fatalf("Fingerprint(%q) = NoPolicyFingerprint, want stable non-empty fingerprint", policyPath)
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
		{
			name: "duplicate parameter",
			body: `    engine: exec
    generate:
      command: ["renderer"]
    parameters:
      allow:
        - name: path
          type: string
        - name: path
          type: string
`,
			want: "duplicate parameter",
		},
		{
			name: "unknown parameter type",
			body: `    engine: exec
    generate:
      command: ["renderer"]
    parameters:
      allow:
        - name: path
          type: object
`,
			want: "unsupported parameter type",
		},
		{
			name: "parameter env collision",
			body: `    engine: exec
    generate:
      command: ["renderer"]
    parameters:
      allow:
        - name: foo-bar
          type: string
        - name: foo_bar
          type: string
`,
			want: "collide in environment variable",
		},
		{
			name: "invalid parameter name",
			body: `    engine: exec
    generate:
      command: ["renderer"]
    parameters:
      allow:
        - name: "$path"
          type: string
`,
			want: "is invalid",
		},
		{
			name: "reserved parameter env",
			body: `    engine: exec
    generate:
      command: ["renderer"]
    env:
      allow: ["PARAM_PATH"]
`,
			want: "reserved",
		},
		{
			name: "path on non string parameter",
			body: `    engine: exec
    generate:
      command: ["renderer"]
    parameters:
      allow:
        - name: values
          type: array
          path: {}
`,
			want: "only supported for string",
		},
		{
			name: "escaping path allow",
			body: `    engine: exec
    generate:
      command: ["renderer"]
    parameters:
      allow:
        - name: path
          type: string
          path:
            allow: ["../secret.yaml"]
`,
			want: "relative non-escaping glob",
		},
		{
			name: "repository path base without repository copy scope",
			body: `    engine: exec
    generate:
      command: ["renderer"]
    parameters:
      allow:
        - name: path
          type: string
          path:
            base: repository
`,
			want: `requires copy.scope "repository"`,
		},
		{
			name: "unknown copy scope",
			body: `    engine: exec
    copy:
      scope: workspace
    generate:
      command: ["renderer"]
`,
			want: "unsupported copy scope",
		},
		{
			name: "repository copy without include",
			body: `    engine: exec
    copy:
      scope: repository
    generate:
      command: ["renderer"]
`,
			want: "include is required",
		},
		{
			name: "source copy with include",
			body: `    engine: exec
    copy:
      scope: source
      include: ["shared/**"]
    generate:
      command: ["renderer"]
`,
			want: "include is only supported",
		},
		{
			name: "copy include escape",
			body: `    engine: exec
    copy:
      scope: repository
      include: ["../shared/**"]
    generate:
      command: ["renderer"]
`,
			want: "parent directory segments",
		},
		{
			name: "copy include git",
			body: `    engine: exec
    copy:
      scope: repository
      include: [".git/**"]
    generate:
      command: ["renderer"]
`,
			want: ".git paths",
		},
		{
			name: "copy include backslash",
			body: `    engine: exec
    copy:
      scope: repository
      include: ["shared\\**"]
    generate:
      command: ["renderer"]
`,
			want: "backslashes",
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
    copy:
      scope: repository
      include: ["shared/**", "components/*.pkl"]
    generate:
      timeout: 3s
      command: ["argocd-vault-plugin", "generate", "."]
    init:
      timeout: 2s
      command: ["argocd-vault-plugin", "version"]
    env:
      allow: [" OP_CONNECT_HOST ", "AVP_TYPE"]
    parameters:
      allow:
        - name: path
          type: string
          required: true
          path:
            allow: ["components/*.pkl", "*.pkl"]
`))
	if err != nil {
		t.Fatalf("Parse(left) error = %v", err)
	}
	right, err := Parse("policy.yaml", []byte(`kind: PluginPolicy
apiVersion: drydock.sholdee.dev/v1alpha1
plugins:
  renderer:
    copy:
      include: ["components/*.pkl", "shared/**"]
      scope: repository
    env:
      allow: ["AVP_TYPE", "OP_CONNECT_HOST"]
    init:
      command: ["argocd-vault-plugin", "version"]
      timeout: 2s
    generate:
      command: ["argocd-vault-plugin", "generate", "."]
      timeout: 3s
    engine: exec
    parameters:
      allow:
        - required: true
          path:
            allow: ["*.pkl", "components/*.pkl"]
          type: string
          name: path
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

func TestFingerprintIncludesPluginMatch(t *testing.T) {
	left, err := Parse("policy.yaml", []byte(`apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
plugins:
  avp:
    engine: avp-compat
    match:
      discover:
        fileName: plugin.yaml
`))
	if err != nil {
		t.Fatalf("Parse(left) error = %v", err)
	}
	right, err := Parse("policy.yaml", []byte(`apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
plugins:
  avp:
    engine: avp-compat
    match:
      discover:
        find:
          glob: "**/plugin.yaml"
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
		t.Fatalf("fingerprints match for different match rules: %s", leftFingerprint)
	}
	wantJSON := `{"apiVersion":"drydock.sholdee.dev/v1alpha1","kind":"PluginPolicy","plugins":{"avp":{"engine":"avp-compat","match":{"discover":{"fileName":"plugin.yaml"}}}}}`
	if want := sha256Hex(wantJSON); leftFingerprint != want {
		t.Fatalf("Fingerprint() = %s, want %s", leftFingerprint, want)
	}
}

func TestFingerprintChangesWhenConfigManagementPluginSeedChanges(t *testing.T) {
	base := `apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
plugins:
  cmp:
    engine: native-kustomize
    configManagementPlugin:
      discover:
        fileName: plugin.yaml
      generate:
        command: ["renderer"]
        args: ["--mode", "default"]
`
	baseFingerprint := mustPolicyFingerprint(t, base)
	for _, tt := range []struct {
		name string
		data string
	}{
		{
			name: "discover",
			data: strings.Replace(base, `plugin.yaml`, `other.yaml`, 1),
		},
		{
			name: "command",
			data: strings.Replace(base, `"renderer"`, `"other-renderer"`, 1),
		},
		{
			name: "args",
			data: strings.Replace(base, `"default"`, `"changed"`, 1),
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

func TestFingerprintChangesWhenBootstrapChanges(t *testing.T) {
	base := `apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
bootstrap:
  entrypoints:
    - name: cluster-root
      plugin: pkl
      sourcePath: personal-cluster
      parameters:
        - name: path
          string: index.pkl
plugins:
  pkl:
    engine: exec
    configManagementPlugin:
      discover:
        fileName: PklProject
    generate:
      command: ["pkl", "eval", "{{param:path}}"]
  cue:
    engine: exec
    configManagementPlugin:
      discover:
        fileName: cue.mod/module.cue
    generate:
      command: ["cue", "export"]
`
	baseFingerprint := mustPolicyFingerprint(t, base)
	for _, tt := range []struct {
		name string
		data string
	}{
		{
			name: "entrypoint name",
			data: strings.Replace(base, "cluster-root", "cluster-bootstrap", 1),
		},
		{
			name: "plugin",
			data: strings.Replace(base, "plugin: pkl", "plugin: cue", 1),
		},
		{
			name: "sourcePath",
			data: strings.Replace(base, "personal-cluster", ".", 1),
		},
		{
			name: "parameter value",
			data: strings.Replace(base, "index.pkl", "bootstrap.pkl", 1),
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

func TestFingerprintChangesForContainerFields(t *testing.T) {
	base := `apiVersion: drydock.sholdee.dev/v1alpha1
kind: PluginPolicy
plugins:
  renderer:
    engine: container
    image: registry.example.com/drydock/renderer@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
    generate:
      command: ["renderer", "generate"]
      timeout: 3s
`
	baseFingerprint := mustPolicyFingerprint(t, base)
	for _, tt := range []struct {
		name string
		data string
	}{
		{
			name: "image",
			data: strings.Replace(base, strings.Repeat("a", 64), strings.Repeat("b", 64), 1),
		},
		{
			name: "network",
			data: strings.Replace(base, "    generate:\n", "    network: default\n    generate:\n", 1),
		},
		{
			name: "allow mutable flag",
			data: strings.Replace(base, "    generate:\n", "    allowMutableImageTag: true\n    generate:\n", 1),
		},
		{
			name: "lifecycle",
			data: strings.Replace(base, "timeout: 3s", "timeout: 4s", 1),
		},
		{
			name: "cache mounts",
			data: strings.Replace(base, "    generate:\n", "    cacheMounts:\n      - name: pkl-cache\n        target: /drydock-cache/pkl-cache\n    generate:\n", 1),
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

func assertSchemaRef(t *testing.T, object map[string]any, want string) {
	t.Helper()
	got, ok := object["$ref"].(string)
	if !ok {
		t.Fatalf("schema object = %#v, want string $ref", object)
	}
	if got != want {
		t.Fatalf("schema $ref = %q, want %q", got, want)
	}
}

func assertSchemaOneOfRefs(t *testing.T, object map[string]any, want []string) {
	t.Helper()
	oneOf, ok := object["oneOf"].([]any)
	if !ok {
		t.Fatalf("schema oneOf = %#v, want refs", object["oneOf"])
	}
	if len(oneOf) != len(want) {
		t.Fatalf("schema oneOf length = %d, want %d", len(oneOf), len(want))
	}
	for index, item := range oneOf {
		child, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("schema oneOf[%d] = %#v, want object", index, item)
		}
		assertSchemaRef(t, child, want[index])
	}
}

func assertSchemaRequired(t *testing.T, object map[string]any, want ...string) {
	t.Helper()
	required, ok := object["required"].([]any)
	if !ok {
		t.Fatalf("schema required = %#v, want list", object["required"])
	}
	if len(required) != len(want) {
		t.Fatalf("schema required length = %d, want %d", len(required), len(want))
	}
	for index, value := range want {
		got, ok := required[index].(string)
		if !ok || got != value {
			t.Fatalf("schema required[%d] = %#v, want %q", index, required[index], value)
		}
	}
}

func assertSchemaEnum(t *testing.T, object map[string]any, want ...string) {
	t.Helper()
	enum, ok := object["enum"].([]any)
	if !ok {
		t.Fatalf("schema enum = %#v, want list", object["enum"])
	}
	if len(enum) != len(want) {
		t.Fatalf("schema enum length = %d, want %d", len(enum), len(want))
	}
	for index, value := range want {
		got, ok := enum[index].(string)
		if !ok || got != value {
			t.Fatalf("schema enum[%d] = %#v, want %q", index, enum[index], value)
		}
	}
}

func assertSchemaDefault(t *testing.T, object map[string]any, want any) {
	t.Helper()
	got, ok := object["default"]
	if !ok {
		t.Fatalf("schema object = %#v, want default %#v", object, want)
	}
	if got != want {
		t.Fatalf("schema default = %#v, want %#v", got, want)
	}
}

func bootstrapParametersByName(params []BootstrapParameter) map[string]BootstrapParameter {
	out := make(map[string]BootstrapParameter, len(params))
	for _, param := range params {
		out[param.Name] = param
	}
	return out
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
