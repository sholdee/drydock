package app

import (
	"maps"
	"reflect"
	"slices"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	"github.com/sholdee/drydock/internal/config"
	"github.com/sholdee/drydock/internal/project"
)

// settingsChangeClass is what a change to one settings field reaches
// (classifySettingsChange).
type settingsChangeClass string

const (
	// settingsRenderAll: every Application.
	settingsRenderAll settingsChangeClass = "render-all"
	// settingsScoped: the Applications whose sources or destination use the
	// changed entry.
	settingsScoped settingsChangeClass = "scoped"
	// settingsNoEffect: nothing a build, diff, or test reports.
	settingsNoEffect settingsChangeClass = "no-effect"
)

// argoSettingsFieldClasses classes every config.ArgoSettings field by its
// readers. A field added to ArgoSettings fails
// TestSettingsFieldClassification until it is classed here, and its class
// must be what classifySettingsChange does with it. Trace a new field's
// readers before classing it; when in doubt, it is render-all.
var argoSettingsFieldClasses = map[string]settingsChangeClass{
	// Render: Kustomize build, Helm value files, tracking metadata, plugin
	// discovery and renders.
	"KustomizeBuildOptions":    settingsRenderAll,
	"HelmValuesFileSchemes":    settingsRenderAll,
	"HelmValuesFileSchemesSet": settingsRenderAll,
	"TrackingMethod":           settingsRenderAll,
	"InstanceLabelKey":         settingsRenderAll,
	"InstallationID":           settingsRenderAll,
	"ConfigManagementPlugins":  settingsRenderAll,
	// Diff: which rendered resources survive, and normalization.
	"ResourceExclusions":     settingsRenderAll,
	"ResourceInclusions":     settingsRenderAll,
	"CompareOptions":         settingsRenderAll,
	"ResourceCustomizations": settingsRenderAll,
	// Project validation of the Applications that use the entry; the OCI
	// repository set it implies is render-all (renderAllSettingsSignature).
	"HelmRepositories": settingsScoped,
	"Clusters":         settingsScoped,
	// Read only by diag, by settings merging, or as provenance.
	"HelmValuesFileSchemesSource":  settingsNoEffect,
	"IgnoreResourceUpdatesEnabled": settingsNoEffect,
	"CommandParameters":            settingsNoEffect,
}

// resourceCustomizationFieldClasses classes every config.ResourceCustomization
// field the way argoSettingsFieldClasses classes ArgoSettings fields.
var resourceCustomizationFieldClasses = map[string]settingsChangeClass{
	// Diff normalization (normalizationFor).
	"IgnoreDifferences": settingsRenderAll,
	"KnownTypeFields":   settingsRenderAll,
	// Lua health checks (luahealth.New).
	"HasHealthLua":         settingsRenderAll,
	"HealthLuaSHA256":      settingsRenderAll,
	"HealthLua":            settingsRenderAll,
	"healthLuaFingerprint": settingsRenderAll,
	"HasUseOpenLibs":       settingsRenderAll,
	"UseOpenLibs":          settingsRenderAll,
	// Only diag summarizes them; provenance names the side's own tree.
	"IgnoreResourceUpdates": settingsNoEffect,
	"Actions":               settingsNoEffect,
	"Provenance":            settingsNoEffect,
}

// fingerprintedResourceCustomizationFields are fields no signature reads
// directly: HealthLuaSHA256 fingerprints them, so a change to them alone is
// not one a loaded customization can carry.
var fingerprintedResourceCustomizationFields = map[string]bool{
	"HealthLua":            true,
	"healthLuaFingerprint": true,
}

// repositorySettingsFieldClasses classes every config.RepositorySettings
// field of an entry whose URL is not an OCI repository: validation reads
// them, only for the Applications that use the entry.
// ociRepositoryFieldClasses overrides it for an OCI-enabled Helm entry with
// an OCI URL, whose OCI repository set every render reads.
var (
	repositorySettingsFieldClasses = map[string]settingsChangeClass{
		"Name":       settingsScoped,
		"Type":       settingsScoped,
		"URL":        settingsScoped,
		"EnableOCI":  settingsScoped,
		"Project":    settingsScoped,
		"Provenance": settingsNoEffect,
	}
	ociRepositoryFieldClasses = map[string]settingsChangeClass{
		"Type":      settingsRenderAll,
		"URL":       settingsRenderAll,
		"EnableOCI": settingsRenderAll,
	}
)

// clusterSettingsFieldClasses classes every config.ClusterSettings field.
var clusterSettingsFieldClasses = map[string]settingsChangeClass{
	"Name":             settingsScoped,
	"Server":           settingsScoped,
	"Namespaces":       settingsScoped,
	"ClusterResources": settingsScoped,
	"Project":          settingsScoped,
	"Provenance":       settingsNoEffect,
}

// Every settings field is classed, and classifySettingsChange treats a change
// to it as its class says: a field nobody classed cannot be scoped or
// dropped silently.
func TestSettingsFieldClassification(t *testing.T) {
	assertFieldsClassed(t, reflect.TypeFor[config.ArgoSettings](), argoSettingsFieldClasses)
	assertFieldsClassed(t, reflect.TypeFor[config.ResourceCustomization](), resourceCustomizationFieldClasses)
	assertFieldsClassed(t, reflect.TypeFor[config.RepositorySettings](), repositorySettingsFieldClasses)
	assertFieldsClassed(t, reflect.TypeFor[config.ClusterSettings](), clusterSettingsFieldClasses)

	for name, class := range argoSettingsFieldClasses {
		changed := config.ArgoSettings{}
		reflect.ValueOf(&changed).Elem().FieldByName(name).Set(nonZeroValue(t, reflect.TypeFor[config.ArgoSettings]().Name()+"."+name, mustField(t, reflect.TypeFor[config.ArgoSettings](), name).Type))
		assertSettingsChangeClass(t, name, config.ArgoSettings{}, changed, class)
	}

	// Each customization field changes alone, on a new customization and on
	// one that already declares another render-affecting field.
	const key = "apps/Deployment"
	for name, class := range resourceCustomizationFieldClasses {
		field := mustField(t, reflect.TypeFor[config.ResourceCustomization](), name)
		if !field.IsExported() || fingerprintedResourceCustomizationFields[name] {
			continue
		}
		existing := config.ResourceCustomization{KnownTypeFields: []config.KnownTypeField{{Field: "spec", Type: "core/v1/PodSpec"}}}
		if name == "KnownTypeFields" {
			existing = config.ResourceCustomization{IgnoreDifferences: config.OverrideIgnoreDifferences{JSONPointers: []string{"/spec"}}}
		}
		for _, base := range []map[string]config.ResourceCustomization{nil, {key: existing}} {
			customization := base[key]
			reflect.ValueOf(&customization).Elem().FieldByName(name).Set(nonZeroValue(t, "ResourceCustomization."+name, field.Type))
			changed := config.ArgoSettings{ResourceCustomizations: map[string]config.ResourceCustomization{key: customization}}
			assertSettingsChangeClass(t, "ResourceCustomizations."+name, config.ArgoSettings{ResourceCustomizations: base}, changed, class)
		}
	}

	// Each repository and cluster entry field changes alone.
	for _, base := range []struct {
		name      string
		entry     config.RepositorySettings
		overrides map[string]settingsChangeClass
	}{
		{name: "HTTP", entry: config.RepositorySettings{URL: chartsXURL, Type: "helm"}},
		{name: "OCI", entry: config.RepositorySettings{URL: "ghcr.io/example/charts", Type: "helm", EnableOCI: true}, overrides: ociRepositoryFieldClasses},
	} {
		for name, class := range repositorySettingsFieldClasses {
			if override, ok := base.overrides[name]; ok {
				class = override
			}
			changed := base.entry
			setChangedField(t, reflect.ValueOf(&changed).Elem(), name)
			assertSettingsChangeClass(t, "HelmRepositories."+name+" ("+base.name+")",
				config.ArgoSettings{HelmRepositories: map[string]config.RepositorySettings{base.entry.URL: base.entry}},
				config.ArgoSettings{HelmRepositories: map[string]config.RepositorySettings{base.entry.URL: changed}}, class)
		}
	}
	cluster := config.ClusterSettings{Name: "prod", Server: prodServer}
	for name, class := range clusterSettingsFieldClasses {
		changed := cluster
		setChangedField(t, reflect.ValueOf(&changed).Elem(), name)
		assertSettingsChangeClass(t, "Clusters."+name,
			config.ArgoSettings{Clusters: map[string]config.ClusterSettings{prodServer: cluster}},
			config.ArgoSettings{Clusters: map[string]config.ClusterSettings{prodServer: changed}}, class)
	}
}

// setChangedField sets the field name of entry to a value it does not hold.
func setChangedField(t *testing.T, entry reflect.Value, name string) {
	t.Helper()
	field := entry.FieldByName(name)
	value := nonZeroValue(t, entry.Type().Name()+"."+name, field.Type())
	if reflect.DeepEqual(value.Interface(), field.Interface()) {
		value = reflect.Zero(field.Type())
	}
	field.Set(value)
}

func assertFieldsClassed(t *testing.T, typ reflect.Type, classes map[string]settingsChangeClass) {
	t.Helper()
	fields := map[string]bool{}
	for field := range typ.Fields() {
		fields[field.Name] = true
		if _, ok := classes[field.Name]; !ok {
			t.Errorf("%s.%s has no settings change class: trace its readers and class it (render-all when in doubt)", typ.Name(), field.Name)
		}
	}
	for name := range classes {
		if !fields[name] {
			t.Errorf("%s has no field %s: drop its class", typ.Name(), name)
		}
	}
}

func mustField(t *testing.T, typ reflect.Type, name string) reflect.StructField {
	t.Helper()
	field, ok := typ.FieldByName(name)
	if !ok {
		t.Fatalf("%s has no field %s", typ.Name(), name)
	}
	return field
}

func assertSettingsChangeClass(t *testing.T, name string, left, right config.ArgoSettings, want settingsChangeClass) {
	t.Helper()
	delta, err := classifySettingsChange(left, right)
	if err != nil {
		t.Fatalf("%s: classifySettingsChange() error = %v", name, err)
	}
	got := settingsNoEffect
	switch {
	case delta.renderAll:
		got = settingsRenderAll
	case delta.scoped():
		got = settingsScoped
	}
	if got != want {
		t.Errorf("%s change classified %s, want %s (delta %+v)", name, got, want, delta)
	}
}

// nonZeroValue builds a value of typ with every exported field, element, and
// map entry set, so that any change a reader could see is present.
func nonZeroValue(t *testing.T, path string, typ reflect.Type) reflect.Value {
	t.Helper()
	value := reflect.New(typ).Elem()
	if scalar, ok := map[reflect.Kind]any{reflect.String: "x", reflect.Bool: true, reflect.Int: 1}[typ.Kind()]; ok {
		value.Set(reflect.ValueOf(scalar).Convert(typ))
		return value
	}
	if typ.Kind() == reflect.Slice {
		value.Set(reflect.Append(value, nonZeroValue(t, path+"[]", typ.Elem())))
		return value
	}
	if typ.Kind() == reflect.Map {
		entries := reflect.MakeMap(typ)
		entries.SetMapIndex(nonZeroValue(t, path+"{}", typ.Key()), nonZeroValue(t, path+"[]", typ.Elem()))
		value.Set(entries)
		return value
	}
	if typ.Kind() != reflect.Struct {
		t.Fatalf("%s: no non-zero value for kind %s", path, typ.Kind())
	}
	for i := range typ.NumField() {
		if field := typ.Field(i); field.IsExported() {
			value.Field(i).Set(nonZeroValue(t, path+"."+field.Name, field.Type))
		}
	}
	return value
}

func TestClassifySettingsChange(t *testing.T) {
	repo := func(url, project string) config.RepositorySettings {
		return config.RepositorySettings{Name: "charts", Type: "helm", URL: url, Project: project, Provenance: config.Provenance{Path: "/left/repos.yaml"}}
	}
	repos := func(entries map[string]config.RepositorySettings) config.ArgoSettings {
		return config.ArgoSettings{HelmRepositories: entries}
	}
	cluster := func(name, server, project string) config.ClusterSettings {
		return config.ClusterSettings{Name: name, Server: server, Project: project, Provenance: config.Provenance{Path: "/left/clusters.yaml"}}
	}
	clusters := func(entries ...config.ClusterSettings) config.ArgoSettings {
		settings := config.ArgoSettings{Clusters: map[string]config.ClusterSettings{}}
		for _, entry := range entries {
			settings.Clusters[entry.Server] = entry
		}
		return settings
	}
	moved := func(settings config.ArgoSettings) config.ArgoSettings {
		next := config.ArgoSettings{HelmRepositories: map[string]config.RepositorySettings{}, Clusters: map[string]config.ClusterSettings{}}
		for key, entry := range settings.HelmRepositories {
			entry.Provenance = config.Provenance{Path: "/right/repos.yaml", Pointer: "/stringData/url"}
			next.HelmRepositories[key] = entry
		}
		for key, entry := range settings.Clusters {
			entry.Provenance = config.Provenance{Path: "/right/clusters.yaml"}
			next.Clusters[key] = entry
		}
		return next
	}
	application := func(project, repoURL string) argoappv1.Application {
		return argoappv1.Application{Name: "app", Spec: argoappv1.ApplicationSpec{
			Project: project,
			Source:  &argoappv1.ApplicationSource{RepoURL: repoURL, Path: "app"},
		}}
	}
	ociEnabled := repo("ghcr.io/example/charts", "")
	ociEnabled.EnableOCI = true
	x := map[string]config.RepositorySettings{chartsXURL: repo(chartsXURL, "")}

	for _, tc := range []struct {
		name        string
		left, right config.ArgoSettings
		renderAll   bool
		// repositories are the keys of the changed repository entries.
		repositories                           []string
		clusterServers, clusterNames, projects []string
		// selects and skips are Applications the delta must and must not
		// select.
		selects, skips []argoappv1.Application
	}{
		{name: "identical", left: repos(x), right: repos(x), skips: []argoappv1.Application{application("", chartsXURL)}},
		{name: "provenance only", left: repos(x), right: moved(repos(x)), skips: []argoappv1.Application{application("", chartsXURL)}},
		{
			name:         "repository added",
			left:         repos(nil),
			right:        repos(map[string]config.RepositorySettings{chartsXURL: repo(chartsXURL, "team-a")}),
			repositories: []string{chartsXURL},
			selects:      []argoappv1.Application{application("other", chartsXURL+".git")},
			skips:        []argoappv1.Application{application("team-a", chartsYURL)},
		},
		{
			name:         "repository removed",
			left:         repos(map[string]config.RepositorySettings{chartsXURL: repo(chartsXURL, ""), chartsYURL: repo(chartsYURL, "")}),
			right:        repos(map[string]config.RepositorySettings{chartsYURL: repo(chartsYURL, "")}),
			repositories: []string{chartsXURL},
			selects:      []argoappv1.Application{application("", chartsXURL)},
			skips:        []argoappv1.Application{application("", chartsYURL)},
		},
		{
			name:         "repository project changed",
			left:         repos(map[string]config.RepositorySettings{chartsXURL: repo(chartsXURL, "team-a")}),
			right:        repos(map[string]config.RepositorySettings{chartsXURL: repo(chartsXURL, "team-b")}),
			repositories: []string{chartsXURL},
			selects:      []argoappv1.Application{application("team-b", chartsXURL)},
		},
		{
			// The key and the URL both match sources.
			name:         "repository URL rewritten under its key",
			left:         repos(x),
			right:        repos(map[string]config.RepositorySettings{chartsXURL: repo(chartsYURL, "")}),
			repositories: []string{chartsXURL},
			selects:      []argoappv1.Application{application("", chartsXURL), application("", chartsYURL)},
		},
		{
			name:         "entry key renamed with the same URL",
			left:         repos(map[string]config.RepositorySettings{"charts-a": repo(chartsXURL, "")}),
			right:        repos(map[string]config.RepositorySettings{"charts-b": repo(chartsXURL, "")}),
			repositories: []string{"charts-a", "charts-b"},
			selects:      []argoappv1.Application{application("", chartsXURL)},
		},
		{
			name:         "same URL under two keys",
			left:         repos(map[string]config.RepositorySettings{"charts-a": repo(chartsXURL, "")}),
			right:        repos(map[string]config.RepositorySettings{"charts-a": repo(chartsXURL, ""), "charts-b": repo(chartsXURL, "")}),
			repositories: []string{"charts-b"},
			selects:      []argoappv1.Application{application("", chartsXURL)},
			skips:        []argoappv1.Application{application("", chartsYURL)},
		},
		{
			name:         "deny-pattern repository reaches its whole project",
			left:         repos(nil),
			right:        repos(map[string]config.RepositorySettings{"!" + chartsXURL: repo("!"+chartsXURL, "team-a")}),
			repositories: []string{"!" + chartsXURL},
			selects:      []argoappv1.Application{application("team-a", chartsYURL)},
			skips:        []argoappv1.Application{application("team-b", chartsYURL)},
		},
		{
			// enableOCI changes how Helm dependencies and Kustomize
			// helmCharts anywhere resolve the repository.
			name:      "OCI repository added",
			left:      repos(nil),
			right:     repos(map[string]config.RepositorySettings{ociEnabled.URL: ociEnabled}),
			renderAll: true,
		},
		{
			name:           "cluster server changed",
			left:           clusters(cluster("prod", prodServer, "")),
			right:          clusters(cluster("prod", prodBServer, "")),
			clusterServers: []string{prodBServer, prodServer},
			clusterNames:   []string{prodBServer, prodServer, "prod"},
		},
		{
			name:           "cluster name and server changed",
			left:           clusters(cluster("prod", prodServer, "")),
			right:          clusters(cluster("prod-b", prodBServer, "")),
			clusterServers: []string{prodBServer, prodServer},
			clusterNames:   []string{prodBServer, prodServer, "prod", "prod-b"},
		},
		{
			// Validation falls back to the server for a nameless cluster.
			name:           "nameless cluster added",
			left:           clusters(),
			right:          config.ArgoSettings{Clusters: map[string]config.ClusterSettings{prodServer: cluster("", prodServer+"/", "")}},
			clusterServers: []string{prodServer},
			clusterNames:   []string{prodServer},
		},
		{
			name:           "project-scoped cluster moved",
			left:           clusters(cluster("prod", prodServer, "team-a")),
			right:          clusters(cluster("prod", prodServer, "team-b")),
			clusterServers: []string{prodServer},
			clusterNames:   []string{prodServer, "prod"},
			projects:       []string{"team-a", "team-b"},
			selects:        []argoappv1.Application{application("team-a", chartsYURL)},
			skips:          []argoappv1.Application{application("team-c", chartsYURL)},
		},
		{
			name:      "serverless cluster",
			left:      clusters(),
			right:     config.ArgoSettings{Clusters: map[string]config.ClusterSettings{"": cluster("prod", "", "")}},
			renderAll: true,
		},
		{
			name:      "render-affecting change wins over a scoped one",
			left:      repos(nil),
			right:     config.ArgoSettings{HelmRepositories: x, TrackingMethod: config.Value[string]{Value: "label"}},
			renderAll: true,
		},
		{
			name:  "no-effect setting",
			left:  config.ArgoSettings{},
			right: config.ArgoSettings{IgnoreResourceUpdatesEnabled: config.Value[bool]{Value: true}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := classifySettingsChange(tc.left, tc.right)
			if err != nil {
				t.Fatalf("classifySettingsChange() error = %v", err)
			}
			if got.renderAll != tc.renderAll {
				t.Fatalf("classifySettingsChange() renderAll = %t, want %t", got.renderAll, tc.renderAll)
			}
			for _, set := range []struct {
				name      string
				got, want []string
			}{
				{"repositories", slices.Sorted(maps.Keys(got.repositories)), tc.repositories},
				{"clusterServers", slices.Sorted(maps.Keys(got.clusterServers)), tc.clusterServers},
				{"clusterNames", slices.Sorted(maps.Keys(got.clusterNames)), tc.clusterNames},
				{"projects", slices.Sorted(maps.Keys(got.projects)), tc.projects},
			} {
				if !slices.Equal(set.got, set.want) {
					t.Errorf("%s = %v, want %v", set.name, set.got, set.want)
				}
			}
			for _, app := range tc.selects {
				if !got.selects(app) {
					t.Errorf("selects(project %q, repoURL %q) = false, want true", app.Spec.Project, app.Spec.Source.RepoURL)
				}
			}
			for _, app := range tc.skips {
				if got.selects(app) {
					t.Errorf("selects(project %q, repoURL %q) = true, want false", app.Spec.Project, app.Spec.Source.RepoURL)
				}
			}
		})
	}
}

// The Applications a settings change reaches bring every Application they
// render, on either side, and only the direct ones count as reached.
func TestWithSettingsSelection(t *testing.T) {
	app := func(name, repoURL string) argoappv1.Application {
		return argoappv1.Application{
			Name:      name,
			Namespace: "argocd",
			Spec:      argoappv1.ApplicationSpec{Source: &argoappv1.ApplicationSource{RepoURL: repoURL, Path: name}},
		}
	}
	parent := app("parent", chartsXURL)
	child := app("child", otherRepoURL)
	other := app("other", otherRepoURL)
	path := app("path", otherRepoURL)
	side := selectionSide{inputs: []ApplicationSelectionInput{
		{Application: parent},
		{Application: child, ParentKey: applicationKey(parent)},
		{Application: other},
		{Application: path},
	}}
	delta := settingsDelta{repositories: map[string][]project.RepositoryMatch{
		chartsXURL: {project.NewRepositoryMatch(chartsXURL, config.RepositorySettings{URL: chartsXURL})},
	}}

	left, right, reached := withSettingsSelection(side, side, []argoappv1.Application{path}, []argoappv1.Application{path}, delta)
	if reached != 1 {
		t.Fatalf("reached = %d, want 1", reached)
	}
	for _, selected := range [][]argoappv1.Application{left, right} {
		var names []string
		for _, application := range selected {
			names = append(names, application.Name)
		}
		if want := []string{"parent", "child", "path"}; !slices.Equal(names, want) {
			t.Fatalf("selected %v, want %v", names, want)
		}
	}
}
