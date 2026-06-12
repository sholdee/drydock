package cli

import (
	"bytes"
	"testing"

	"github.com/sholdee/drydock/internal/rendercache"
)

func executeVersionedCommand(t *testing.T, info VersionInfo, recorder *recordingCLIOrchestrator, args ...string) {
	t.Helper()
	cmd := NewRootCommandWithDependencies(info, Dependencies{Orchestrator: recorder})
	cmd.SetArgs(args)
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute(%v) error = %v\nstdout:\n%s\nstderr:\n%s", args, err, stdout.String(), stderr.String())
	}
}

func releaseVersionInfo() VersionInfo {
	return VersionInfo{
		Version:            "1.2.3",
		Commit:             "0123456789abcdef0123456789abcdef01234567",
		ArgoCDModule:       "github.com/argoproj/argo-cd/v3@v3.0.0",
		GitOpsEngineModule: "github.com/argoproj/argo-cd/gitops-engine@v0.7.0",
		HelmModule:         "helm.sh/helm/v4@v4.0.0",
		KustomizeModule:    "sigs.k8s.io/kustomize/api@v0.20.0",
		JsonnetModule:      "github.com/google/go-jsonnet@v0.21.0",
		KubernetesModule:   "k8s.io/apimachinery@v0.34.0",
	}
}

func TestBuildAppsRenderCacheDefaults(t *testing.T) {
	recorder := &recordingCLIOrchestrator{}
	executeVersionedCommand(t, releaseVersionInfo(), recorder, "build", "apps")

	request := recorder.buildRequests[0]
	if !request.RenderCacheEnabled {
		t.Fatalf("RenderCacheEnabled = false, want true by default")
	}
	if request.RenderCacheDir != "" {
		t.Fatalf("RenderCacheDir = %q, want empty (store resolves the default)", request.RenderCacheDir)
	}
	if request.RenderCacheMaxBytes != rendercache.DefaultMaxSizeBytes {
		t.Fatalf("RenderCacheMaxBytes = %d, want %d", request.RenderCacheMaxBytes, rendercache.DefaultMaxSizeBytes)
	}
	if request.RefreshRenders {
		t.Fatalf("RefreshRenders = true, want false by default")
	}
}

func TestBuildAppsRenderCacheFlags(t *testing.T) {
	recorder := &recordingCLIOrchestrator{}
	executeVersionedCommand(t, releaseVersionInfo(), recorder, "build", "apps",
		"--render-cache=false", "--render-cache-dir", "/tmp/render-cache",
		"--render-cache-max-size", "1Gi", "--refresh-renders")

	request := recorder.buildRequests[0]
	if request.RenderCacheEnabled {
		t.Fatalf("RenderCacheEnabled = true, want false")
	}
	if request.RenderCacheDir != "/tmp/render-cache" {
		t.Fatalf("RenderCacheDir = %q, want /tmp/render-cache", request.RenderCacheDir)
	}
	if request.RenderCacheMaxBytes != 1024*1024*1024 {
		t.Fatalf("RenderCacheMaxBytes = %d, want 1Gi", request.RenderCacheMaxBytes)
	}
	if !request.RefreshRenders {
		t.Fatalf("RefreshRenders = false, want true")
	}
}

func TestRenderCacheFlagsOnEveryRenderPathCommand(t *testing.T) {
	cases := []struct {
		name string
		args []string
		get  func(recorder *recordingCLIOrchestrator) bool
	}{
		{"build app", []string{"build", "app", "demo", "--refresh-renders"}, func(r *recordingCLIOrchestrator) bool { return r.buildAppRequests[0].RefreshRenders }},
		{"test apps", []string{"test", "apps", "--refresh-renders"}, func(r *recordingCLIOrchestrator) bool { return r.buildRequests[0].RefreshRenders }},
		{"test app", []string{"test", "app", "demo", "--refresh-renders"}, func(r *recordingCLIOrchestrator) bool { return r.buildAppRequests[0].RefreshRenders }},
		{"diff apps", []string{"diff", "apps", "--refresh-renders"}, func(r *recordingCLIOrchestrator) bool { return r.diffAppsRequests[0].RefreshRenders }},
		{"diff app", []string{"diff", "app", "demo", "--refresh-renders"}, func(r *recordingCLIOrchestrator) bool { return r.diffAppRequests[0].RefreshRenders }},
		{"diff images", []string{"diff", "images", "--refresh-renders"}, func(r *recordingCLIOrchestrator) bool { return r.diffImagesRequests[0].RefreshRenders }},
		{"get apps", []string{"get", "apps", "--refresh-renders"}, func(r *recordingCLIOrchestrator) bool { return r.listRequests[0].RefreshRenders }},
		{"get images", []string{"get", "images", "--refresh-renders"}, func(r *recordingCLIOrchestrator) bool { return r.buildSelectionRequests[0].RefreshRenders }},
		{"diag", []string{"diag", "--render", "--refresh-renders"}, func(r *recordingCLIOrchestrator) bool { return r.diagRequests[0].RefreshRenders }},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := &recordingCLIOrchestrator{}
			executeVersionedCommand(t, releaseVersionInfo(), recorder, testCase.args...)
			if !testCase.get(recorder) {
				t.Fatalf("RefreshRenders not threaded for %s", testCase.name)
			}
		})
	}
}

func TestRenderCacheMaxSizeRejectsInvalidQuantity(t *testing.T) {
	recorder := &recordingCLIOrchestrator{}
	cmd := NewRootCommandWithDependencies(releaseVersionInfo(), Dependencies{Orchestrator: recorder})
	cmd.SetArgs([]string{"build", "apps", "--render-cache-max-size", "not-a-quantity"})
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	if err := cmd.Execute(); err == nil {
		t.Fatalf("Execute() error = nil, want quantity parse error")
	}
}

func TestEngineFingerprintFromVersionInfoOverridesVersionAndCommit(t *testing.T) {
	fingerprint := engineFingerprintFromVersionInfo(VersionInfo{Version: "v9.9.9", Commit: "abc123"})
	if fingerprint.Version != "v9.9.9" || fingerprint.Commit != "abc123" {
		t.Fatalf("fingerprint = %#v, want ldflags version/commit override", fingerprint)
	}
	buildInfo := rendercache.FingerprintFromBuildInfo()
	if fingerprint.ArgoCDModule != buildInfo.ArgoCDModule || fingerprint.KustomizeModule != buildInfo.KustomizeModule {
		t.Fatalf("module labels diverge from FingerprintFromBuildInfo: %#v vs %#v", fingerprint, buildInfo)
	}
}

func TestEngineFingerprintFromVersionInfoDevBuild(t *testing.T) {
	fingerprint := engineFingerprintFromVersionInfo(VersionInfo{Version: "dev", Commit: "none"})
	if vcs, ok := rendercache.VCSCommitFromBuildInfo(); ok {
		if fingerprint.Commit != vcs {
			t.Fatalf("Commit = %q, want VCS fallback %q", fingerprint.Commit, vcs)
		}
		return
	}
	if fingerprint.Known() {
		t.Fatalf("Known() = true for dev build without VCS buildinfo")
	}
}

func TestBuildAppsThreadsEngineFingerprint(t *testing.T) {
	recorder := &recordingCLIOrchestrator{}
	executeVersionedCommand(t, releaseVersionInfo(), recorder, "build", "apps")

	fingerprint := recorder.buildRequests[0].EngineFingerprint
	if fingerprint.Version != "1.2.3" || fingerprint.Commit != "0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("EngineFingerprint = %#v, want VersionInfo-derived fingerprint", fingerprint)
	}
}
