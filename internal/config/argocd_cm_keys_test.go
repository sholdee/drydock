package config

import (
	"os"
	"regexp"
	"testing"
)

// TestArgoCDConfigMapSettingKeysListEveryKeyApplyCMMapReads scans
// provider_cm.go for every values["..."] lookup, so a key that applyCMMap
// starts to read is also a key that settings discovery recognizes.
func TestArgoCDConfigMapSettingKeysListEveryKeyApplyCMMapReads(t *testing.T) {
	source, err := os.ReadFile("provider_cm.go")
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	matches := regexp.MustCompile(`values\["([^"]+)"\]`).FindAllStringSubmatch(string(source), -1)
	if len(matches) == 0 {
		t.Fatal("found no values[...] lookups in provider_cm.go")
	}
	read := map[string]struct{}{}
	for _, match := range matches {
		read[match[1]] = struct{}{}
		if !IsArgoCDConfigMapSettingKey(match[1]) {
			t.Errorf("applyCMMap reads %q, but IsArgoCDConfigMapSettingKey does not list it", match[1])
		}
	}
	for key := range argoCDConfigMapSettingKeys {
		if _, ok := read[key]; !ok {
			t.Errorf("argoCDConfigMapSettingKeys lists %q, which applyCMMap does not read", key)
		}
	}
}

func TestIsArgoCDHelmValuesConfigMapKey(t *testing.T) {
	for key, want := range map[string]bool{
		"installationID":                        true,
		"cluster.inClusterEnabled":              true,
		"helm.valuesFileSchemes":                true,
		"resource.ignoreResourceUpdatesEnabled": true,
		"resourceExclusionsAdditional":          true,
		"resource.customizations":               true,
		"resource.customizationsX.health.Foo":   true,
		"create":                                false,
		"annotations":                           false,
		"url":                                   false,
		"admin.enabled":                         false,
	} {
		if got := IsArgoCDHelmValuesConfigMapKey(key); got != want {
			t.Errorf("IsArgoCDHelmValuesConfigMapKey(%q) = %v, want %v", key, got, want)
		}
	}
}
