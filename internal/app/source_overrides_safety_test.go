package app

import (
	"context"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	argoappv1 "github.com/argoproj/argo-cd/v3/pkg/apis/application/v1alpha1"
	sourcepkg "github.com/sholdee/drydock/internal/source"
)

// withTimeout runs fn and fails the test when it does not return in time,
// such as when it blocks on a named pipe.
func withTimeout(t *testing.T, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("timed out: an override file blocked the read")
	}
}

// TestArgocdSourceOverridesReadOnlySafeFiles pins that drydock reads an
// .argocd-source*.yaml file only when it is a regular file whose real path
// is in the repository and outside .git. Changed-only selection falls back
// to the spec source, and the render fails, without blocking on a named
// pipe or reading a device or a file outside the repository.
func TestArgocdSourceOverridesReadOnlySafeFiles(t *testing.T) {
	outside := t.TempDir()
	writeTestFile(t, filepath.Join(outside, "override.yaml"), "helm:\n  valueFiles:\n    - ../../outside-values.yaml\n")
	for _, tt := range []struct {
		name  string
		make  func(t *testing.T, root, file string)
		want  string
		skips bool
	}{
		{name: "symlink outside the repository", make: func(t *testing.T, root, file string) {
			symlinkBoundsLink(t, filepath.Join(outside, "override.yaml"), file)
		}, want: "outside the repository"},
		{name: "symlink to /dev/zero", make: func(t *testing.T, root, file string) {
			symlinkBoundsLink(t, "/dev/zero", file)
		}, want: "outside the repository"},
		{name: "symlink into .git", make: func(t *testing.T, root, file string) {
			writeTestFile(t, filepath.Join(root, ".git", "override.yaml"), "helm: {}\n")
			symlinkBoundsLink(t, "../../.git/override.yaml", file)
		}, want: ".git directory"},
		{name: "named pipe", make: func(t *testing.T, root, file string) {
			if err := syscall.Mkfifo(file, 0o644); err != nil {
				t.Skipf("mkfifo unavailable: %v", err)
			}
		}, want: "not a regular file"},
		{name: "dangling symlink is skipped", make: func(t *testing.T, root, file string) {
			symlinkBoundsLink(t, "missing.yaml", file)
		}, skips: true},
		{name: "symlink to a directory in the repository is skipped", make: func(t *testing.T, root, file string) {
			writeTestFile(t, filepath.Join(root, "shared", "dir", "keep.yaml"), "helm: {}\n")
			symlinkBoundsLink(t, "../../shared/dir", file)
		}, skips: true},
		{name: "symlink to a directory in .git is skipped", make: func(t *testing.T, root, file string) {
			writeTestFile(t, filepath.Join(root, ".git", "refs", "keep"), "x\n")
			symlinkBoundsLink(t, "../../.git/refs", file)
		}, skips: true},
		{name: "symlink to a directory outside the repository is skipped", make: func(t *testing.T, root, file string) {
			symlinkBoundsLink(t, outside, file)
		}, skips: true},
		{name: "symlink in the repository", make: func(t *testing.T, root, file string) {
			writeTestFile(t, filepath.Join(root, "shared", "override.yaml"), "helm:\n  releaseName: from-linked-override\n")
			symlinkBoundsLink(t, "../../shared/override.yaml", file)
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			application := writeHelmFileParameterSymlinkApp(t, root, false)
			file := filepath.Join(root, "charts", "demo", repoSourceFile)
			tt.make(t, root, file)
			source := *application.Spec.Source
			var merged argoappv1.ApplicationSource
			var err error
			withTimeout(t, func() {
				merged, err = mergeArgocdSourceOverrides(source, root, filepath.Join(root, "charts", "demo"), "demo")
			})
			switch {
			case tt.want != "":
				if err == nil || !strings.Contains(err.Error(), tt.want) {
					t.Fatalf("mergeArgocdSourceOverrides() error = %v, want %q", err, tt.want)
				}
				assertUnsafeOverrideFallback(t, root, application, tt.want)
			case tt.skips:
				if err != nil || merged.Helm == nil || merged.Helm.ReleaseName != "" {
					t.Fatalf("mergeArgocdSourceOverrides() = %#v, %v, want the source unchanged", merged.Helm, err)
				}
			default:
				if err != nil || merged.Helm == nil || merged.Helm.ReleaseName != "from-linked-override" {
					t.Fatalf("mergeArgocdSourceOverrides() = %#v, %v, want the linked override read", merged.Helm, err)
				}
			}
		})
	}
}

// assertUnsafeOverrideFallback checks that selection falls back to the spec
// source and that the render fails with want, both in time. The render
// allows out-of-bounds symlinks, so its own check does not fail first.
func assertUnsafeOverrideFallback(t *testing.T, root string, application argoappv1.Application, want string) {
	t.Helper()
	withTimeout(t, func() {
		got := withSelectionOnlyPaths(context.Background(), root, nil, selfRepoRefs{}, []ApplicationSelectionInput{{Application: application}}).inputs
		if selected, _ := SelectChangedApplicationInputs(got, []string{"links/target.txt"}); len(selected) != 1 {
			t.Errorf("selection did not fall back to the spec source (paths %v)", got[0].Paths)
		}
	})
	withTimeout(t, func() {
		provider := localProvider{repoRoot: root, sourceResolver: sourcepkg.NewResolver(sourcepkg.Options{}), allowOutOfBoundsSymlinks: true}
		if _, err := RenderApplication(context.Background(), application, provider); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("RenderApplication() error = %v, want %q", err, want)
		}
	})
}

// TestArgocdSourceOverrideWithRelativeRoot pins that a relative root, such
// as --path ., reads an override file that is a symlink with an absolute
// target in the repository.
func TestArgocdSourceOverrideWithRelativeRoot(t *testing.T) {
	root := t.TempDir()
	application := writeHelmFileParameterSymlinkApp(t, root, false)
	writeTestFile(t, filepath.Join(root, "shared", "override.yaml"), "helm:\n  releaseName: from-absolute-link\n")
	symlinkBoundsLink(t, filepath.Join(root, "shared", "override.yaml"), filepath.Join(root, "charts", "demo", repoSourceFile))
	t.Chdir(root)
	merged, err := mergeArgocdSourceOverrides(*application.Spec.Source, ".", filepath.Join("charts", "demo"), "demo")
	if err != nil || merged.Helm == nil || merged.Helm.ReleaseName != "from-absolute-link" {
		t.Fatalf("mergeArgocdSourceOverrides() = %#v, %v, want the linked override read", merged.Helm, err)
	}
}

// TestSettingsInstallMergedSourceFallsBackOnUnsafeOverride pins that the
// Argo CD settings scan uses the spec source when an override file is not
// safe to read, without blocking on a named pipe.
func TestSettingsInstallMergedSourceFallsBackOnUnsafeOverride(t *testing.T) {
	root := t.TempDir()
	application := writeHelmFileParameterSymlinkApp(t, root, false)
	if err := syscall.Mkfifo(filepath.Join(root, "charts", "demo", repoSourceFile), 0o644); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	plan := mustPlan(t, application)
	var got SourcePlan
	withTimeout(t, func() {
		got = settingsInstallMergedSource(localProvider{repoRoot: root}, application, plan.Sources[0], root, filepath.Join(root, "charts", "demo"))
	})
	if got.Source.Helm == nil || len(got.Source.Helm.FileParameters) != 1 || got.Source.Helm.FileParameters[0].Path != "../../links/chain.txt" {
		t.Fatalf("settingsInstallMergedSource() source = %#v, want the spec source", got.Source.Helm)
	}
}
