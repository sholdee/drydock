package config

import (
	"testing"

	"github.com/argoproj/argo-cd/v3/common"
	argoenv "github.com/argoproj/argo-cd/v3/util/env"
)

// The Argo CD v3.6.1 application controller reads
// ARGOCD_APPLICATION_CONTROLLER_SERVER_SIDE_DIFF, which the install fills
// from controller.diff.server.side, with env.ParseBoolFromEnv and the
// default false as the default of --server-side-diff-enabled
// (cmd/argocd-application-controller/commands/argocd_application_controller.go:299).
// The test sets the same environment variable and compares the results.
func TestParseControllerServerSideDiffMatchesTheController(t *testing.T) {
	for _, value := range []string{"", "true", "TRUE", "True", "false", "FALSE", "1", "0", "yes", "on", "t", " true", "true "} {
		t.Setenv(common.EnvServerSideDiff, value)
		want := argoenv.ParseBoolFromEnv(common.EnvServerSideDiff, false)
		if got := ParseControllerServerSideDiff(value); got != want {
			t.Errorf("ParseControllerServerSideDiff(%q) = %t, want %t", value, got, want)
		}
	}
}

// controller.diff.server.side is a diffed key: drydock keeps the value as
// written, gives no runtime-only warning, and reports a conflict only when
// the controller would read two values differently.
func TestCommandParametersReadControllerServerSideDiff(t *testing.T) {
	settings, diags := commandParametersSettings("argocd-cmd-params-cm.yaml", "data", map[string]string{
		controllerServerSideDiffKey: "true",
	})
	if len(diags) != 0 {
		t.Fatalf("diags = %#v, want no runtime-only warning", diags)
	}
	if got := settings.ControllerServerSideDiff.Value; got != "true" {
		t.Fatalf("ControllerServerSideDiff = %q, want true", got)
	}
	if settings.ControllerServerSideDiff.Provenance.Pointer != "data.controller.diff.server.side" {
		t.Fatalf("provenance = %#v", settings.ControllerServerSideDiff.Provenance)
	}
	assertCommandParameter(t, settings, controllerServerSideDiffKey, "true", false, CommandParameterDiffed)

	merged, mergeDiags := MergeDiscovered([]ArgoSettings{settings, settings})
	if merged.ControllerServerSideDiff.Value != "true" || len(mergeDiags) != 0 {
		t.Fatalf("MergeDiscovered() = %q, %#v", merged.ControllerServerSideDiff.Value, mergeDiags)
	}
	off, _ := commandParametersSettings("off.yaml", "data", map[string]string{controllerServerSideDiffKey: "false"})
	if _, conflict := MergeDiscovered([]ArgoSettings{settings, off}); len(conflict) != 1 {
		t.Fatalf("MergeDiscovered() conflict diagnostics = %#v, want one", conflict)
	}
	// Spellings that the controller reads the same do not conflict.
	for _, pair := range [][2]string{{"TRUE", "true"}, {"false", "1"}, {"", "yes"}, {" true", "false"}} {
		left, _ := commandParametersSettings("left.yaml", "data", map[string]string{controllerServerSideDiffKey: pair[0]})
		right, _ := commandParametersSettings("right.yaml", "data", map[string]string{controllerServerSideDiffKey: pair[1]})
		if _, diags := MergeDiscovered([]ArgoSettings{left, right}); len(diags) != 0 {
			t.Fatalf("MergeDiscovered(%q, %q) diagnostics = %#v, want none", pair[0], pair[1], diags)
		}
	}
}
