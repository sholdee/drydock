package dyffreport

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"sigs.k8s.io/kustomize/api/hasher"

	"github.com/sholdee/drydock/internal/diff"
)

const (
	oldHash = "745mg7f9mk"
	newHash = "8f59fckt76"
)

func generatedConfigMap(name, namespace, tail string) diff.Document {
	return diff.Document{
		Parent:   diff.Parent{Name: "demo"},
		Resource: diff.Resource{Kind: "ConfigMap", Namespace: namespace, Name: name},
		Body: "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: " + name +
			"\n  namespace: " + namespace + "\ndata:\n  A: a\n  B: b\n  C: c\n  D: d\n  TAIL: " + tail + "\n",
	}
}

func renderItems(t *testing.T, results []diff.Result) string {
	t.Helper()
	var out bytes.Buffer
	if err := Write(&out, results, false); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	return out.String()
}

func TestGeneratedRenameShowsOnlyChangedValue(t *testing.T) {
	results := runDiff(t,
		[]diff.Document{generatedConfigMap("common-env-"+oldHash, "app", "old")},
		[]diff.Document{generatedConfigMap("common-env-"+newHash, "app", "new")})
	if len(results) != 2 {
		t.Fatalf("diff.Run() results = %d, want 2", len(results))
	}

	got := renderItems(t, results)

	if !strings.Contains(got, "(renamed from common-env-"+oldHash+")") {
		t.Errorf("header does not mention the rename:\n%s", got)
	}
	for _, want := range []string{"data.TAIL", "old", "new", "metadata.name"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"document removed", "document added", "data.A", "dyff:"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("output contains %q:\n%s", unwanted, got)
		}
	}
	if strings.Count(got, "Application:") != 1 {
		t.Errorf("rename should be one report:\n%s", got)
	}
}

func TestGeneratedRenameGitHubBody(t *testing.T) {
	results := runDiff(t,
		[]diff.Document{generatedConfigMap("common-env-"+oldHash, "app", "old")},
		[]diff.Document{generatedConfigMap("common-env-"+newHash, "app", "new")})

	items := Items(results)
	if len(items) != 1 {
		t.Fatalf("Items() = %d, want 1", len(items))
	}
	got := items[0].GitHubBody()

	for _, want := range []string{"@@ data.TAIL @@", "- old", "+ new", "@@ metadata.name @@"} {
		if !strings.Contains(got, want) {
			t.Errorf("body missing %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"document removed", "document added", "data.A", "dyff:"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("body contains %q:\n%s", unwanted, got)
		}
	}
}

func TestItemsDoNotPairUnrelatedResources(t *testing.T) {
	tests := []struct {
		name        string
		left, right []diff.Document
	}{
		{
			name:  "different base",
			left:  []diff.Document{generatedConfigMap("common-env-"+oldHash, "app", "a")},
			right: []diff.Document{generatedConfigMap("other-env-"+newHash, "app", "a")},
		},
		{
			name:  "different namespace",
			left:  []diff.Document{generatedConfigMap("common-env-"+oldHash, "app", "a")},
			right: []diff.Document{generatedConfigMap("common-env-"+newHash, "other", "a")},
		},
		{
			name:  "not a Kustomize hash",
			left:  []diff.Document{generatedConfigMap("common-env-old", "app", "a")},
			right: []diff.Document{generatedConfigMap("common-env-new", "app", "a")},
		},
		{
			name:  "hash alphabet excludes vowels",
			left:  []diff.Document{generatedConfigMap("common-env-aaaaaaaaaa", "app", "a")},
			right: []diff.Document{generatedConfigMap("common-env-bbbbbbbbbb", "app", "a")},
		},
		{
			name:  "random-name characters Kustomize never emits",
			left:  []diff.Document{generatedConfigMap("common-env-bcdfghjkmn", "app", "a")},
			right: []diff.Document{generatedConfigMap("common-env-pqrsvwxzbc", "app", "a")},
		},
		{
			name: "other kinds are never paired",
			left: []diff.Document{{
				Parent:   diff.Parent{Name: "demo"},
				Resource: diff.Resource{Group: "batch", Kind: "Job", Namespace: "app", Name: "migrate-" + oldHash},
				Body:     "apiVersion: batch/v1\nkind: Job\nmetadata:\n  name: migrate-" + oldHash + "\n  namespace: app\n",
			}},
			right: []diff.Document{{
				Parent:   diff.Parent{Name: "demo"},
				Resource: diff.Resource{Group: "batch", Kind: "Job", Namespace: "app", Name: "migrate-" + newHash},
				Body:     "apiVersion: batch/v1\nkind: Job\nmetadata:\n  name: migrate-" + newHash + "\n  namespace: app\n",
			}},
		},
		{
			name:  "Secrets are never paired",
			left:  []diff.Document{generatedSecret("creds-" + oldHash)},
			right: []diff.Document{generatedSecret("creds-" + newHash)},
		},
		{
			name: "two removed one added",
			left: []diff.Document{
				generatedConfigMap("common-env-"+oldHash, "app", "a"),
				generatedConfigMap("common-env-bcdfghkmt2", "app", "a"),
			},
			right: []diff.Document{generatedConfigMap("common-env-"+newHash, "app", "a")},
		},
		{
			name:  "only removed",
			left:  []diff.Document{generatedConfigMap("common-env-"+oldHash, "app", "a")},
			right: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results := runDiff(t, tt.left, tt.right)

			if got := len(Items(results)); got != len(results) {
				t.Errorf("Items() = %d, want %d (no pairing)", got, len(results))
			}
		})
	}
}

func TestItemsDoNotPairAcrossApplications(t *testing.T) {
	removed := generatedConfigMap("common-env-"+oldHash, "app", "a")
	removed.Parent = diff.Parent{Name: "one"}
	added := generatedConfigMap("common-env-"+newHash, "app", "a")
	added.Parent = diff.Parent{Name: "two"}
	results := runDiff(t, []diff.Document{removed}, []diff.Document{added})

	if got := len(Items(results)); got != 2 {
		t.Errorf("Items() = %d, want 2", got)
	}
}

func TestGeneratedRenameDoesNotChangeResults(t *testing.T) {
	results := runDiff(t,
		[]diff.Document{generatedConfigMap("common-env-"+oldHash, "app", "old")},
		[]diff.Document{generatedConfigMap("common-env-"+newHash, "app", "new")})
	before := []diff.Result{results[0], results[1]}

	Items(results)

	for i := range before {
		if results[i] != before[i] {
			t.Errorf("result %d changed: %+v != %+v", i, results[i], before[i])
		}
	}
}

func TestUnmatchedPairIsRenderedSeparately(t *testing.T) {
	// Every key and value differs, so dyff does not match the documents as a
	// rename and must not report a fake order change or a rename header.
	body := func(name, prefix string) diff.Document {
		var b strings.Builder
		b.WriteString("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: " + name + "\n  namespace: app\ndata:\n")
		for _, key := range []string{"ONE", "TWO", "THREE", "FOUR", "FIVE"} {
			b.WriteString("  " + prefix + key + ": " + prefix + "-" + strings.ToLower(key) + "-value\n")
		}
		return diff.Document{
			Parent:   diff.Parent{Name: "demo"},
			Resource: diff.Resource{Kind: "ConfigMap", Namespace: "app", Name: name},
			Body:     b.String(),
		}
	}
	results := runDiff(t,
		[]diff.Document{body("common-env-"+oldHash, "OLD_")},
		[]diff.Document{body("common-env-"+newHash, "NEW_")})
	if got := len(Items(results)); got != 1 {
		t.Fatalf("Items() = %d, want 1 candidate pair", got)
	}

	for output, got := range map[string]string{
		"Write":      renderItems(t, results),
		"GitHubBody": Items(results)[0].GitHubBody(),
	} {
		for _, unwanted := range []string{"renamed from", "order change", "\u21c6"} {
			if strings.Contains(got, unwanted) {
				t.Errorf("%s: output contains %q:\n%s", output, unwanted, got)
			}
		}
		for _, want := range []string{"document removed", "document added", "common-env-" + oldHash, "common-env-" + newHash} {
			if !strings.Contains(got, want) {
				t.Errorf("%s: output missing %q:\n%s", output, want, got)
			}
		}
	}
}

func TestPairWithUnsafeValueFallsBackPerResource(t *testing.T) {
	// A control character in a value makes dyff refuse the comparison.
	removed := generatedConfigMap("common-env-"+oldHash, "app", `"old"`)
	added := generatedConfigMap("common-env-"+newHash, "app", `"new\rfake"`)
	results := runDiff(t, []diff.Document{removed}, []diff.Document{added})

	items := Items(results)
	if len(items) != 1 {
		t.Fatalf("Items() = %d, want 1", len(items))
	}
	got := items[0].GitHubBody()

	if strings.Contains(got, "renamed from") || strings.ContainsAny(got, "\r") {
		t.Errorf("unexpected rename header or raw control character:\n%q", got)
	}
	if !strings.Contains(got, "values contain control characters; showing unified diff") || !strings.Contains(got, results[1].Diff) {
		t.Errorf("unsafe added resource should fall back to its unified diff:\n%s", got)
	}
	if !strings.Contains(got, "document removed") {
		t.Errorf("safe removed resource should still render with dyff:\n%s", got)
	}
}

func TestItemsKeepSecretsApartFromRedaction(t *testing.T) {
	results := runDiff(t,
		[]diff.Document{generatedSecret("creds-" + oldHash)},
		[]diff.Document{generatedSecret("creds-" + newHash)})

	got := renderItems(t, results)

	if strings.Contains(got, "renamed from") || strings.Contains(got, "order change") {
		t.Errorf("Secrets must render as separate resources:\n%s", got)
	}
}

func TestPairKeepsPositionAndOrder(t *testing.T) {
	left := []diff.Document{
		generatedConfigMap("common-env-"+oldHash, "app", "old"),
		configMapDocument("zz-plain", "1"),
	}
	right := []diff.Document{
		generatedConfigMap("common-env-"+newHash, "app", "new"),
		configMapDocument("zz-plain", "2"),
	}
	results := runDiff(t, left, right)

	got := renderItems(t, results)

	rename := strings.Index(got, "renamed from")
	plain := strings.Index(got, "zz-plain")
	if rename < 0 || plain < 0 || rename > plain {
		t.Errorf("rename should render before zz-plain (rename=%d, plain=%d):\n%s", rename, plain, got)
	}
}

func TestPairsStayInsideOneApplicationSource(t *testing.T) {
	removed := generatedConfigMap("common-env-"+oldHash, "app", "a")
	removed.Parent = diff.Parent{Name: "demo", SourceIndex: 0}
	added := generatedConfigMap("common-env-"+newHash, "app", "a")
	added.Parent = diff.Parent{Name: "demo", SourceIndex: 1}
	results := runDiff(t, []diff.Document{removed}, []diff.Document{added})

	if got := len(Items(results)); got != 2 {
		t.Errorf("Items() = %d, want 2", got)
	}
}

func TestKustomizeHashesAreRecognized(t *testing.T) {
	for i := range 200 {
		hash, err := hasher.SortArrayAndComputeHash([]string{fmt.Sprint(i)})
		if err != nil {
			t.Fatalf("SortArrayAndComputeHash() error = %v", err)
		}
		if base, ok := generatedNameBase("base-" + hash); !ok || base != "base" {
			t.Fatalf("generatedNameBase(%q) = %q, %v; want base, true", "base-"+hash, base, ok)
		}
	}
}

func generatedSecret(name string) diff.Document {
	return diff.Document{
		Parent:   diff.Parent{Name: "demo"},
		Resource: diff.Resource{Kind: "Secret", Namespace: "app", Name: name},
		Body: "apiVersion: v1\nkind: Secret\nmetadata:\n  name: " + name +
			"\n  namespace: app\ndata:\n  A: " + name + "\n  B: Yg==\n  C: Yw==\n",
	}
}

func TestPairRendersWhenAddedResultSortsFirst(t *testing.T) {
	// "2222222222" sorts before "tttttttttt", so diff.Run emits the added
	// result first.
	results := runDiff(t,
		[]diff.Document{generatedConfigMap("common-env-tttttttttt", "app", "old")},
		[]diff.Document{generatedConfigMap("common-env-2222222222", "app", "new")})
	if results[0].Change != diff.ChangeAdded {
		t.Fatalf("results[0].Change = %q, want added first", results[0].Change)
	}

	got := renderItems(t, results)

	for _, want := range []string{"(renamed from common-env-tttttttttt)", "data.TAIL", "old", "new"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "document removed") {
		t.Errorf("rename reported as removal:\n%s", got)
	}
}
