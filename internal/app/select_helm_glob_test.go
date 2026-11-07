package app

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
)

// TestHelmLocalSelectionPathsFollowValueFileGlobSymlinks pins that a glob
// value file owns, for each match on the tree, the symlinks on its way and
// the file where it ends, as a value file written as that match does. A
// change to the target of a symlink that the glob matches, or that the glob
// passes through, then selects the Application.
func TestHelmLocalSelectionPathsFollowValueFileGlobSymlinks(t *testing.T) {
	repo := t.TempDir()
	write := func(rel, content string) {
		full := filepath.Join(repo, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	link := func(target, rel string) {
		full := filepath.Join(repo, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, full); err != nil {
			t.Fatal(err)
		}
	}
	write("charts/app/Chart.yaml", "apiVersion: v2\nname: app\nversion: 0.1.0\n")
	write("shared/real.yaml", "a: 1\n")
	write("shared/dir/inner.yaml", "a: 1\n")
	link("../../../shared/real.yaml", "charts/app/env/linked.yaml")
	link("../../shared/dir", "charts/app/linkdir")
	for _, tt := range []struct {
		file string
		want []string
	}{
		{file: "env/*.yaml", want: []string{"charts/app/env", "charts/app/env/linked.yaml", "shared/real.yaml"}},
		{file: "linkdir/*.yaml", want: []string{"charts/app/linkdir", "shared/dir/inner.yaml"}},
		{file: "/charts/app/env/*.yaml", want: []string{"charts/app/env", "charts/app/env/linked.yaml", "shared/real.yaml"}},
	} {
		source := argoappv1.ApplicationSource{Path: "charts/app", Helm: &argoappv1.ApplicationSourceHelm{ValueFiles: []string{tt.file}}}
		got := helmLocalSelectionPaths(repo, source)
		for _, want := range tt.want {
			if !slices.Contains(got, want) {
				t.Fatalf("helmLocalSelectionPaths(%q) = %v, want %q", tt.file, got, want)
			}
		}
	}
}
