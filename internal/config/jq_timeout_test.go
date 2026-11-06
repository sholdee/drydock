package config

import (
	"math"
	"testing"
	"time"

	argoenv "github.com/argoproj/argo-cd/v3/util/env"
)

// TestParseIgnoreNormalizerJQTimeoutMatchesTheController compares drydock's
// reading of controller.ignore.normalizer.jq.timeout with the Argo CD v3.6.1
// application controller. The controller reads
// ARGOCD_IGNORE_NORMALIZER_JQ_TIMEOUT with env.ParseDurationFromEnv(..., 0,
// 0, math.MaxInt64), and a zero result means the 1 second default.
func TestParseIgnoreNormalizerJQTimeoutMatchesTheController(t *testing.T) {
	const envVar = "ARGOCD_IGNORE_NORMALIZER_JQ_TIMEOUT"
	for _, value := range []string{
		"", "0", "0s", "1ns", "2s", "1500ms", "1.5s", "60s", "1m", "3h", "2d", "7d",
		"-1s", "-2d", "abc", " 1s", "1s ", "10", "1d2h", "1D", "1w",
		"9223372036854775807s", "106752d", "9223372036854775807d",
	} {
		t.Setenv(envVar, value)
		want := argoenv.ParseDurationFromEnv(envVar, 0, 0, math.MaxInt64)
		if want == 0 {
			want = time.Second
		}
		if got := ParseIgnoreNormalizerJQTimeout(value); got != want {
			t.Fatalf("ParseIgnoreNormalizerJQTimeout(%q) = %s, controller %s", value, got, want)
		}
	}
}

// A day count that overflows int64 ends the upstream helper with log.Fatal,
// so it cannot be compared with upstream. drydock uses the default.
func TestParseIgnoreNormalizerJQTimeoutOverflowUsesDefault(t *testing.T) {
	if got := ParseIgnoreNormalizerJQTimeout("99999999999999999999d"); got != DefaultIgnoreNormalizerJQTimeout {
		t.Fatalf("ParseIgnoreNormalizerJQTimeout() = %s, want the default", got)
	}
}

// TestCommandParametersReadIgnoreNormalizerJQTimeout pins that drydock
// applies controller.ignore.normalizer.jq.timeout instead of recording it as
// an unknown key, and that two sources that disagree conflict.
func TestCommandParametersReadIgnoreNormalizerJQTimeout(t *testing.T) {
	settings, diags := commandParametersSettings("argocd-cmd-params-cm.yaml", "data", map[string]string{
		"controller.ignore.normalizer.jq.timeout": "5s",
	})
	if got := settings.IgnoreNormalizerJQTimeout.Value; got != "5s" {
		t.Fatalf("IgnoreNormalizerJQTimeout = %q, want 5s", got)
	}
	if settings.IgnoreNormalizerJQTimeout.Provenance.Pointer != "data.controller.ignore.normalizer.jq.timeout" {
		t.Fatalf("provenance = %#v", settings.IgnoreNormalizerJQTimeout.Provenance)
	}
	if len(diags) != 0 {
		t.Fatalf("diags = %#v, want no runtime-only warning", diags)
	}
	if len(settings.CommandParameters) != 1 || settings.CommandParameters[0].Classification != CommandParameterDiffed || settings.CommandParameters[0].Value != "5s" {
		t.Fatalf("CommandParameters = %#v, want one diffed setting with its value", settings.CommandParameters)
	}
	merged, mergeDiags := MergeDiscovered([]ArgoSettings{settings, settings})
	if merged.IgnoreNormalizerJQTimeout.Value != "5s" || len(mergeDiags) != 0 {
		t.Fatalf("MergeDiscovered() = %q, %#v", merged.IgnoreNormalizerJQTimeout.Value, mergeDiags)
	}
	other, _ := commandParametersSettings("other.yaml", "data", map[string]string{"controller.ignore.normalizer.jq.timeout": "10s"})
	if _, conflict := MergeDiscovered([]ArgoSettings{settings, other}); len(conflict) != 1 {
		t.Fatalf("MergeDiscovered() conflict diagnostics = %#v, want one", conflict)
	}
	// Spellings that the controller reads the same do not conflict.
	for _, pair := range [][2]string{{"60s", "1m"}, {"", "1s"}, {"0", "abc"}, {"-5s", ""}} {
		left, _ := commandParametersSettings("left.yaml", "data", map[string]string{"controller.ignore.normalizer.jq.timeout": pair[0]})
		right, _ := commandParametersSettings("right.yaml", "data", map[string]string{"controller.ignore.normalizer.jq.timeout": pair[1]})
		if _, diags := MergeDiscovered([]ArgoSettings{left, right}); len(diags) != 0 {
			t.Fatalf("MergeDiscovered(%q, %q) diagnostics = %#v, want none", pair[0], pair[1], diags)
		}
	}
}
