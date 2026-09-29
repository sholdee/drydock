package render

import (
	"context"
	"maps"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	goyaml "go.yaml.in/yaml/v3"
	"sigs.k8s.io/kustomize/api/builtins" //nolint:staticcheck // Deprecated for kustomize API v1, but its aliases are the only exported names of the builtin plugin structs.
	"sigs.k8s.io/kustomize/api/types"
	sigsyaml "sigs.k8s.io/yaml"
)

// These guards force a classification decision whenever a kustomize bump
// adds a field the render could read files through. The persistent render
// cache key is a model of every file a render reads: a kustomization field
// or builtin plugin config field that names a file the digest walk does not
// collect lets a cache entry outlive an edit to that file.

// kustomizationFieldClass says how the render-input digest accounts for one
// types.Kustomization field, or one key of a struct type such a field holds.
type kustomizationFieldClass string

const (
	// kustomizationFieldDigested names files the render reads; the digest
	// walk (kustomizeInputCollector.collectNode) collects them.
	kustomizationFieldDigested kustomizationFieldClass = "digested"
	// kustomizationFieldInline holds its whole value in the kustomization
	// file, which is always digested, and names no other file.
	kustomizationFieldInline kustomizationFieldClass = "inline"
	// kustomizationFieldRejected fails graph validation, so no render or
	// digest ever reads through it.
	kustomizationFieldRejected kustomizationFieldClass = "rejected"
	// kustomizationFieldMetadata identifies the kustomization document.
	kustomizationFieldMetadata kustomizationFieldClass = "metadata"
)

// kustomizationFieldCoverage classifies one types.Kustomization field or one
// nested key. A digested top-level field, and every rejected field or key,
// carries a fixture: a kustomization using it, the files it names, and
// either the paths the strict digest must then include (digested as required
// records, optional as present-or-absent ones) or the error it must fail
// with. Paths are relative to the kustomization directory.
type kustomizationFieldCoverage struct {
	class         kustomizationFieldClass
	kustomization string
	files         map[string]string
	digested      []string
	optional      []string
	wantErr       string
}

const fieldCoverageTestConfigMap = "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\n"

const fieldCoverageTestChart = "apiVersion: v2\nname: web\nversion: 0.1.0\n"

// kustomizationFieldCoverageTable maps every types.Kustomization field (Go
// name) to its classification.
var kustomizationFieldCoverageTable = map[string]kustomizationFieldCoverage{
	"TypeMeta": {class: kustomizationFieldMetadata},
	"MetaData": {class: kustomizationFieldMetadata},

	"NamePrefix":        {class: kustomizationFieldInline},
	"NameSuffix":        {class: kustomizationFieldInline},
	"Namespace":         {class: kustomizationFieldInline},
	"CommonLabels":      {class: kustomizationFieldInline},
	"Labels":            {class: kustomizationFieldInline},
	"CommonAnnotations": {class: kustomizationFieldInline},
	"Images":            {class: kustomizationFieldInline},
	"ImageTags":         {class: kustomizationFieldInline},
	"Replicas":          {class: kustomizationFieldInline},
	"Vars":              {class: kustomizationFieldInline},
	"SortOptions":       {class: kustomizationFieldInline},
	"GeneratorOptions":  {class: kustomizationFieldInline},
	"BuildMetadata":     {class: kustomizationFieldInline},

	"OpenAPI": {
		class:         kustomizationFieldDigested,
		kustomization: "openapi:\n  path: schema.json\n",
		files:         map[string]string{"schema.json": "{}\n"},
		digested:      []string{"schema.json"},
	},
	"PatchesStrategicMerge": {
		class:         kustomizationFieldDigested,
		kustomization: "resources:\n  - cm.yaml\npatchesStrategicMerge:\n  - smp.yaml\n",
		files:         map[string]string{"cm.yaml": fieldCoverageTestConfigMap, "smp.yaml": fieldCoverageTestConfigMap},
		digested:      []string{"smp.yaml"},
	},
	"PatchesJson6902": {
		class:         kustomizationFieldDigested,
		kustomization: "resources:\n  - cm.yaml\npatchesJson6902:\n  - path: ops.yaml\n    target:\n      version: v1\n      kind: ConfigMap\n      name: demo\n",
		files:         map[string]string{"cm.yaml": fieldCoverageTestConfigMap, "ops.yaml": "[]\n"},
		digested:      []string{"ops.yaml"},
	},
	"Patches": {
		class:         kustomizationFieldDigested,
		kustomization: "resources:\n  - cm.yaml\npatches:\n  - path: patch.yaml\n",
		files:         map[string]string{"cm.yaml": fieldCoverageTestConfigMap, "patch.yaml": fieldCoverageTestConfigMap},
		digested:      []string{"patch.yaml"},
	},
	"Replacements": {
		class:         kustomizationFieldDigested,
		kustomization: "resources:\n  - cm.yaml\nreplacements:\n  - path: replacement.yaml\n",
		files:         map[string]string{"cm.yaml": fieldCoverageTestConfigMap, "replacement.yaml": "source:\n  kind: ConfigMap\n  name: demo\ntargets: []\n"},
		digested:      []string{"replacement.yaml"},
	},
	"Resources": {
		class:         kustomizationFieldDigested,
		kustomization: "resources:\n  - cm.yaml\n",
		files:         map[string]string{"cm.yaml": fieldCoverageTestConfigMap},
		digested:      []string{"cm.yaml"},
	},
	"Components": {
		class:         kustomizationFieldDigested,
		kustomization: "components:\n  - ../component\n",
		files:         map[string]string{"../component/kustomization.yaml": "apiVersion: kustomize.config.k8s.io/v1alpha1\nkind: Component\n"},
		digested:      []string{"../component", "../component/kustomization.yaml"},
	},
	"Crds": {
		class:         kustomizationFieldDigested,
		kustomization: "crds:\n  - crd.yaml\n",
		files:         map[string]string{"crd.yaml": "apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\nmetadata:\n  name: widgets.example.com\n"},
		digested:      []string{"crd.yaml"},
	},
	"Bases": {
		class:         kustomizationFieldDigested,
		kustomization: "bases:\n  - ../base\n",
		files:         map[string]string{"../base/kustomization.yaml": "resources:\n  - cm.yaml\n", "../base/cm.yaml": fieldCoverageTestConfigMap},
		digested:      []string{"../base", "../base/kustomization.yaml", "../base/cm.yaml"},
	},
	// Kustomization-level generators read env: too: FixKustomization merges
	// it into envs: before building. Builtin generator configs never do.
	"ConfigMapGenerator": {
		class:         kustomizationFieldDigested,
		kustomization: "configMapGenerator:\n  - name: generated\n    files:\n      - key=data.txt\n    envs:\n      - a.env\n    env: b.env\n",
		files:         map[string]string{"data.txt": "data\n", "a.env": "A=a\n", "b.env": "B=b\n"},
		digested:      []string{"data.txt", "a.env", "b.env"},
	},
	"SecretGenerator": {
		class:         kustomizationFieldDigested,
		kustomization: "secretGenerator:\n  - name: generated\n    files:\n      - token.txt\n    envs:\n      - a.env\n    env: b.env\n",
		files:         map[string]string{"token.txt": "token\n", "a.env": "A=a\n", "b.env": "B=b\n"},
		digested:      []string{"token.txt", "a.env", "b.env"},
	},
	// helmGlobals.chartHome moves every local chart; configHome is rejected
	// by graph validation.
	"HelmGlobals": {
		class:         kustomizationFieldDigested,
		kustomization: "helmGlobals:\n  chartHome: vendor\nhelmCharts:\n  - name: web\n",
		files:         map[string]string{"vendor/web/Chart.yaml": fieldCoverageTestChart},
		digested:      []string{"vendor/web"},
	},
	// A chart with repo: and version: both set lives at name-version/name
	// under chartHome, optional because kustomize pulls a missing one.
	"HelmCharts": {
		class:         kustomizationFieldDigested,
		kustomization: "helmCharts:\n  - name: web\n    valuesFile: values.yaml\n    additionalValuesFiles:\n      - extra.yaml\n  - name: api\n    repo: https://charts.example.test\n    version: 1.2.3\n",
		files: map[string]string{
			"charts/web/Chart.yaml":           fieldCoverageTestChart,
			"charts/api-1.2.3/api/Chart.yaml": "apiVersion: v2\nname: api\nversion: 1.2.3\n",
			"values.yaml":                     "a: b\n",
			"extra.yaml":                      "c: d\n",
		},
		digested: []string{"charts/web", "values.yaml", "extra.yaml"},
		optional: []string{"charts/api-1.2.3/api"},
	},
	"HelmChartInflationGenerator": {
		class:         kustomizationFieldRejected,
		kustomization: "helmChartInflationGenerator:\n  - chartName: web\n",
		wantErr:       "helmChartInflationGenerator is deprecated and unsupported",
	},
	"Configurations": {
		class:         kustomizationFieldDigested,
		kustomization: "configurations:\n  - config.yaml\n",
		files:         map[string]string{"config.yaml": "nameReference: []\n"},
		digested:      []string{"config.yaml"},
	},
	"Generators": {
		class:         kustomizationFieldDigested,
		kustomization: "generators:\n  - generator.yaml\n",
		files: map[string]string{
			"generator.yaml": "apiVersion: builtin\nkind: ConfigMapGenerator\nmetadata:\n  name: generated\nfiles:\n  - generated.txt\n",
			"generated.txt":  "generated\n",
		},
		digested: []string{"generator.yaml", "generated.txt"},
	},
	"Transformers": {
		class:         kustomizationFieldDigested,
		kustomization: "transformers:\n  - transformer.yaml\n",
		files: map[string]string{
			"transformer.yaml": "apiVersion: builtin\nkind: PatchTransformer\nmetadata:\n  name: patch\npath: patch.yaml\n",
			"patch.yaml":       fieldCoverageTestConfigMap,
		},
		digested: []string{"transformer.yaml", "patch.yaml"},
	},
	"Validators": {
		class:         kustomizationFieldDigested,
		kustomization: "validators:\n  - validator.yaml\n",
		files: map[string]string{
			"validator.yaml":   "apiVersion: builtin\nkind: ReplacementTransformer\nmetadata:\n  name: validate\nreplacements:\n  - path: replacement.yaml\n",
			"replacement.yaml": "source:\n  kind: ConfigMap\n  name: demo\ntargets: []\n",
		},
		digested: []string{"validator.yaml", "replacement.yaml"},
	},
}

// TestKustomizationFieldsAreClassified fails when a kustomize bump adds a
// types.Kustomization field nobody has classified, or drops one the table
// still names.
func TestKustomizationFieldsAreClassified(t *testing.T) {
	kustomizationType := reflect.TypeFor[types.Kustomization]()
	fields := map[string]bool{}
	for field := range kustomizationType.Fields() {
		fields[field.Name] = true
		if _, ok := kustomizationFieldCoverageTable[field.Name]; !ok {
			t.Errorf("types.Kustomization.%s is unclassified: classify it in kustomizationFieldCoverageTable, and if the render reads files through it, collect them in kustomizeInputCollector.collectNode", field.Name)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(kustomizationFieldCoverageTable)) {
		if !fields[name] {
			t.Errorf("kustomizationFieldCoverageTable classifies %s, which types.Kustomization no longer has", name)
		}
	}
}

// TestKustomizationFieldClassificationsHoldForTheDigest checks each digested
// field's fixture against the strict digest walk, each rejected field's
// fixture against its failure, and each inline field, every leaf set to a
// path that exists, against a digest that collects nothing beyond the
// kustomization file.
func TestKustomizationFieldClassificationsHoldForTheDigest(t *testing.T) {
	kustomizationFields := map[string]reflect.StructField{}
	for field := range reflect.TypeFor[types.Kustomization]().Fields() {
		kustomizationFields[field.Name] = field
	}
	bare := bareKustomizationDigestPaths(t)
	for _, name := range slices.Sorted(maps.Keys(kustomizationFieldCoverageTable)) {
		coverage := kustomizationFieldCoverageTable[name]
		t.Run(name, func(t *testing.T) {
			switch coverage.class {
			case kustomizationFieldMetadata:
				assertNoCoverageFixture(t, coverage)
			case kustomizationFieldInline:
				assertNoCoverageFixture(t, coverage)
				if field, ok := kustomizationFields[name]; ok {
					assertInlineKustomizationFieldNamesNoFile(t, field, bare)
				}
			case kustomizationFieldDigested, kustomizationFieldRejected:
				assertKustomizationCoverageFixture(t, coverage)
			}
		})
	}
}

func assertNoCoverageFixture(t *testing.T, coverage kustomizationFieldCoverage) {
	t.Helper()
	if coverage.kustomization != "" || len(coverage.files) != 0 || len(coverage.digested) != 0 || len(coverage.optional) != 0 || coverage.wantErr != "" {
		t.Errorf("classified %s but carries a fixture", coverage.class)
	}
}

func fieldCoverageDigestPaths(rels []string) []string {
	out := make([]string, 0, len(rels))
	for _, rel := range rels {
		out = append(out, path.Join("apps/demo", rel))
	}
	return out
}

// assertKustomizationCoverageFixture writes a digested or rejected fixture
// under apps/demo and checks it against the strict digest walk.
func assertKustomizationCoverageFixture(t *testing.T, coverage kustomizationFieldCoverage) {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "apps", "demo")
	writeFile(t, filepath.Join(dir, "kustomization.yaml"), coverage.kustomization)
	for file, content := range coverage.files {
		writeFile(t, filepath.Join(dir, filepath.FromSlash(file)), content)
	}
	if coverage.class == kustomizationFieldRejected {
		_, err := KustomizeInputDigestPaths(context.Background(), ResolvedSource{RepoRoot: root, Path: "apps/demo"}, RenderOptions{})
		if coverage.wantErr == "" || err == nil || !strings.Contains(err.Error(), coverage.wantErr) {
			t.Fatalf("KustomizeInputDigestPaths() error = %v, want %q", err, coverage.wantErr)
		}
		return
	}
	if coverage.kustomization == "" || len(coverage.digested)+len(coverage.optional) == 0 {
		t.Fatal("classified digested but carries no fixture")
	}
	paths := pluginDigestPaths(t, root, "apps/demo")
	assertRequiredDigestPaths(t, paths, fieldCoverageDigestPaths(coverage.digested)...)
	for _, rel := range fieldCoverageDigestPaths(coverage.optional) {
		if optional, ok := paths[rel]; !ok || !optional {
			t.Errorf("digest paths %v lack the optional record %q", paths, rel)
		}
	}
}

// bareKustomizationDigestPaths is what the strict digest collects for a
// kustomization that uses no field: its own file records.
func bareKustomizationDigestPaths(t *testing.T) map[string]bool {
	t.Helper()
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "apps", "demo", "kustomization.yaml"), "apiVersion: kustomize.config.k8s.io/v1beta1\nkind: Kustomization\n")
	return pluginDigestPaths(t, root, "apps/demo")
}

// assertInlineKustomizationFieldNamesNoFile sets every leaf of an inline
// field to referent.yaml, which exists next to the kustomization; the strict
// digest must still collect only what it collects for a bare kustomization.
func assertInlineKustomizationFieldNamesNoFile(t *testing.T, field reflect.StructField, bare map[string]bool) {
	t.Helper()
	key, _, _ := strings.Cut(field.Tag.Get("json"), ",")
	if key == "" {
		key = field.Name
	}
	content := marshalReferentDocument(t, map[string]any{key: referentFilledValue(field.Type).Interface()})
	root := t.TempDir()
	dir := filepath.Join(root, "apps", "demo")
	writeFile(t, filepath.Join(dir, "kustomization.yaml"), content)
	writeFile(t, filepath.Join(dir, "referent.yaml"), fieldCoverageTestConfigMap)
	paths, err := KustomizeInputDigestPaths(context.Background(), ResolvedSource{RepoRoot: root, Path: "apps/demo"}, RenderOptions{})
	if err != nil {
		t.Fatalf("inline %s: KustomizeInputDigestPaths() error = %v\n%s", key, err, content)
	}
	got := make(map[string]bool, len(paths))
	for _, digestPath := range paths {
		got[digestPath.Path] = digestPath.Optional
	}
	if !maps.Equal(got, bare) {
		t.Errorf("inline %s: digest paths %v, want only the kustomization file records %v\n%s", key, got, bare, content)
	}
}

// referentFilledValue returns a value of typ with every string leaf set to
// referent.yaml, every other scalar non-zero, every pointer allocated and
// every slice and map holding one element, recursively, so a document built
// from it sets every key the type decodes to a would-be path. A struct type
// recurring inside itself is left zero at the recurrence.
func referentFilledValue(typ reflect.Type) reflect.Value {
	return fillReferentValue(typ, map[reflect.Type]bool{})
}

func fillReferentValue(typ reflect.Type, filling map[reflect.Type]bool) reflect.Value {
	value := reflect.New(typ).Elem()
	if fillReferentScalar(value) {
		return value
	}
	switch kind := typ.Kind(); {
	case kind == reflect.Pointer:
		pointer := reflect.New(typ.Elem())
		pointer.Elem().Set(fillReferentValue(typ.Elem(), filling))
		value.Set(pointer)
	case kind == reflect.Slice:
		value.Set(reflect.Append(value, fillReferentValue(typ.Elem(), filling)))
	case kind == reflect.Array:
		for i := range typ.Len() {
			value.Index(i).Set(fillReferentValue(typ.Elem(), filling))
		}
	case kind == reflect.Map:
		value.Set(reflect.MakeMapWithSize(typ, 1))
		value.SetMapIndex(fillReferentValue(typ.Key(), filling), fillReferentValue(typ.Elem(), filling))
	case kind == reflect.Struct && !filling[typ]:
		filling[typ] = true
		defer delete(filling, typ)
		for i := range typ.NumField() {
			if typ.Field(i).IsExported() {
				value.Field(i).Set(fillReferentValue(typ.Field(i).Type, filling))
			}
		}
	}
	return value
}

// fillReferentScalar sets value when it is a scalar, or an interface a
// string satisfies, and reports whether it was.
func fillReferentScalar(value reflect.Value) bool {
	switch {
	case value.Kind() == reflect.String:
		value.SetString("referent.yaml")
	case value.Kind() == reflect.Bool:
		value.SetBool(true)
	case value.CanInt():
		value.SetInt(1)
	case value.CanUint():
		value.SetUint(1)
	case value.CanFloat():
		value.SetFloat(1)
	case value.Kind() == reflect.Interface && reflect.TypeFor[string]().Implements(value.Type()):
		value.Set(reflect.ValueOf("referent.yaml"))
	default:
		return false
	}
	return true
}

func marshalReferentDocument(t *testing.T, document map[string]any) string {
	t.Helper()
	content, err := sigsyaml.Marshal(document)
	if err != nil {
		t.Fatalf("marshal %#v: %v", document, err)
	}
	return string(content)
}

// builtinPluginFieldClass classifies one JSON key a builtin plugin's Config
// decodes. The zero value is inline: the value lives in the config document
// and the plugin reads no file through it.
type builtinPluginFieldClass struct {
	referent bool
	// fragment sets the field so that it names referent.yaml; field is the
	// Field kustomizePluginConfigRefs reports for it.
	fragment string
	field    string
}

var pluginFieldInline = builtinPluginFieldClass{}

func pluginFieldReferent(fragment, field string) builtinPluginFieldClass {
	return builtinPluginFieldClass{referent: true, fragment: fragment, field: field}
}

// builtinPluginFieldCoverageTable classifies every JSON key of every exported
// builtin plugin struct (the sigs.k8s.io/kustomize/api/builtins aliases). A
// field is a referent when the plugin reads the file it names through the
// loader. A builtin kind kustomize adds later has no extractor, so its
// configs already fail closed as unknown; this table catches the silent
// drift, a new field on a kind drydock enumerates.
var builtinPluginFieldCoverageTable = []struct {
	plugin reflect.Type
	// rejected is the error every config of the kind fails with; its
	// fields stay unclassified because no such config keys a cache entry.
	rejected string
	fields   map[string]builtinPluginFieldClass
}{
	{plugin: reflect.TypeFor[builtins.AnnotationsTransformerPlugin](), fields: map[string]builtinPluginFieldClass{
		"annotations": pluginFieldInline,
		"fieldSpecs":  pluginFieldInline,
	}},
	{plugin: reflect.TypeFor[builtins.ConfigMapGeneratorPlugin](), fields: kvGeneratorPluginFieldClasses(nil)},
	{plugin: reflect.TypeFor[builtins.HashTransformerPlugin](), fields: map[string]builtinPluginFieldClass{}},
	{plugin: reflect.TypeFor[builtins.HelmChartInflationGeneratorPlugin](), rejected: "builtin HelmChartInflationGenerator is unsupported"},
	{plugin: reflect.TypeFor[builtins.IAMPolicyGeneratorPlugin](), fields: map[string]builtinPluginFieldClass{
		"cloud":             pluginFieldInline,
		"kubernetesService": pluginFieldInline,
		"serviceAccount":    pluginFieldInline,
	}},
	{plugin: reflect.TypeFor[builtins.ImageTagTransformerPlugin](), fields: map[string]builtinPluginFieldClass{
		"imageTag":   pluginFieldInline,
		"fieldSpecs": pluginFieldInline,
	}},
	{plugin: reflect.TypeFor[builtins.LabelTransformerPlugin](), fields: map[string]builtinPluginFieldClass{
		"labels":     pluginFieldInline,
		"fieldSpecs": pluginFieldInline,
	}},
	{plugin: reflect.TypeFor[builtins.NamespaceTransformerPlugin](), fields: map[string]builtinPluginFieldClass{
		"metadata":               pluginFieldInline,
		"fieldSpecs":             pluginFieldInline,
		"unsetOnly":              pluginFieldInline,
		"setRoleBindingSubjects": pluginFieldInline,
	}},
	{plugin: reflect.TypeFor[builtins.PatchJson6902TransformerPlugin](), fields: map[string]builtinPluginFieldClass{
		"target": pluginFieldInline,
		"jsonOp": pluginFieldInline,
		"path":   pluginFieldReferent("path: referent.yaml\n", "path"),
	}},
	{plugin: reflect.TypeFor[builtins.PatchStrategicMergeTransformerPlugin](), fields: map[string]builtinPluginFieldClass{
		"patches": pluginFieldInline,
		"paths":   pluginFieldReferent("paths:\n  - referent.yaml\n", "paths"),
	}},
	{plugin: reflect.TypeFor[builtins.PatchTransformerPlugin](), fields: map[string]builtinPluginFieldClass{
		"patch":   pluginFieldInline,
		"target":  pluginFieldInline,
		"options": pluginFieldInline,
		"path":    pluginFieldReferent("path: referent.yaml\n", "path"),
	}},
	{plugin: reflect.TypeFor[builtins.PrefixTransformerPlugin](), fields: map[string]builtinPluginFieldClass{
		"prefix":     pluginFieldInline,
		"fieldSpecs": pluginFieldInline,
	}},
	{plugin: reflect.TypeFor[builtins.SuffixTransformerPlugin](), fields: map[string]builtinPluginFieldClass{
		"suffix":     pluginFieldInline,
		"fieldSpecs": pluginFieldInline,
	}},
	{plugin: reflect.TypeFor[builtins.ReplacementTransformerPlugin](), fields: map[string]builtinPluginFieldClass{
		// Each entry is either a path: to load or an inline replacement.
		"replacements": pluginFieldReferent("replacements:\n  - path: referent.yaml\n", "replacements.path"),
	}},
	{plugin: reflect.TypeFor[builtins.ReplicaCountTransformerPlugin](), fields: map[string]builtinPluginFieldClass{
		"replica":    pluginFieldInline,
		"fieldSpecs": pluginFieldInline,
	}},
	{plugin: reflect.TypeFor[builtins.SecretGeneratorPlugin](), fields: kvGeneratorPluginFieldClasses(map[string]builtinPluginFieldClass{
		"type": pluginFieldInline,
	})},
	{plugin: reflect.TypeFor[builtins.ValueAddTransformerPlugin](), fields: map[string]builtinPluginFieldClass{
		// An empty value defaults to the base name of the listing
		// directory: a path the source path pins, not a file read.
		"value":          pluginFieldInline,
		"targets":        pluginFieldInline,
		"targetFilePath": pluginFieldReferent("targetFilePath: referent.yaml\n", "targetFilePath"),
	}},
}

// kvGeneratorPluginFieldClasses classifies the fields ConfigMapGenerator and
// SecretGenerator share (metadata plus the flattened GeneratorArgs and
// KvPairSources), merged with extra.
func kvGeneratorPluginFieldClasses(extra map[string]builtinPluginFieldClass) map[string]builtinPluginFieldClass {
	fields := map[string]builtinPluginFieldClass{
		"metadata":  pluginFieldInline,
		"name":      pluginFieldInline,
		"namespace": pluginFieldInline,
		"behavior":  pluginFieldInline,
		"options":   pluginFieldInline,
		"literals":  pluginFieldInline,
		// kv.Load reads envs: alone; only FixKustomization merges env:
		// into envs:, for kustomization-level generators. A builtin
		// config's env: file is never read.
		"env":   pluginFieldInline,
		"files": pluginFieldReferent("files:\n  - key=referent.yaml\n", "files"),
		"envs":  pluginFieldReferent("envs:\n  - referent.yaml\n", "envs"),
	}
	maps.Copy(fields, extra)
	return fields
}

// builtinPluginFieldClasses returns the classified fields of a builtin
// plugin struct, nil when the table does not classify its fields.
func builtinPluginFieldClasses(plugin reflect.Type) map[string]builtinPluginFieldClass {
	for _, entry := range builtinPluginFieldCoverageTable {
		if entry.plugin == plugin {
			return entry.fields
		}
	}
	return nil
}

// builtinPluginCompositeKinds are builtin kinds krusty configures as several
// plugins at once, each decoding the same config bytes (builtinhelpers
// NewMultiTransformer).
var builtinPluginCompositeKinds = map[string][]string{
	"PrefixSuffixTransformer": {"PrefixTransformer", "SuffixTransformer"},
}

func builtinPluginKind(plugin reflect.Type) string {
	return strings.TrimSuffix(plugin.Name(), "Plugin")
}

// decodedJSONField is one JSON key a struct decodes.
type decodedJSONField struct {
	index []int
	typ   reflect.Type
}

// decodedJSONFields returns the JSON keys a struct decodes the way
// encoding/json (and so sigs.k8s.io/yaml, the plugins' decoder) does:
// exported fields by their json name, anonymous struct fields without a json
// name flattened into their parent. Ambiguous keys fail the test rather than
// guess at json's dominance rules.
func decodedJSONFields(t *testing.T, structType reflect.Type) map[string]decodedJSONField {
	t.Helper()
	fields := map[string]decodedJSONField{}
	var walk func(reflect.Type, []int)
	walk = func(walked reflect.Type, prefix []int) {
		for i := range walked.NumField() {
			field := walked.Field(i)
			name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
			if name == "-" {
				continue
			}
			index := append(slices.Clone(prefix), i)
			if field.Anonymous && name == "" {
				embedded := field.Type
				if embedded.Kind() == reflect.Pointer {
					embedded = embedded.Elem()
				}
				if embedded.Kind() == reflect.Struct {
					walk(embedded, index)
					continue
				}
			}
			if !field.IsExported() {
				continue
			}
			if name == "" {
				name = field.Name
			}
			if _, dup := fields[name]; dup {
				t.Fatalf("%s decodes JSON key %q from more than one field", structType, name)
			}
			fields[name] = decodedJSONField{index: index, typ: field.Type}
		}
	}
	walk(structType, nil)
	return fields
}

// TestBuiltinPluginConfigFieldsAreClassified fails when a kustomize bump adds
// a builtin plugin config field nobody has classified, or drops one the table
// still names.
func TestBuiltinPluginConfigFieldsAreClassified(t *testing.T) {
	for _, entry := range builtinPluginFieldCoverageTable {
		kind := builtinPluginKind(entry.plugin)
		if entry.rejected != "" {
			if entry.fields != nil {
				t.Errorf("%s is rejected outright; its fields need no classification", kind)
			}
			continue
		}
		fields := decodedJSONFields(t, entry.plugin)
		for _, name := range slices.Sorted(maps.Keys(fields)) {
			if _, ok := entry.fields[name]; !ok {
				t.Errorf("builtin %s config field %q is unclassified: classify it in builtinPluginFieldCoverageTable, and if the plugin reads a file through it, extract it in builtinPluginReferentExtractors", kind, name)
			}
		}
		for _, name := range slices.Sorted(maps.Keys(entry.fields)) {
			if _, ok := fields[name]; !ok {
				t.Errorf("builtinPluginFieldCoverageTable classifies %s field %q, which %s no longer decodes", kind, name, entry.plugin.Name())
			}
		}
	}
}

// TestBuiltinPluginFieldCoverageMatchesExtractors keeps the kind sets of the
// coverage table and builtinPluginReferentExtractors identical: every kind
// the parser enumerates is classified field by field here, and every
// classified kind is enumerated rather than rejected as unknown.
func TestBuiltinPluginFieldCoverageMatchesExtractors(t *testing.T) {
	classified := map[string]bool{}
	for _, entry := range builtinPluginFieldCoverageTable {
		kind := builtinPluginKind(entry.plugin)
		if entry.rejected != "" {
			if _, ok := builtinPluginReferentExtractors[kind]; ok {
				t.Errorf("rejected builtin %s has an extractor", kind)
			}
			continue
		}
		classified[kind] = true
	}
	for kind := range builtinPluginCompositeKinds {
		classified[kind] = true
	}
	for _, kind := range slices.Sorted(maps.Keys(builtinPluginReferentExtractors)) {
		if !classified[kind] {
			t.Errorf("builtinPluginReferentExtractors enumerates %s, which builtinPluginFieldCoverageTable does not classify", kind)
		}
	}
	for _, kind := range slices.Sorted(maps.Keys(classified)) {
		if _, ok := builtinPluginReferentExtractors[kind]; !ok {
			t.Errorf("builtin %s is classified but missing from builtinPluginReferentExtractors", kind)
		}
	}
}

// builtinPluginCoverageCase is one plugin struct whose fields a config of
// kind decodes. A composite kind yields one case per part.
type builtinPluginCoverageCase struct {
	kind   string
	plugin reflect.Type
	fields map[string]builtinPluginFieldClass
}

func builtinPluginCoverageCases(t *testing.T) []builtinPluginCoverageCase {
	t.Helper()
	byKind := map[string]builtinPluginCoverageCase{}
	var cases []builtinPluginCoverageCase
	for _, entry := range builtinPluginFieldCoverageTable {
		if entry.rejected != "" {
			continue
		}
		c := builtinPluginCoverageCase{kind: builtinPluginKind(entry.plugin), plugin: entry.plugin, fields: entry.fields}
		byKind[c.kind] = c
		cases = append(cases, c)
	}
	for _, kind := range slices.Sorted(maps.Keys(builtinPluginCompositeKinds)) {
		for _, part := range builtinPluginCompositeKinds[kind] {
			c, ok := byKind[part]
			if !ok {
				t.Fatalf("composite %s part %s is not classified", kind, part)
			}
			c.kind = kind
			cases = append(cases, c)
		}
	}
	return cases
}

func builtinPluginTestDocument(kind, fragment string) string {
	return "apiVersion: builtin\nkind: " + kind + "\nmetadata:\n  name: coverage\n" + fragment
}

// builtinPluginFilledDocument is a config of kind whose key holds a
// referent-filled value of typ; a filled metadata key replaces the default.
func builtinPluginFilledDocument(t *testing.T, kind, key string, typ reflect.Type) string {
	t.Helper()
	document := map[string]any{
		"apiVersion": "builtin",
		"kind":       kind,
		"metadata":   map[string]any{"name": "coverage"},
	}
	document[key] = referentFilledValue(typ).Interface()
	return marshalReferentDocument(t, document)
}

// assertPluginDecodesField pins a fixture to the plugin itself: kustomize's
// own struct must see the field set, so a fragment cannot pass by sharing a
// misspelled key with the extractor.
func assertPluginDecodesField(t *testing.T, plugin reflect.Type, field decodedJSONField, doc string) {
	t.Helper()
	value := reflect.New(plugin)
	if err := sigsyaml.Unmarshal([]byte(doc), value.Interface()); err != nil {
		t.Fatalf("decode %s: %v\n%s", plugin.Name(), err, doc)
	}
	decoded, err := value.Elem().FieldByIndexErr(field.index)
	if err != nil || decoded.IsZero() {
		t.Fatalf("%s leaves the field unset for\n%s", plugin.Name(), doc)
	}
}

func pluginConfigRefsForDocument(t *testing.T, doc string) []kustomizePluginConfigRef {
	t.Helper()
	refs, err := kustomizePluginConfigRefs(decodePluginConfigTestDocs(t, doc))
	if err != nil {
		t.Fatalf("kustomizePluginConfigRefs() error = %v\n%s", err, doc)
	}
	return refs
}

// TestBuiltinPluginFieldClassificationsHoldForTheParser checks every
// classification against kustomizePluginConfigRefs: a referent field naming
// referent.yaml comes back as exactly that referent, an inline field with
// every leaf of its value set to the same path comes back as nothing, and so
// does a bare config of each kind. A rejected kind fails.
func TestBuiltinPluginFieldClassificationsHoldForTheParser(t *testing.T) {
	for _, entry := range builtinPluginFieldCoverageTable {
		if entry.rejected == "" {
			continue
		}
		kind := builtinPluginKind(entry.plugin)
		_, err := kustomizePluginConfigRefs(decodePluginConfigTestDocs(t, builtinPluginTestDocument(kind, "")))
		if err == nil || !strings.Contains(err.Error(), entry.rejected) {
			t.Errorf("kustomizePluginConfigRefs(%s) error = %v, want %q", kind, err, entry.rejected)
		}
	}
	for _, c := range builtinPluginCoverageCases(t) {
		t.Run(c.kind+"/"+c.plugin.Name(), func(t *testing.T) {
			if refs := pluginConfigRefsForDocument(t, builtinPluginTestDocument(c.kind, "")); len(refs) != 0 {
				t.Errorf("bare %s config refs = %#v, want none", c.kind, refs)
			}
			fields := decodedJSONFields(t, c.plugin)
			for _, name := range slices.Sorted(maps.Keys(c.fields)) {
				field, ok := fields[name]
				if !ok {
					continue // TestBuiltinPluginConfigFieldsAreClassified reports it.
				}
				if class := c.fields[name]; class.referent {
					assertReferentFieldEnumerated(t, c, name, class, field)
				} else {
					assertInlineFieldNotEnumerated(t, c, name, field)
				}
			}
		})
	}
}

func assertReferentFieldEnumerated(t *testing.T, c builtinPluginCoverageCase, name string, class builtinPluginFieldClass, field decodedJSONField) {
	t.Helper()
	doc := builtinPluginTestDocument(c.kind, class.fragment)
	assertPluginDecodesField(t, c.plugin, field, doc)
	refs, err := kustomizePluginConfigRefs(decodePluginConfigTestDocs(t, doc))
	want := []kustomizePluginConfigRef{{Kind: c.kind, Field: class.field, Path: "referent.yaml"}}
	if err != nil || !slices.Equal(refs, want) {
		t.Errorf("referent %s field %q: kustomizePluginConfigRefs() = %#v, %v; want %#v", c.kind, name, refs, err, want)
	}
}

// assertInlineFieldNotEnumerated fills an inline field's whole value with
// would-be paths; the parser must report none of them.
func assertInlineFieldNotEnumerated(t *testing.T, c builtinPluginCoverageCase, name string, field decodedJSONField) {
	t.Helper()
	doc := builtinPluginFilledDocument(t, c.kind, name, field.typ)
	assertPluginDecodesField(t, c.plugin, field, doc)
	refs, err := kustomizePluginConfigRefs(decodePluginConfigTestDocs(t, doc))
	if err != nil || len(refs) != 0 {
		t.Errorf("inline %s field %q: kustomizePluginConfigRefs() = %#v, %v; want no referents and no error\n%s", c.kind, name, refs, err, doc)
	}
}

// nestedFieldDigested marks a nested key the render reads files through. It
// carries no fixture of its own: a digested kustomizationFieldCoverageTable
// fixture must set it, and every builtin plugin config key reaching its type
// must be a referent.
var nestedFieldDigested = kustomizationFieldCoverage{class: kustomizationFieldDigested}

// nestedFields classifies the keys of one nested struct type: the given
// non-inline ones, and the inline ones.
func nestedFields(classified map[string]kustomizationFieldCoverage, inline ...string) map[string]kustomizationFieldCoverage {
	fields := make(map[string]kustomizationFieldCoverage, len(classified)+len(inline))
	maps.Copy(fields, classified)
	for _, key := range inline {
		fields[key] = kustomizationFieldCoverage{class: kustomizationFieldInline}
	}
	return fields
}

// kvGeneratorArgsDigested classifies the file keys of kustomization-level
// generators: files:, envs:, and env:, which FixKustomization merges into
// envs:.
func kvGeneratorArgsDigested() map[string]kustomizationFieldCoverage {
	return map[string]kustomizationFieldCoverage{
		"files": nestedFieldDigested,
		"envs":  nestedFieldDigested,
		"env":   nestedFieldDigested,
	}
}

// helmChartKeyRejected is a helmCharts key graph validation rejects.
func helmChartKeyRejected(fragment, wantErr string) kustomizationFieldCoverage {
	return kustomizationFieldCoverage{
		class:         kustomizationFieldRejected,
		kustomization: "helmCharts:\n  - name: web\n" + fragment,
		files:         map[string]string{"charts/web/Chart.yaml": fieldCoverageTestChart},
		wantErr:       wantErr,
	}
}

// kustomizeNestedFieldCoverageTable classifies every JSON key of every struct
// type a classified types.Kustomization field or builtin plugin config key
// holds, directly or through pointers, slices, maps, arrays and other such
// types' keys; nothing below a rejected field or kind is classified. Types
// are keyed by reflect.Type.String(), so builtins.Target and types.Target
// stay apart. Keys are JSON keys, embedded structs flattened into their
// holder as encoding/json decodes them: GeneratorArgs, KvPairSources,
// Replacement, ResId and Gvk have no rows of their own.
//
// A classification holds wherever its type is reached. Builtin
// ConfigMapGenerator and SecretGenerator configs flatten ConfigMapArgs and
// SecretArgs into their own keys, classified in
// builtinPluginFieldCoverageTable (where env: is inline), so the two types
// below are reached only from kustomization-level generators.
var kustomizeNestedFieldCoverageTable = map[string]map[string]kustomizationFieldCoverage{
	// fieldPath and filePathPosition address the field value to edit.
	"builtins.Target":         nestedFields(nil, "selector", "fieldPath", "filePathPosition"),
	"types.ConfigMapArgs":     nestedFields(kvGeneratorArgsDigested(), "name", "namespace", "behavior", "literals", "options"),
	"types.SecretArgs":        nestedFields(kvGeneratorArgsDigested(), "name", "namespace", "behavior", "literals", "options", "type"),
	"types.FieldOptions":      nestedFields(nil, "delimiter", "index", "encoding", "create"),
	"types.FieldSelector":     nestedFields(nil, "fieldPath"),
	"types.FieldSpec":         nestedFields(nil, "group", "version", "kind", "path", "create"),
	"types.GeneratorOptions":  nestedFields(nil, "labels", "annotations", "disableNameSuffixHash", "immutable"),
	"types.Image":             nestedFields(nil, "name", "newName", "newTag", "digest", "tagSuffix"),
	"types.KubernetesService": nestedFields(nil, "name", "namespace"),
	"types.Label":             nestedFields(nil, "pairs", "includeSelectors", "includeTemplates", "fields"),
	"types.LegacySortOptions": nestedFields(nil, "orderFirst", "orderLast"),
	"types.ObjectMeta":        nestedFields(nil, "name", "namespace", "labels", "annotations"),
	"types.PatchArgs":         nestedFields(nil, "allowNameChange", "allowKindChange"),
	"types.Replica":           nestedFields(nil, "name", "count"),
	"types.Selector":          nestedFields(nil, "group", "version", "kind", "name", "namespace", "annotationSelector", "labelSelector"),
	"types.ServiceAccount":    nestedFields(nil, "name", "projectId"),
	"types.SortOptions":       nestedFields(nil, "order", "legacySortOptions"),
	"types.SourceSelector":    nestedFields(nil, "group", "version", "kind", "name", "namespace", "fieldPath", "options"),
	"types.Target":            nestedFields(nil, "apiVersion", "group", "version", "kind", "name", "namespace"),
	"types.TargetSelector":    nestedFields(nil, "select", "reject", "fieldPaths", "options"),
	"types.TypeMeta":          nestedFields(nil, "apiVersion", "kind"),
	"types.Var":               nestedFields(nil, "name", "objref", "fieldref"),

	// name, and with repo and version both set name-version/name, locates
	// the chart directory under chartHome.
	"types.HelmChart": nestedFields(map[string]kustomizationFieldCoverage{
		"name":                  nestedFieldDigested,
		"repo":                  nestedFieldDigested,
		"version":               nestedFieldDigested,
		"valuesFile":            nestedFieldDigested,
		"additionalValuesFiles": nestedFieldDigested,
		"nameTemplate":          helmChartKeyRejected("    nameTemplate: web\n", "helmCharts.nameTemplate is unsupported"),
		"devel":                 helmChartKeyRejected("    devel: true\n", "helmCharts.devel is unsupported"),
		"debug":                 helmChartKeyRejected("    debug: true\n", "helmCharts.debug is unsupported"),
	}, "releaseName", "namespace", "valuesInline", "valuesMerge", "includeCRDs", "skipHooks", "apiVersions", "kubeVersion", "skipTests"),
	"types.HelmGlobals": nestedFields(map[string]kustomizationFieldCoverage{
		"chartHome": nestedFieldDigested,
		"configHome": {
			class:         kustomizationFieldRejected,
			kustomization: "helmGlobals:\n  configHome: helm-config\n",
			wantErr:       "helmGlobals.configHome is unsupported",
		},
	}),
	"types.Patch": nestedFields(map[string]kustomizationFieldCoverage{
		"path": nestedFieldDigested,
	}, "patch", "target", "options"),
	// Each entry is either a path: to load or an inline replacement.
	"types.ReplacementField": nestedFields(map[string]kustomizationFieldCoverage{
		"path": nestedFieldDigested,
	}, "source", "sourceValue", "targets"),
}

// nestedFieldOrigin is a top-level key reaching a nested struct type: a
// types.Kustomization field (Go name, plugin nil) or a builtin plugin config
// key.
type nestedFieldOrigin struct {
	plugin reflect.Type
	key    string
}

func (o nestedFieldOrigin) String() string {
	if o.plugin == nil {
		return "types.Kustomization." + o.key
	}
	return "builtin " + builtinPluginKind(o.plugin) + " " + o.key
}

// nestedStructType is one reachable struct type and the top-level keys that
// reach it.
type nestedStructType struct {
	typ     reflect.Type
	origins []nestedFieldOrigin
}

func (n *nestedStructType) originList() string {
	names := make([]string, 0, len(n.origins))
	for _, origin := range n.origins {
		names = append(names, origin.String())
	}
	slices.Sort(names)
	return strings.Join(names, ", ")
}

// reachableNestedStructTypes returns, by reflect.Type.String(), every struct
// type reachable from a types.Kustomization field or a builtin plugin config
// key, skipping rejected fields and kinds.
func reachableNestedStructTypes(t *testing.T) map[string]*nestedStructType {
	t.Helper()
	reached := map[string]*nestedStructType{}
	for field := range reflect.TypeFor[types.Kustomization]().Fields() {
		if kustomizationFieldCoverageTable[field.Name].class == kustomizationFieldRejected {
			continue
		}
		collectNestedStructTypes(t, field.Type, nestedFieldOrigin{key: field.Name}, reached, map[reflect.Type]bool{})
	}
	for _, entry := range builtinPluginFieldCoverageTable {
		if entry.rejected != "" {
			continue
		}
		for key, field := range decodedJSONFields(t, entry.plugin) {
			collectNestedStructTypes(t, field.typ, nestedFieldOrigin{plugin: entry.plugin, key: key}, reached, map[reflect.Type]bool{})
		}
	}
	return reached
}

func collectNestedStructTypes(t *testing.T, typ reflect.Type, origin nestedFieldOrigin, reached map[string]*nestedStructType, visited map[reflect.Type]bool) {
	t.Helper()
	switch kind := typ.Kind(); {
	case kind == reflect.Map:
		collectNestedStructTypes(t, typ.Key(), origin, reached, visited)
		collectNestedStructTypes(t, typ.Elem(), origin, reached, visited)
		return
	case kind == reflect.Pointer || kind == reflect.Slice || kind == reflect.Array:
		collectNestedStructTypes(t, typ.Elem(), origin, reached, visited)
		return
	case kind != reflect.Struct || visited[typ]:
		return
	}
	visited[typ] = true
	name := typ.String()
	nested, ok := reached[name]
	if !ok {
		nested = &nestedStructType{typ: typ}
		reached[name] = nested
	} else if nested.typ != typ {
		t.Fatalf("%s and %s both print as %s: key kustomizeNestedFieldCoverageTable by a unique name", nested.typ.PkgPath(), typ.PkgPath(), name)
	}
	nested.origins = append(nested.origins, origin)
	for _, field := range decodedJSONFields(t, typ) {
		collectNestedStructTypes(t, field.typ, origin, reached, visited)
	}
}

// TestKustomizeNestedFieldsAreClassified fails when a kustomize bump adds a
// key to a struct type a kustomization or builtin plugin config holds, or a
// whole new such type, that nobody has classified, or drops one the table
// still names.
func TestKustomizeNestedFieldsAreClassified(t *testing.T) {
	reached := reachableNestedStructTypes(t)
	for _, typeName := range slices.Sorted(maps.Keys(reached)) {
		nested := reached[typeName]
		rows := kustomizeNestedFieldCoverageTable[typeName]
		fields := decodedJSONFields(t, nested.typ)
		for _, key := range slices.Sorted(maps.Keys(fields)) {
			if _, ok := rows[key]; !ok {
				t.Errorf("%s field %q (reached through %s) is unclassified: classify it in kustomizeNestedFieldCoverageTable, and if the render reads files through it, collect them in kustomizeInputCollector.collectNode, or for a builtin plugin config extract them in builtinPluginReferentExtractors", typeName, key, nested.originList())
			}
		}
		for _, key := range slices.Sorted(maps.Keys(rows)) {
			if _, ok := fields[key]; !ok {
				t.Errorf("kustomizeNestedFieldCoverageTable classifies %s field %q, which %s no longer decodes", typeName, key, typeName)
			}
		}
	}
	for _, typeName := range slices.Sorted(maps.Keys(kustomizeNestedFieldCoverageTable)) {
		if _, ok := reached[typeName]; !ok {
			t.Errorf("kustomizeNestedFieldCoverageTable classifies %s, which no classified kustomization field or builtin plugin config key reaches any more", typeName)
		}
	}
}

// TestKustomizeNestedFieldClassificationsHold checks each nested
// classification: a digested key is set by a digested
// kustomizationFieldCoverageTable fixture, which pins its digest, and is a
// referent wherever a builtin plugin config reaches it; a rejected key fails
// the strict digest with its error and is out of reach of builtin plugin
// configs, which graph validation never sees; an inline key carries no
// fixture.
func TestKustomizeNestedFieldClassificationsHold(t *testing.T) {
	reached := reachableNestedStructTypes(t)
	exercised := digestedFixtureSetKeys(t)
	for _, typeName := range slices.Sorted(maps.Keys(kustomizeNestedFieldCoverageTable)) {
		nested, ok := reached[typeName]
		if !ok {
			continue // TestKustomizeNestedFieldsAreClassified reports it.
		}
		rows := kustomizeNestedFieldCoverageTable[typeName]
		for _, key := range slices.Sorted(maps.Keys(rows)) {
			checkNestedFieldCoverage(t, typeName, key, rows[key], nested, exercised)
		}
	}
}

func checkNestedFieldCoverage(t *testing.T, typeName, key string, coverage kustomizationFieldCoverage, nested *nestedStructType, exercised map[string]bool) {
	t.Helper()
	name := typeName + "." + key
	switch coverage.class {
	case kustomizationFieldInline, kustomizationFieldMetadata:
		if coverage.kustomization != "" || coverage.wantErr != "" {
			t.Errorf("%s is %s but carries a fixture", name, coverage.class)
		}
	case kustomizationFieldDigested:
		if coverage.kustomization != "" || coverage.wantErr != "" {
			t.Errorf("%s is digested but carries a fixture: set it in the fixture of the kustomizationFieldCoverageTable field that reaches it", name)
		}
		if !exercised[name] {
			t.Errorf("%s is digested but no digested kustomizationFieldCoverageTable fixture sets it: set it in the fixture of the field that reaches it (%s)", name, nested.originList())
		}
		for _, origin := range nested.origins {
			if origin.plugin != nil && !builtinPluginFieldClasses(origin.plugin)[origin.key].referent {
				t.Errorf("%s is digested, but %s reaches it through an inline key: extract %s in builtinPluginReferentExtractors and classify the key as a referent", name, origin, key)
			}
		}
	case kustomizationFieldRejected:
		for _, origin := range nested.origins {
			if origin.plugin != nil {
				t.Errorf("%s is rejected by kustomization graph validation, which never sees the %s config that reaches it", name, origin)
			}
		}
		t.Run(name, func(t *testing.T) {
			assertKustomizationCoverageFixture(t, coverage)
		})
	}
}

// digestedFixtureSetKeys returns the nested keys ("types.HelmChart.name") the
// digested kustomizationFieldCoverageTable fixtures set, decoding them as
// graph validation does.
func digestedFixtureSetKeys(t *testing.T) map[string]bool {
	t.Helper()
	set := map[string]bool{}
	for _, name := range slices.Sorted(maps.Keys(kustomizationFieldCoverageTable)) {
		coverage := kustomizationFieldCoverageTable[name]
		if coverage.class != kustomizationFieldDigested {
			continue
		}
		var kustomization types.Kustomization
		if err := goyaml.Unmarshal([]byte(coverage.kustomization), &kustomization); err != nil {
			t.Fatalf("decode %s fixture: %v", name, err)
		}
		collectSetNestedKeys(t, reflect.ValueOf(kustomization), set)
	}
	return set
}

// collectSetNestedKeys records, as type.key, every non-zero JSON key of every
// struct value within value.
func collectSetNestedKeys(t *testing.T, value reflect.Value, set map[string]bool) {
	t.Helper()
	switch kind := value.Kind(); {
	case (kind == reflect.Pointer || kind == reflect.Interface) && !value.IsNil():
		collectSetNestedKeys(t, value.Elem(), set)
	case kind == reflect.Slice || kind == reflect.Array:
		for i := range value.Len() {
			collectSetNestedKeys(t, value.Index(i), set)
		}
	case kind == reflect.Map:
		for iter := value.MapRange(); iter.Next(); {
			collectSetNestedKeys(t, iter.Value(), set)
		}
	case kind == reflect.Struct:
		for key, field := range decodedJSONFields(t, value.Type()) {
			fieldValue, err := value.FieldByIndexErr(field.index)
			if err != nil {
				continue
			}
			if !fieldValue.IsZero() {
				set[value.Type().String()+"."+key] = true
			}
			collectSetNestedKeys(t, fieldValue, set)
		}
	}
}
