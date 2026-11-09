package config

import (
	"reflect"
	"strings"
	"testing"
)

// ParseComponentArgs reads the setting flags as pflag v1.0.10 parses the
// command line of the Argo CD program.
func TestParseComponentArgs(t *testing.T) {
	for _, tc := range []struct {
		name      string
		component ArgoCDComponent
		command   []string
		args      []string
		want      []ComponentFlag
		notes     []string
	}{
		{
			name:      "boolean flag without a value is true",
			component: RepoServerComponent,
			args:      []string{"/usr/local/bin/argocd-repo-server", "--port=8081", "--allow-oob-symlinks"},
			want:      []ComponentFlag{{Flag: "allow-oob-symlinks", Value: "true"}},
		},
		{
			name:      "boolean flag reads its value with strconv.ParseBool",
			component: RepoServerComponent,
			args:      []string{"/usr/local/bin/argocd-repo-server", "--include-hidden-directories=0", "--allow-oob-symlinks=T"},
			want:      []ComponentFlag{{Flag: "allow-oob-symlinks", Value: "true"}, {Flag: "include-hidden-directories", Value: "false"}},
		},
		{
			name:      "boolean flag does not take the next argument",
			component: RepoServerComponent,
			args:      []string{"/usr/local/bin/argocd-repo-server", "--allow-oob-symlinks", "false"},
			want:      []ComponentFlag{{Flag: "allow-oob-symlinks", Value: "true"}},
		},
		{
			name:      "string flag takes the next argument and the last one wins",
			component: RepoServerComponent,
			args:      []string{"/usr/local/bin/argocd-repo-server", "--max-combined-directory-manifests-size", "5M", "--max-combined-directory-manifests-size=7M"},
			want:      []ComponentFlag{{Flag: "max-combined-directory-manifests-size", Value: "7M"}},
		},
		{
			name:      "another value flag takes a setting flag as its value",
			component: RepoServerComponent,
			args:      []string{"/usr/local/bin/argocd-repo-server", "--otlp-address", "--allow-oob-symlinks"},
		},
		{
			name:      "another boolean flag takes no value",
			component: RepoServerComponent,
			args:      []string{"/usr/local/bin/argocd-repo-server", "--disable-tls", "--allow-oob-symlinks"},
			want:      []ComponentFlag{{Flag: "allow-oob-symlinks", Value: "true"}},
		},
		{
			name:      "double dash ends the flags",
			component: RepoServerComponent,
			args:      []string{"/usr/local/bin/argocd-repo-server", "--", "--allow-oob-symlinks"},
		},
		{
			name:      "command and args make one command line",
			component: ApplicationSetControllerComponent,
			command:   []string{"argocd-applicationset-controller"},
			args:      []string{"--enable-progressive-syncs", "-n", "--enable-new-git-file-globbing"},
			want:      []ComponentFlag{{Flag: "enable-progressive-syncs", Value: "true"}},
		},
		{
			name:      "string slice flags add their fields",
			component: ApplicationControllerComponent,
			args:      []string{"/usr/local/bin/argocd-application-controller", "--application-namespaces=a,b", "--application-namespaces", "c"},
			want:      []ComponentFlag{{Flag: "application-namespaces", Value: "a,b,c"}},
		},
		{
			name:      "an invalid boolean stops the program",
			component: ApplicationSetControllerComponent,
			args:      []string{"/usr/local/bin/argocd-applicationset-controller", "--enable-progressive-syncs=yes"},
			notes:     []string{`flag --enable-progressive-syncs has the value "yes", which is not a boolean, so argocd-applicationset-controller does not start`},
		},
		{
			name:      "an invalid duration stops the program",
			component: ApplicationControllerComponent,
			args:      []string{"/usr/local/bin/argocd-application-controller", "--ignore-normalizer-jq-execution-timeout-seconds=5"},
			notes:     []string{`flag --ignore-normalizer-jq-execution-timeout-seconds has the value "5", which is not a duration, so argocd-application-controller does not start`},
		},
		{
			name:      "a $(VAR) reference is not known",
			component: RepoServerComponent,
			args:      []string{"/usr/local/bin/argocd-repo-server", "--max-combined-directory-manifests-size=$(SIZE)"},
			notes:     []string{"flag --max-combined-directory-manifests-size has a $(VAR) reference, which the kubelet fills in from the container environment"},
		},
		{
			name:      "a $(VAR) argument in flag position can become a flag",
			component: RepoServerComponent,
			args:      []string{"/usr/local/bin/argocd-repo-server", "$(EXTRA_FLAGS)"},
			notes:     []string{`the argument "$(EXTRA_FLAGS)" has a $(VAR) reference, which the kubelet fills in from the container environment, so it can set a flag`},
		},
		{
			name:      "an unknown flag stops the program",
			component: ApplicationSetControllerComponent,
			args:      []string{"/usr/local/bin/argocd-applicationset-controller", "--no-such-flag", "--enable-progressive-syncs"},
			want:      []ComponentFlag{{Flag: "enable-progressive-syncs", Value: "true"}},
			notes:     []string{"flag --no-such-flag is not a flag of argocd-applicationset-controller, so argocd-applicationset-controller does not start"},
		},
		{
			name:      "an unknown shorthand stops the program",
			component: RepoServerComponent,
			args:      []string{"/usr/local/bin/argocd-repo-server", "-n", "argocd"},
			notes:     []string{"flag -n is not a flag of argocd-repo-server, so argocd-repo-server does not start"},
		},
		{
			name:      "help stops the program",
			component: RepoServerComponent,
			args:      []string{"/usr/local/bin/argocd-repo-server", "--help"},
			notes:     []string{"flag --help prints the help of argocd-repo-server, so argocd-repo-server does not start"},
		},
		{
			name:      "a known value flag takes the next argument",
			component: ApplicationControllerComponent,
			args:      []string{"/usr/local/bin/argocd-application-controller", "--kubeconfig", "--application-namespaces", "-n", "argocd"},
		},
		{
			name:      "another program is not read",
			component: RepoServerComponent,
			command:   []string{"sh", "-c"},
			args:      []string{"exec argocd-repo-server --allow-oob-symlinks"},
			notes:     []string{`the container runs "sh", not argocd-repo-server, so drydock does not read its flags`},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			flags, notes := ParseComponentArgs(tc.component, tc.command, tc.args)
			if !reflect.DeepEqual(flags, tc.want) {
				t.Errorf("flags = %#v, want %#v", flags, tc.want)
			}
			if strings.Join(notes, "\n") != strings.Join(tc.notes, "\n") {
				t.Errorf("notes = %q, want %q", notes, tc.notes)
			}
		})
	}
}

// Every boolean flag of each program takes no separate value, so a setting
// flag after it is read. A boolean flag that the tables miss would take the
// setting flag as its value.
func TestParseComponentArgsReadsEveryBooleanFlagAsBoolean(t *testing.T) {
	next := map[ArgoCDComponent][]string{
		RepoServerComponent:               {"--allow-oob-symlinks"},
		ApplicationControllerComponent:    {"--application-namespaces", "team-a"},
		ApplicationSetControllerComponent: {"--enable-progressive-syncs"},
	}
	for component, flags := range componentBoolFlags {
		for _, flag := range flags {
			if flag == "help" {
				continue
			}
			t.Run(string(component)+"/"+flag, func(t *testing.T) {
				args := append([]string{"/usr/local/bin/" + string(component), "--" + flag}, next[component]...)
				got, notes := ParseComponentArgs(component, nil, args)
				if len(got) != 1 || len(notes) != 0 {
					t.Fatalf("ParseComponentArgs(%q) = %#v, %q, want the setting flag after the boolean flag", args, got, notes)
				}
			})
		}
	}
}
