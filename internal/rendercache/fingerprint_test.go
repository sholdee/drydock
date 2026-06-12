package rendercache

import (
	"runtime/debug"
	"testing"
)

func TestEngineFingerprintKnown(t *testing.T) {
	cases := []struct {
		name   string
		commit string
		want   bool
	}{
		{name: "release commit", commit: "0123456789abcdef0123456789abcdef01234567", want: true},
		{name: "short commit", commit: "abc1234", want: true},
		{name: "dev none", commit: "none", want: false},
		{name: "empty", commit: "", want: false},
		{name: "whitespace", commit: "   ", want: false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fingerprint := EngineFingerprint{Commit: testCase.commit}
			if got := fingerprint.Known(); got != testCase.want {
				t.Fatalf("Known() = %t, want %t", got, testCase.want)
			}
		})
	}
}

func TestFingerprintFromBuildInfoIsConsistentWithVCSCommit(t *testing.T) {
	// Test binaries do not carry VCS stamping, so the exact values depend on
	// the build; the contract is internal consistency: the fingerprint commit
	// is "none" exactly when no clean VCS revision is available.
	fingerprint := FingerprintFromBuildInfo()
	commit, ok := VCSCommitFromBuildInfo()
	if ok {
		if fingerprint.Commit != commit {
			t.Fatalf("FingerprintFromBuildInfo().Commit = %q, want VCS commit %q", fingerprint.Commit, commit)
		}
		if !fingerprint.Known() {
			t.Fatalf("Known() = false with VCS commit available")
		}
		return
	}
	if fingerprint.Commit != "none" {
		t.Fatalf("FingerprintFromBuildInfo().Commit = %q, want \"none\" without VCS data", fingerprint.Commit)
	}
	if fingerprint.Known() {
		t.Fatalf("Known() = true without VCS data")
	}
}

func TestModuleLabelIncludesReplacement(t *testing.T) {
	info := &debug.BuildInfo{Deps: []*debug.Module{{
		Path:    "helm.sh/helm/v4",
		Version: "v4.0.0",
		Replace: &debug.Module{Path: "example.test/fork/helm", Version: "v4.0.1"},
	}}}
	got := ModuleLabel(info, "helm.sh/helm/v4")
	want := "helm.sh/helm/v4@v4.0.0 => example.test/fork/helm@v4.0.1"
	if got != want {
		t.Fatalf("ModuleLabel() = %q, want %q", got, want)
	}
	if got := ModuleLabel(info, "unknown.test/module"); got != "unknown.test/module" {
		t.Fatalf("ModuleLabel(unknown) = %q, want bare path", got)
	}
}
