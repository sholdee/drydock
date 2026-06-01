package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestCompletionCommandGeneratesShellCompletions(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "bash",
			args: []string{"completion", "bash"},
			want: "complete",
		},
		{
			name: "zsh",
			args: []string{"completion", "zsh"},
			want: "#compdef drydock",
		},
		{
			name: "fish",
			args: []string{"completion", "fish"},
			want: "complete -c drydock",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := NewRootCommand(VersionInfo{})
			cmd.SetArgs(tt.args)
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			cmd.SetOut(&stdout)
			cmd.SetErr(&stderr)

			if err := cmd.Execute(); err != nil {
				t.Fatalf("Execute() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
			}
			if stdout.Len() == 0 {
				t.Fatalf("stdout is empty")
			}
			if !strings.Contains(stdout.String(), tt.want) {
				t.Fatalf("stdout missing %q:\n%s", tt.want, stdout.String())
			}
			if stderr.String() != "" {
				t.Fatalf("stderr = %q, want empty", stderr.String())
			}
		})
	}
}

func TestCompletionCommandRejectsUnknownShell(t *testing.T) {
	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"completion", "powershell"})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err == nil {
		t.Fatalf("Execute() error = nil, want error")
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
}

func TestCompletionCommandRejectsExtraArgs(t *testing.T) {
	cmd := NewRootCommand(VersionInfo{})
	cmd.SetArgs([]string{"completion", "bash", "extra"})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if err := cmd.Execute(); err == nil {
		t.Fatalf("Execute() error = nil, want error")
	}
	if stdout.String() != "" {
		t.Fatalf("stdout = %q, want empty", stdout.String())
	}
}
