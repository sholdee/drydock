package render

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sholdee/drydock/internal/diagnostic"
	goyaml "go.yaml.in/yaml/v3"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

const ksopsTestGeneratorManifest = `apiVersion: viaduct.ai/v1
kind: ksops
metadata:
  name: demo-secret-generator
files:
  - ./demo-secret.sops.yaml
`

// ksopsTestSopsSecret mirrors the real-world KSOPS fixture shape
// (lehmanju/homecluster): a complete Secret manifest with plaintext structure
// and key names, ENC[AES256_GCM,...] data values, and a trailing sops
// metadata block.
func ksopsTestSopsSecret(name, key, ciphertext string) string {
	return `apiVersion: v1
kind: Secret
metadata:
    name: ` + name + `
data:
    ` + key + `: ENC[AES256_GCM,data:` + ciphertext + `,iv:aksv1mXYiMja9Guq9SCT6wPjrXTo2MHX6JMGGyYmIo8=,tag:IBjPwh9GgBEIClIjaXyyVQ==,type:str]
sops:
    age:
        - recipient: age1zc4knw29dcgns9zdpyxyx56xsax6s9vsu82sgef3mph56mdr7fusp6v9yh
    lastmodified: "2025-06-29T07:49:54Z"
    mac: ENC[AES256_GCM,data:mv+e2VOLIb3om/Rm95A+lchk7xWjCLwCEjTYqB1ULwM8k7ekXHM4vzEhIJ6pt2PPKptW2Pj1BavyLjg4er/go8Z51ZZEt/1g9E85BphdiDH4G6ksYY9GEjf/wKNXNMTH5MZeWaKvzJQVgU=,iv:Wx0pFIZpeuWr4hIzoB1j1mPPH80V4+CvtMZGGi+EpZU=,tag:gkmsqAnezpZs7S4dW5c+NA==,type:str]
    encrypted_regex: ^(data|stringData)$
    version: 3.10.2
`
}

func writeKSOPSFixtureApp(t *testing.T, root string) {
	t.Helper()
	writeFile(t, filepath.Join(root, "apps", "demo", "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
generators:
  - ./secret-generator.yaml
`)
	writeFile(t, filepath.Join(root, "apps", "demo", "secret-generator.yaml"), ksopsTestGeneratorManifest)
	writeFile(t, filepath.Join(root, "apps", "demo", "demo-secret.sops.yaml"), ksopsTestSopsSecret("demo-secret", "TOKEN", "NYI8Q9o3original"))
}

func renderKSOPSFixture(t *testing.T, root string, enableKSOPSCompat bool) ([]Manifest, []diagnostic.Diagnostic, error) {
	t.Helper()
	return (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     filepath.Join("apps", "demo"),
	}, RenderOptions{EnableKSOPSCompat: enableKSOPSCompat})
}

func serializeRenderedManifests(t *testing.T, manifests []Manifest) string {
	t.Helper()
	var builder strings.Builder
	for _, manifest := range manifests {
		data, err := goyaml.Marshal(manifest.Object.Object)
		if err != nil {
			t.Fatalf("marshal rendered manifest: %v", err)
		}
		builder.WriteString("---\n")
		builder.Write(data)
	}
	return builder.String()
}

func manifestNamed(t *testing.T, manifests []Manifest, kind, name string) *unstructured.Unstructured {
	t.Helper()
	for _, manifest := range manifests {
		if manifest.Object.GetKind() == kind && manifest.Object.GetName() == name {
			return manifest.Object
		}
	}
	t.Fatalf("no %s named %q in %#v", kind, name, manifests)
	return nil
}

func TestKustomizeKSOPSCompatRendersPlaceholderSecret(t *testing.T) {
	root := t.TempDir()
	writeKSOPSFixtureApp(t, root)

	manifests, diags, err := renderKSOPSFixture(t, root, true)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(manifests) != 1 {
		t.Fatalf("len(manifests) = %d, want 1: %#v", len(manifests), manifests)
	}
	secret := manifestNamed(t, manifests, "Secret", "demo-secret")
	data, found, err := unstructured.NestedStringMap(secret.Object, "data")
	if err != nil || !found {
		t.Fatalf("Secret data missing: found=%v err=%v object=%#v", found, err, secret.Object)
	}
	decoded, err := base64.StdEncoding.DecodeString(data["TOKEN"])
	if err != nil {
		t.Fatalf("data.TOKEN = %q is not valid base64: %v", data["TOKEN"], err)
	}
	if !strings.HasPrefix(string(decoded), ksopsRedactedPrefix) {
		t.Fatalf("decoded data.TOKEN = %q, want %q prefix", decoded, ksopsRedactedPrefix)
	}
	if _, found, _ := unstructured.NestedFieldNoCopy(secret.Object, "sops"); found {
		t.Fatalf("rendered Secret retains sops metadata: %#v", secret.Object)
	}
	if len(diags) != 1 || diags[0].Code != "kustomize.ksops-compat-substituted" || diags[0].Severity != diagnostic.SeverityWarning {
		t.Fatalf("diagnostics = %#v, want one kustomize.ksops-compat-substituted warning", diags)
	}
	if !strings.Contains(diags[0].Message, "1 sops files substituted") {
		t.Fatalf("diagnostic message = %q, want substituted-file count", diags[0].Message)
	}
}

func TestKustomizeKSOPSGeneratorWithoutCompatFailsActionably(t *testing.T) {
	root := t.TempDir()
	writeKSOPSFixtureApp(t, root)

	_, _, err := renderKSOPSFixture(t, root, false)
	if err == nil {
		t.Fatal("Render() error = nil, want actionable KSOPS diagnostic")
	}
	if !strings.Contains(err.Error(), "./secret-generator.yaml") {
		t.Fatalf("Render() error = %v, want generator manifest path", err)
	}
	if !strings.Contains(err.Error(), "--enable-ksops-compat") {
		t.Fatalf("Render() error = %v, want --enable-ksops-compat suggestion", err)
	}
}

func TestKustomizeBuiltinGeneratorConfigStillRenders(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "demo", "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
generators:
  - ./cm-generator.yaml
`)
	writeFile(t, filepath.Join(root, "apps", "demo", "cm-generator.yaml"), `
apiVersion: builtin
kind: ConfigMapGenerator
metadata:
  name: generated-cm
literals:
  - key=value
`)

	manifests, diags, err := renderKSOPSFixture(t, root, false)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(manifests) != 1 || manifests[0].Object.GetKind() != "ConfigMap" || !strings.HasPrefix(manifests[0].Object.GetName(), "generated-cm") {
		t.Fatalf("manifests = %#v, want generated-cm ConfigMap", manifests)
	}
}

func TestKustomizeKSOPSInputDigestCoversSopsFiles(t *testing.T) {
	root := t.TempDir()
	writeKSOPSFixtureApp(t, root)

	paths, err := KustomizeInputDigestPaths(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     filepath.Join("apps", "demo"),
	}, RenderOptions{EnableKSOPSCompat: true})
	if err != nil {
		t.Fatalf("KustomizeInputDigestPaths() error = %v", err)
	}
	found := false
	for _, path := range paths {
		if path.Path == "apps/demo/demo-secret.sops.yaml" {
			found = true
			if path.Optional {
				t.Fatalf("sops file digest path is optional: %#v", path)
			}
		}
	}
	if !found {
		t.Fatalf("digest paths %#v missing apps/demo/demo-secret.sops.yaml — a sops edit would not rotate the render cache key", paths)
	}
}

// TestKustomizeInlineKSOPSInputDigestCoversSopsFiles mirrors the path-entry
// digest test above for inline generators: entries: the inline manifest lives
// in the kustomization file (always digested), but its files: referents are
// separate render inputs of ksops-compat emulation. Before they joined the
// digest, a sops edit behind an inline generator produced a stale persistent
// cache hit.
func TestKustomizeInlineKSOPSInputDigestCoversSopsFiles(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "demo", "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
generators:
  - |
    apiVersion: viaduct.ai/v1
    kind: ksops
    metadata:
      name: inline-generator
    files:
      - ./demo-secret.sops.yaml
`)
	writeFile(t, filepath.Join(root, "apps", "demo", "demo-secret.sops.yaml"), ksopsTestSopsSecret("demo-secret", "TOKEN", "inlineCipher"))

	paths, err := KustomizeInputDigestPaths(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     filepath.Join("apps", "demo"),
	}, RenderOptions{EnableKSOPSCompat: true})
	if err != nil {
		t.Fatalf("KustomizeInputDigestPaths() error = %v", err)
	}
	found := false
	for _, path := range paths {
		if path.Path == "apps/demo/demo-secret.sops.yaml" {
			found = true
			if path.Optional {
				t.Fatalf("sops file digest path is optional: %#v", path)
			}
		}
	}
	if !found {
		t.Fatalf("digest paths %#v missing apps/demo/demo-secret.sops.yaml — a sops edit behind an inline generator would not rotate the render cache key", paths)
	}
}

func TestKustomizeKSOPSCompatStructuralEditChangesPlaceholders(t *testing.T) {
	root := t.TempDir()
	writeKSOPSFixtureApp(t, root)

	manifests, _, err := renderKSOPSFixture(t, root, true)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	baseline := serializeRenderedManifests(t, manifests)

	// Structural edit: add a key to the sops fixture. The placeholder set in
	// the output must change (new identity), distinct from the value-only
	// rotation case below.
	writeFile(t, filepath.Join(root, "apps", "demo", "demo-secret.sops.yaml"), strings.Replace(
		ksopsTestSopsSecret("demo-secret", "TOKEN", "NYI8Q9o3original"),
		"data:\n    TOKEN:",
		"data:\n    EXTRA: ENC[AES256_GCM,data:extravalue,type:str]\n    TOKEN:",
		1,
	))

	manifests, _, err = renderKSOPSFixture(t, root, true)
	if err != nil {
		t.Fatalf("Render() after structural edit error = %v", err)
	}
	edited := serializeRenderedManifests(t, manifests)
	if edited == baseline {
		t.Fatal("structural sops edit did not change the rendered placeholder set")
	}
	secret := manifestNamed(t, manifests, "Secret", "demo-secret")
	data, _, _ := unstructured.NestedStringMap(secret.Object, "data")
	if len(data) != 2 {
		t.Fatalf("Secret data = %#v, want TOKEN and EXTRA placeholders", data)
	}
	if data["EXTRA"] == data["TOKEN"] {
		t.Fatalf("placeholders are not identity-derived: EXTRA == TOKEN == %q", data["EXTRA"])
	}
}

func TestKustomizeKSOPSCompatValueOnlyRotationRendersIdentically(t *testing.T) {
	root := t.TempDir()
	writeKSOPSFixtureApp(t, root)

	manifests, _, err := renderKSOPSFixture(t, root, true)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	baseline := serializeRenderedManifests(t, manifests)

	// Value-only rotation: only the ENC[...] ciphertext changes. The file
	// bytes change (cache miss via the digest path pinned above), but the
	// placeholder derives from identity, not ciphertext, so the rendered
	// output must be byte-identical.
	writeFile(t, filepath.Join(root, "apps", "demo", "demo-secret.sops.yaml"), ksopsTestSopsSecret("demo-secret", "TOKEN", "rotatedCiphertext"))

	manifests, _, err = renderKSOPSFixture(t, root, true)
	if err != nil {
		t.Fatalf("Render() after rotation error = %v", err)
	}
	if rotated := serializeRenderedManifests(t, manifests); rotated != baseline {
		t.Fatalf("value-only sops rotation changed rendered output:\nbaseline:\n%s\nrotated:\n%s", baseline, rotated)
	}
}

func TestKustomizeKSOPSCompatReplacesStringDataAndCustomFields(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "demo", "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
generators:
  - ./secret-generator.yaml
`)
	writeFile(t, filepath.Join(root, "apps", "demo", "secret-generator.yaml"), ksopsTestGeneratorManifest)
	writeFile(t, filepath.Join(root, "apps", "demo", "demo-secret.sops.yaml"), `apiVersion: v1
kind: Secret
metadata:
    name: demo-secret
stringData:
    PASSWORD: ENC[AES256_GCM,data:plainfield,type:str]
customField: ENC[AES256_GCM,data:customregex,type:str]
sops:
    encrypted_regex: ^(data|stringData|customField)$
    version: 3.10.2
`)

	manifests, _, err := renderKSOPSFixture(t, root, true)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	secret := manifestNamed(t, manifests, "Secret", "demo-secret")
	stringData, _, _ := unstructured.NestedStringMap(secret.Object, "stringData")
	if !strings.HasPrefix(stringData["PASSWORD"], ksopsRedactedPrefix) {
		t.Fatalf("stringData.PASSWORD = %q, want plain %q marker", stringData["PASSWORD"], ksopsRedactedPrefix)
	}
	custom, _, _ := unstructured.NestedString(secret.Object, "customField")
	if !strings.HasPrefix(custom, ksopsRedactedPrefix) {
		t.Fatalf("customField = %q, want plain %q marker (custom encrypted_regex values must be replaced too)", custom, ksopsRedactedPrefix)
	}
}

func TestKustomizeKSOPSCompatSupportsMultiDocMultiFileMultiGenerator(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "demo", "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
generators:
  - ./generator-a.yaml
  - ./generator-b.yaml
`)
	writeFile(t, filepath.Join(root, "apps", "demo", "generator-a.yaml"), `apiVersion: viaduct.ai/v1
kind: ksops
metadata:
  name: generator-a
files:
  - ./a.sops.yaml
  - ./b.sops.yaml
`)
	writeFile(t, filepath.Join(root, "apps", "demo", "generator-b.yaml"), `apiVersion: viaduct.ai/v1
kind: ksops
metadata:
  name: generator-b
files:
  - ./c.sops.yaml
`)
	multiDoc := ksopsTestSopsSecret("secret-a-one", "TOKEN", "aOne") + "---\n" + ksopsTestSopsSecret("secret-a-two", "TOKEN", "aTwo")
	writeFile(t, filepath.Join(root, "apps", "demo", "a.sops.yaml"), multiDoc)
	writeFile(t, filepath.Join(root, "apps", "demo", "b.sops.yaml"), ksopsTestSopsSecret("secret-b", "TOKEN", "bOne"))
	writeFile(t, filepath.Join(root, "apps", "demo", "c.sops.yaml"), ksopsTestSopsSecret("secret-c", "TOKEN", "cOne"))

	manifests, diags, err := renderKSOPSFixture(t, root, true)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(manifests) != 4 {
		t.Fatalf("len(manifests) = %d, want 4: %#v", len(manifests), manifests)
	}
	for _, name := range []string{"secret-a-one", "secret-a-two", "secret-b", "secret-c"} {
		manifestNamed(t, manifests, "Secret", name)
	}
	if len(diags) != 1 || !strings.Contains(diags[0].Message, "3 sops files substituted") {
		t.Fatalf("diagnostics = %#v, want one warning counting 3 substituted sops files", diags)
	}
}

func TestKustomizeKSOPSCompatRejectsBoundaryEscapingFiles(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "demo", "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
generators:
  - ./secret-generator.yaml
`)
	writeFile(t, filepath.Join(root, "apps", "demo", "secret-generator.yaml"), `apiVersion: viaduct.ai/v1
kind: ksops
metadata:
  name: escaping-generator
files:
  - ../../../outside.sops.yaml
`)

	_, _, err := renderKSOPSFixture(t, root, true)
	if err == nil {
		t.Fatal("Render() error = nil, want boundary escape error")
	}
	if !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("Render() error = %v, want boundary escape error", err)
	}
}

// TestKustomizeInlineKSOPSGeneratorRejectsBoundaryEscapingFiles pins that an
// inline generator's files: referents are boundary-validated at the graph
// level, resolved relative to the kustomization directory — fork-PR content
// is attacker-influenced, and inline entries must not be a validation bypass.
func TestKustomizeInlineKSOPSGeneratorRejectsBoundaryEscapingFiles(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "demo", "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
generators:
  - |
    apiVersion: viaduct.ai/v1
    kind: ksops
    metadata:
      name: escaping-inline-generator
    files:
      - ../../../outside.sops.yaml
`)

	if _, err := validateKustomizeGraph(context.Background(), root, filepath.Join(root, "apps", "demo")); err == nil {
		t.Fatal("validateKustomizeGraph() error = nil, want boundary escape error")
	} else if !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("validateKustomizeGraph() error = %v, want boundary escape error", err)
	}

	_, _, err := renderKSOPSFixture(t, root, true)
	if err == nil {
		t.Fatal("Render() error = nil, want boundary escape error")
	}
	if !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("Render() error = %v, want boundary escape error", err)
	}
}

// TestKustomizeMixedKSOPSAndBuiltinGeneratorManifestFailsLoudly pins the
// loud rejection of a single generator manifest mixing KSOPS and builtin
// documents: splitting it would require rewriting a hard-linked workspace
// file, so drydock refuses to render incompletely in both modes.
func TestKustomizeMixedKSOPSAndBuiltinGeneratorManifestFailsLoudly(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "demo", "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
generators:
  - ./mixed-generator.yaml
`)
	writeFile(t, filepath.Join(root, "apps", "demo", "mixed-generator.yaml"), ksopsTestGeneratorManifest+`---
apiVersion: builtin
kind: ConfigMapGenerator
metadata:
  name: mixed-cm
literals:
  - key=value
`)
	writeFile(t, filepath.Join(root, "apps", "demo", "demo-secret.sops.yaml"), ksopsTestSopsSecret("demo-secret", "TOKEN", "mixedCipher"))

	for _, enable := range []bool{false, true} {
		_, _, err := renderKSOPSFixture(t, root, enable)
		if err == nil {
			t.Fatalf("Render(enableKSOPSCompat=%v) error = nil, want mixed-generator error", enable)
		}
		if !strings.Contains(err.Error(), "mixes KSOPS and builtin generator documents") {
			t.Fatalf("Render(enableKSOPSCompat=%v) error = %v, want mixed-generator error", enable, err)
		}
	}
}

func TestKustomizeUnsupportedGeneratorFailsBothModes(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "demo", "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
generators:
  - ./custom-generator.yaml
`)
	writeFile(t, filepath.Join(root, "apps", "demo", "custom-generator.yaml"), `
apiVersion: someteam.example.com/v1
kind: SecretsFetcher
metadata:
  name: custom
`)

	for _, enable := range []bool{false, true} {
		_, _, err := renderKSOPSFixture(t, root, enable)
		if err == nil {
			t.Fatalf("Render(enableKSOPSCompat=%v) error = nil, want unsupported generator error", enable)
		}
		if !strings.Contains(err.Error(), "someteam.example.com/v1/SecretsFetcher is unsupported") {
			t.Fatalf("Render(enableKSOPSCompat=%v) error = %v, want unsupported generator kind", enable, err)
		}
	}
}

func TestKustomizeInlineKSOPSGeneratorIsEmulated(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "demo", "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
generators:
  - |
    apiVersion: viaduct.ai/v1
    kind: ksops
    metadata:
      name: inline-generator
    files:
      - ./demo-secret.sops.yaml
`)
	writeFile(t, filepath.Join(root, "apps", "demo", "demo-secret.sops.yaml"), ksopsTestSopsSecret("demo-secret", "TOKEN", "inlineCipher"))

	manifests, diags, err := renderKSOPSFixture(t, root, true)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	manifestNamed(t, manifests, "Secret", "demo-secret")
	if len(diags) != 1 || diags[0].Code != "kustomize.ksops-compat-substituted" {
		t.Fatalf("diagnostics = %#v, want ksops-compat-substituted warning", diags)
	}

	_, _, err = renderKSOPSFixture(t, root, false)
	if err == nil || !strings.Contains(err.Error(), "--enable-ksops-compat") {
		t.Fatalf("Render() without the mode error = %v, want --enable-ksops-compat suggestion", err)
	}
}

func TestKustomizeInlineBuiltinGeneratorLeftForKrusty(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "demo", "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
generators:
  - |
    apiVersion: builtin
    kind: ConfigMapGenerator
    metadata:
      name: inline-cm
    literals:
      - key=value
`)

	manifests, diags, err := renderKSOPSFixture(t, root, false)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(manifests) != 1 || !strings.HasPrefix(manifests[0].Object.GetName(), "inline-cm") {
		t.Fatalf("manifests = %#v, want inline-cm ConfigMap", manifests)
	}
}

func TestKustomizeInlineExecGeneratorFailsBothModes(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "demo", "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
generators:
  - |
    apiVersion: example.com/v1
    kind: MyExecGenerator
    metadata:
      name: exec-generator
      annotations:
        config.kubernetes.io/function: |
          exec:
            path: my-generator
`)

	for _, enable := range []bool{false, true} {
		_, _, err := renderKSOPSFixture(t, root, enable)
		if err == nil {
			t.Fatalf("Render(enableKSOPSCompat=%v) error = nil, want unsupported generator error", enable)
		}
		if !strings.Contains(err.Error(), "unsupported") {
			t.Fatalf("Render(enableKSOPSCompat=%v) error = %v, want unsupported generator error", enable, err)
		}
	}
}

func TestKustomizeKSOPSCompatRejectsUnsupportedKSOPSFields(t *testing.T) {
	tests := []struct {
		name  string
		extra string
	}{
		{"secretFrom", "secretFrom:\n  - metadata:\n      name: from-files\n    files:\n      - ./secret.enc.conf\n"},
		{"binaryFiles", "binaryFiles:\n  - ./secret.enc\n"},
		{"envs", "envs:\n  - ./secret.enc.env\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, filepath.Join(root, "apps", "demo", "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
generators:
  - ./secret-generator.yaml
`)
			writeFile(t, filepath.Join(root, "apps", "demo", "secret-generator.yaml"), "apiVersion: viaduct.ai/v1\nkind: ksops\nmetadata:\n  name: demo\n"+tt.extra)

			_, _, err := renderKSOPSFixture(t, root, true)
			if err == nil {
				t.Fatalf("Render() error = nil, want unsupported ksops %s error", tt.name)
			}
			if !strings.Contains(err.Error(), "ksops "+tt.name+" is unsupported") {
				t.Fatalf("Render() error = %v, want unsupported ksops %s error", err, tt.name)
			}
		})
	}
}

func TestKustomizeKSOPSCompatRejectsGeneratorOptionAnnotations(t *testing.T) {
	t.Run("on generator manifest", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, filepath.Join(root, "apps", "demo", "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
generators:
  - ./secret-generator.yaml
`)
		writeFile(t, filepath.Join(root, "apps", "demo", "secret-generator.yaml"), `apiVersion: viaduct.ai/v1
kind: ksops
metadata:
  name: demo
  annotations:
    kustomize.config.k8s.io/behavior: replace
files:
  - ./demo-secret.sops.yaml
`)
		writeFile(t, filepath.Join(root, "apps", "demo", "demo-secret.sops.yaml"), ksopsTestSopsSecret("demo-secret", "TOKEN", "cipher"))

		_, _, err := renderKSOPSFixture(t, root, true)
		if err == nil || !strings.Contains(err.Error(), `annotation "kustomize.config.k8s.io/behavior" is unsupported`) {
			t.Fatalf("Render() error = %v, want unsupported generator option annotation error", err)
		}
	})

	t.Run("inside sops document", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, filepath.Join(root, "apps", "demo", "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
generators:
  - ./secret-generator.yaml
`)
		writeFile(t, filepath.Join(root, "apps", "demo", "secret-generator.yaml"), ksopsTestGeneratorManifest)
		writeFile(t, filepath.Join(root, "apps", "demo", "demo-secret.sops.yaml"), `apiVersion: v1
kind: Secret
metadata:
    name: demo-secret
    annotations:
        kustomize.config.k8s.io/needs-hash: "true"
data:
    TOKEN: ENC[AES256_GCM,data:cipher,type:str]
sops:
    version: 3.10.2
`)

		_, _, err := renderKSOPSFixture(t, root, true)
		if err == nil || !strings.Contains(err.Error(), `annotation "kustomize.config.k8s.io/needs-hash" is unsupported`) {
			t.Fatalf("Render() error = %v, want unsupported generator option annotation error", err)
		}
	})
}

func TestParseKustomizeBuildOptionsAcceptsPluginFlags(t *testing.T) {
	settings, err := parseKustomizeBuildOptions([]string{"--enable-alpha-plugins", "--enable-exec"})
	if err != nil {
		t.Fatalf("parseKustomizeBuildOptions() error = %v", err)
	}
	if !settings.EnableAlphaPlugins {
		t.Fatal("EnableAlphaPlugins = false, want true")
	}
	if !settings.EnableExec {
		t.Fatal("EnableExec = false, want true")
	}
}

func TestKustomizeRendererAcceptsPluginBuildOptionsWithoutGenerators(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "demo", "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
resources:
  - cm.yaml
`)
	writeFile(t, filepath.Join(root, "apps", "demo", "cm.yaml"), `
apiVersion: v1
kind: ConfigMap
metadata:
  name: demo
`)

	manifests, diags, err := (KustomizeRenderer{}).Render(context.Background(), ResolvedSource{
		RepoRoot: root,
		Path:     filepath.Join("apps", "demo"),
	}, RenderOptions{BuildOptions: []string{"--enable-helm", "--enable-alpha-plugins", "--enable-exec"}})
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if len(diags) != 0 {
		t.Fatalf("diagnostics = %#v", diags)
	}
	if len(manifests) != 1 || manifests[0].Object.GetName() != "demo" {
		t.Fatalf("manifests = %#v, want demo ConfigMap", manifests)
	}
}

func TestKustomizeKSOPSCompatIsDeterministic(t *testing.T) {
	root := t.TempDir()
	writeKSOPSFixtureApp(t, root)

	first, _, err := renderKSOPSFixture(t, root, true)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	second, _, err := renderKSOPSFixture(t, root, true)
	if err != nil {
		t.Fatalf("Render() error = %v", err)
	}
	if serializeRenderedManifests(t, first) != serializeRenderedManifests(t, second) {
		t.Fatal("two renders of identical inputs produced different placeholder output bytes")
	}
}

func TestKustomizeKSOPSCompatReadsCrossDirectorySopsFiles(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "demo", "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
generators:
  - ./secret-generator.yaml
`)
	writeFile(t, filepath.Join(root, "apps", "demo", "secret-generator.yaml"), `apiVersion: viaduct.ai/v1
kind: ksops
metadata:
  name: cross-dir-generator
files:
  - ../../secrets/demo.sops.yaml
`)
	writeFile(t, filepath.Join(root, "secrets", "demo.sops.yaml"), ksopsTestSopsSecret("cross-dir-secret", "TOKEN", "crossDirCipher"))

	manifests, _, err := renderKSOPSFixture(t, root, true)
	if err != nil {
		t.Fatalf("Render() error = %v (the workspace copier must cover ksops files targets outside copied directories)", err)
	}
	manifestNamed(t, manifests, "Secret", "cross-dir-secret")
}

func TestKustomizeInlineKSOPSCompatReadsCrossDirectorySopsFiles(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "demo", "kustomization.yaml"), `
apiVersion: kustomize.config.k8s.io/v1beta1
kind: Kustomization
generators:
  - |-
    apiVersion: viaduct.ai/v1
    kind: ksops
    metadata:
      name: inline-cross-dir-generator
    files:
      - ../../secrets/demo.sops.yaml
`)
	writeFile(t, filepath.Join(root, "secrets", "demo.sops.yaml"), ksopsTestSopsSecret("inline-cross-dir-secret", "TOKEN", "inlineCrossDirCipher"))

	manifests, _, err := renderKSOPSFixture(t, root, true)
	if err != nil {
		t.Fatalf("Render() error = %v (the workspace copier must cover inline ksops files targets outside copied directories)", err)
	}
	manifestNamed(t, manifests, "Secret", "inline-cross-dir-secret")
}

// snapshotTree records every regular file under root by relative path and
// content, for asserting that a render leaves the original repository tree
// untouched.
func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = string(content)
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot tree %q: %v", root, err)
	}
	return out
}

// TestKustomizeKSOPSCompatRejectsPlantedGeneratedPathCollision is the
// permanent regression test for the hard-link write-through exploit: the
// workspace copier materializes repository files as hard links, and the ksops
// placeholder path (.drydock/ksops/<graph>-<entry>-<file>-<basename>.yaml) is
// predictable, so a repository (e.g. a fork PR) committing a file at that
// exact path shares an inode with the workspace copy. Without the O_EXCL
// guard, os.WriteFile's O_TRUNC would write the placeholder THROUGH the
// shared inode into the user's original repository file. The render must
// fail loudly AND the planted original must stay byte-identical.
func TestKustomizeKSOPSCompatRejectsPlantedGeneratedPathCollision(t *testing.T) {
	root := t.TempDir()
	writeKSOPSFixtureApp(t, root)
	planted := filepath.Join(root, "apps", "demo", ".drydock", "ksops", "000-000-000-demo-secret.sops.yaml")
	const plantedContent = "planted: repository content at drydock's predictable generated path\n"
	writeFile(t, planted, plantedContent)

	_, _, err := renderKSOPSFixture(t, root, true)
	if err == nil {
		t.Fatal("Render() error = nil, want generated-path collision error")
	}
	if !strings.Contains(err.Error(), "already exists in the source tree") {
		t.Fatalf("Render() error = %v, want generated-path collision error", err)
	}

	after, readErr := os.ReadFile(planted)
	if readErr != nil {
		t.Fatalf("read planted file after render attempt: %v", readErr)
	}
	if string(after) != plantedContent {
		t.Fatalf("planted repository file was mutated through the workspace hard link:\nbefore: %q\nafter:  %q", plantedContent, after)
	}
}

// TestKustomizeKSOPSCompatRenderDoesNotMutateOriginalTree is the baseline for
// the collision test above: a normal mode-ON render must leave every file in
// the original repository tree byte-identical — all generated-file writes
// happen in the temp workspace only.
func TestKustomizeKSOPSCompatRenderDoesNotMutateOriginalTree(t *testing.T) {
	root := t.TempDir()
	writeKSOPSFixtureApp(t, root)
	before := snapshotTree(t, root)

	if _, _, err := renderKSOPSFixture(t, root, true); err != nil {
		t.Fatalf("Render() error = %v", err)
	}

	after := snapshotTree(t, root)
	for rel, content := range before {
		got, ok := after[rel]
		if !ok {
			t.Errorf("render removed original file %q", rel)
			continue
		}
		if got != content {
			t.Errorf("render mutated original file %q", rel)
		}
	}
	for rel := range after {
		if _, ok := before[rel]; !ok {
			t.Errorf("render created %q in the original tree", rel)
		}
	}
}
