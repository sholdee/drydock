package pluginpolicy

import (
	"crypto/sha256"
	"encoding/hex"
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
