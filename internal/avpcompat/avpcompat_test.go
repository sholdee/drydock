package avpcompat

import (
	"strings"
	"testing"
)

func TestReplaceStringSubstitutesInlinePathPlaceholder(t *testing.T) {
	got, changed := ReplaceString("argocd.<path:vaults/Kubernetes/items/cluster#domain>")
	if !changed {
		t.Fatal("ReplaceString() changed = false, want true")
	}
	if !strings.HasPrefix(got, "argocd."+redactedPrefix) {
		t.Fatalf("ReplaceString() = %q, want inline redacted value", got)
	}
	assertNoSecretMaterial(t, got)

	again, againChanged := ReplaceString("argocd.<path:vaults/Kubernetes/items/cluster#domain>")
	if !againChanged {
		t.Fatal("ReplaceString() second call changed = false, want true")
	}
	if again != got {
		t.Fatalf("ReplaceString() = %q, want deterministic %q", again, got)
	}
}

func TestReplaceStringSubstitutesMultiplePlaceholders(t *testing.T) {
	got, changed := ReplaceString("<path:secret/data/app#username>:<path:secret/data/app#password>")
	if !changed {
		t.Fatal("ReplaceString() changed = false, want true")
	}

	parts := strings.Split(got, ":")
	if len(parts) != 2 {
		t.Fatalf("ReplaceString() = %q, want two colon-separated replacements", got)
	}
	if parts[0] == parts[1] {
		t.Fatalf("ReplaceString() = %q, want different placeholders to produce different values", got)
	}
	for _, part := range parts {
		if !strings.HasPrefix(part, redactedPrefix) {
			t.Fatalf("replacement %q does not have prefix %q", part, redactedPrefix)
		}
	}
	assertNoSecretMaterial(t, got)
}

func TestReplaceStringIgnoresUnsupportedAngleTokens(t *testing.T) {
	tests := []string{
		"keep <username>",
		"keep <path:secret/data/app>",
		"keep <path:#password>",
		"keep <path:secret/data/app#>",
		"keep <path:secret/data/app#password",
	}

	for _, tt := range tests {
		got, changed := ReplaceString(tt)
		if changed {
			t.Fatalf("ReplaceString(%q) changed = true, want false", tt)
		}
		if got != tt {
			t.Fatalf("ReplaceString(%q) = %q, want unchanged", tt, got)
		}
	}
}

func TestContainsPlaceholder(t *testing.T) {
	if !ContainsPlaceholder("value=<path:secret/data/app#password>") {
		t.Fatal("ContainsPlaceholder() = false, want true")
	}
	if ContainsPlaceholder("value=<password>") {
		t.Fatal("ContainsPlaceholder() = true for unsupported generic token, want false")
	}
}

func TestReplaceValueSubstitutesDecodedValuesWithoutMutatingInput(t *testing.T) {
	input := map[string]any{
		"host": "https://<path:secret/data/app#host>",
		"env": []any{
			map[string]any{"value": "<path:secret/data/app#token>"},
			true,
			3,
		},
		"unchanged": "plain",
	}

	replaced, changed := ReplaceValue(input)
	if !changed {
		t.Fatal("ReplaceValue() changed = false, want true")
	}

	got, ok := replaced.(map[string]any)
	if !ok {
		t.Fatalf("ReplaceValue() type = %T, want map[string]any", replaced)
	}
	if input["host"] != "https://<path:secret/data/app#host>" {
		t.Fatalf("ReplaceValue() mutated input host = %q", input["host"])
	}

	host, ok := got["host"].(string)
	if !ok {
		t.Fatalf("replaced host type = %T, want string", got["host"])
	}
	if strings.Contains(host, "<path:") {
		t.Fatalf("replaced host still contains AVP placeholder: %q", host)
	}

	env, ok := got["env"].([]any)
	if !ok {
		t.Fatalf("replaced env type = %T, want []any", got["env"])
	}
	nested, ok := env[0].(map[string]any)
	if !ok {
		t.Fatalf("replaced nested env type = %T, want map[string]any", env[0])
	}
	nestedValue, ok := nested["value"].(string)
	if !ok {
		t.Fatalf("replaced nested value type = %T, want string", nested["value"])
	}
	if !strings.HasPrefix(nestedValue, redactedPrefix) {
		t.Fatalf("nested value = %q, want redacted prefix", nestedValue)
	}
	if env[1] != true || env[2] != 3 {
		t.Fatalf("scalar passthrough changed env = %#v", env)
	}
	assertNoSecretMaterial(t, host)
	assertNoSecretMaterial(t, nestedValue)
}

func TestReplaceValueReportsUnchangedScalars(t *testing.T) {
	got, changed := ReplaceValue(map[string]any{
		"enabled": true,
		"count":   2,
		"name":    "plain",
	})
	if changed {
		t.Fatal("ReplaceValue() changed = true, want false")
	}
	if _, ok := got.(map[string]any); !ok {
		t.Fatalf("ReplaceValue() type = %T, want map[string]any", got)
	}
}

func assertNoSecretMaterial(t *testing.T, value string) {
	t.Helper()
	for _, forbidden := range []string{"vaults", "secret/data", "password", "token", "domain"} {
		if strings.Contains(value, forbidden) {
			t.Fatalf("value %q contains forbidden secret material %q", value, forbidden)
		}
	}
}
