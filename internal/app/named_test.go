package app

import (
	"strings"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestSelectApplicationByNameMatchesMetadataName(t *testing.T) {
	apps := []argoappv1.Application{
		namedApplication("argocd", "alpha"),
		namedApplication("argocd", "beta"),
	}

	selected, err := SelectApplicationByName(apps, "beta")
	if err != nil {
		t.Fatalf("SelectApplicationByName() error = %v", err)
	}
	if selected.Name != "beta" || selected.Namespace != "argocd" {
		t.Fatalf("selected = %s/%s, want argocd/beta", selected.Namespace, selected.Name)
	}
}

func TestSelectApplicationByNameMatchesNamespaceQualifiedName(t *testing.T) {
	apps := []argoappv1.Application{
		namedApplication("argocd", "demo"),
		namedApplication("other", "demo"),
	}

	selected, err := SelectApplicationByName(apps, "other/demo")
	if err != nil {
		t.Fatalf("SelectApplicationByName() error = %v", err)
	}
	if selected.Name != "demo" || selected.Namespace != "other" {
		t.Fatalf("selected = %s/%s, want other/demo", selected.Namespace, selected.Name)
	}
}

func TestSelectApplicationByNameReportsMissingName(t *testing.T) {
	_, err := SelectApplicationByName([]argoappv1.Application{namedApplication("argocd", "demo")}, "missing")
	if err == nil {
		t.Fatal("SelectApplicationByName() error = nil, want missing error")
	}
	if !strings.Contains(err.Error(), `application "missing" not found`) {
		t.Fatalf("error = %v, want missing application message", err)
	}
}

func TestSelectApplicationByNameReportsAmbiguousName(t *testing.T) {
	apps := []argoappv1.Application{
		namedApplication("argocd", "demo"),
		namedApplication("other", "demo"),
	}

	_, err := SelectApplicationByName(apps, "demo")
	if err == nil {
		t.Fatal("SelectApplicationByName() error = nil, want ambiguous error")
	}
	for _, want := range []string{`application "demo" matched multiple Applications`, "argocd/demo", "other/demo", "use namespace/name"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %v, want %q", err, want)
		}
	}
}

func TestSelectOptionalApplicationByNameAllowsMissing(t *testing.T) {
	selected, ok, err := SelectOptionalApplicationByName([]argoappv1.Application{namedApplication("argocd", "demo")}, "missing")
	if err != nil {
		t.Fatalf("SelectOptionalApplicationByName() error = %v", err)
	}
	if ok {
		t.Fatalf("ok = true with selected %s/%s, want false", selected.Namespace, selected.Name)
	}
}

func TestSelectApplicationByNameRejectsEmptyTarget(t *testing.T) {
	_, err := SelectApplicationByName(nil, " ")
	if err == nil {
		t.Fatal("SelectApplicationByName() error = nil, want empty target error")
	}
	if !strings.Contains(err.Error(), "application name is required") {
		t.Fatalf("error = %v, want required message", err)
	}
}

func namedApplication(namespace, name string) argoappv1.Application {
	return argoappv1.Application{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
		},
	}
}

func TestRenderEventTargetCharset(t *testing.T) {
	cases := []struct {
		name      string
		namespace string
		appName   string
		want      string
	}{
		{"dns-name", "argocd", "demo-app.v2", "argocd/demo-app.v2"},
		{"no-namespace", "", "demo", "demo"},
		{"permissive-extras", "argocd", "My_App", "argocd/My_App"},
		{"scp-url", "argocd", "git@host:repo", "[invalid-name]"},
		{"space", "argocd", "demo app", "[invalid-name]"},
		{"unicode-colon", "argocd", "demo：app", "[invalid-name]"},
		{"empty", "", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			application := argoappv1.Application{ObjectMeta: metav1.ObjectMeta{Namespace: tc.namespace, Name: tc.appName}}
			if got := renderEventTarget(application); got != tc.want {
				t.Fatalf("renderEventTarget(%q/%q) = %q, want %q", tc.namespace, tc.appName, got, tc.want)
			}
		})
	}
}
