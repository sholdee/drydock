package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestParseRepoMaps(t *testing.T) {
	maps, err := parseRepoMaps([]string{
		"https://github.com/example/values.git=/tmp/values",
		"oci://ignored=still-accepted-as-url-string",
	})
	if err != nil {
		t.Fatalf("parseRepoMaps() error = %v", err)
	}
	if len(maps) != 2 {
		t.Fatalf("len(maps) = %d, want 2", len(maps))
	}
	if maps[0].URL != "https://github.com/example/values.git" || maps[0].Path != "/tmp/values" {
		t.Fatalf("maps[0] = %#v", maps[0])
	}
	if maps[1].URL != "oci://ignored" || maps[1].Path != "still-accepted-as-url-string" {
		t.Fatalf("maps[1] = %#v", maps[1])
	}
}

func TestParseRepoMapsRejectsInvalidMapping(t *testing.T) {
	_, err := parseRepoMaps([]string{"https://github.com/example/repo"})
	if err == nil {
		t.Fatal("parseRepoMaps() error = nil, want invalid mapping error")
	}
}

func TestRepresentativeCommandsExposeFocusedFlagGroups(t *testing.T) {
	tests := []struct {
		name    string
		command []string
		flags   []string
	}{
		{
			name:    "build apps common acquisition discovery fixtures filters",
			command: []string{"build", "apps"},
			flags:   []string{"offline", "repo-map", "enable-avp-compat", "enable-ksops-compat", "enable-plugins", "plugin-cache-dir", "plugin-policy-path", "plugin-policy-ref", "plugin-policy-repo", "appset-provider-fixture", "skip-kind", "project-diagnostics"},
		},
		{
			name:    "diff apps common specialized diff flags",
			command: []string{"diff", "apps"},
			flags:   []string{"offline", "repo-map", "plugin-cache-dir", "appset-provider-fixture", "skip-kind", "project-diagnostics", "ref-orig", "show-ignored-fields"},
		},
		{
			name:    "test apps common specialized lua flag",
			command: []string{"test", "apps"},
			flags:   []string{"offline", "repo-map", "plugin-cache-dir", "appset-provider-fixture", "skip-kind", "project-diagnostics", "skip-lua-health"},
		},
		{
			name:    "diag common flags",
			command: []string{"diag"},
			flags:   []string{"offline", "repo-map", "plugin-cache-dir", "appset-provider-fixture", "skip-kind", "project-diagnostics"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := findCLICommand(t, tt.command...)
			for _, flagName := range tt.flags {
				if cmd.Flags().Lookup(flagName) == nil {
					t.Fatalf("%s missing --%s", cmd.CommandPath(), flagName)
				}
			}
		})
	}
}

func TestEnablePluginsHelpDescribesTrustedCommandPolicy(t *testing.T) {
	cmd := findCLICommand(t, "build", "apps")
	flag := cmd.Flags().Lookup("enable-plugins")
	if flag == nil {
		t.Fatal("build apps missing --enable-plugins")
	}
	for _, want := range []string{"trusted", "exec", "container", "plugin policy"} {
		if !strings.Contains(flag.Usage, want) {
			t.Fatalf("--enable-plugins usage = %q, want %q", flag.Usage, want)
		}
	}
}

func TestEnableKSOPSCompatHelpDescribesPlaceholderRendering(t *testing.T) {
	cmd := findCLICommand(t, "build", "apps")
	flag := cmd.Flags().Lookup("enable-ksops-compat")
	if flag == nil {
		t.Fatal("build apps missing --enable-ksops-compat")
	}
	for _, want := range []string{"KSOPS", "placeholder", "decryption"} {
		if !strings.Contains(flag.Usage, want) {
			t.Fatalf("--enable-ksops-compat usage = %q, want %q", flag.Usage, want)
		}
	}
}

func TestRequestOptionsFromFlagsCarriesKSOPSCompat(t *testing.T) {
	flags := defaultCommonFlags()
	flags.enableKSOPSCompat = true

	opts := requestOptionsFromFlags(flags, nil)

	if !opts.EnableKSOPSCompat {
		t.Fatal("opts.EnableKSOPSCompat = false, want true")
	}
	if buildReq := opts.Build(); !buildReq.EnableKSOPSCompat {
		t.Fatal("opts.Build() EnableKSOPSCompat = false, want true")
	}
}

func TestRequestOptionsFromFlagsCarriesNoCRDScope(t *testing.T) {
	flags := defaultCommonFlags()
	flags.noCRDScope = true

	opts := requestOptionsFromFlags(flags, nil)

	if !opts.NoCRDScope {
		t.Fatal("opts.NoCRDScope = false, want true")
	}
	if buildReq := opts.Build(); !buildReq.Disabled {
		t.Fatal("opts.Build() CRD scope Disabled = false, want true")
	}
	if diffReq := opts.Diff(); !diffReq.Disabled {
		t.Fatal("opts.Diff() CRD scope Disabled = false, want true")
	}
}

func findCLICommand(t *testing.T, args ...string) *cobra.Command {
	t.Helper()

	root := NewRootCommand(VersionInfo{})
	cmd, _, err := root.Find(args)
	if err != nil {
		t.Fatalf("Find(%v) error = %v", args, err)
	}
	if cmd == root {
		t.Fatalf("Find(%v) returned root command", args)
	}
	return cmd
}
