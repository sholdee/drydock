package cli

import (
	"bytes"
	"strings"
	"testing"
)

type assertErr struct{}

func (assertErr) Error() string {
	return "assert error"
}

func TestExitCodeForDiff(t *testing.T) {
	tests := []struct {
		name                string
		err                 error
		disableDiffExitCode bool
		hasDiff             bool
		want                int
	}{
		{
			name:    "diff found",
			hasDiff: true,
			want:    1,
		},
		{
			name: "no diff",
			want: 0,
		},
		{
			name: "error",
			err:  assertErr{},
			want: 2,
		},
		{
			name:                "diff exit code disabled",
			disableDiffExitCode: true,
			hasDiff:             true,
			want:                0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := exitCode(tt.err, tt.disableDiffExitCode, tt.hasDiff); got != tt.want {
				t.Fatalf("exitCode(%v, %v, %v) = %d, want %d", tt.err, tt.disableDiffExitCode, tt.hasDiff, got, tt.want)
			}
		})
	}
}

func TestChartCacheFlagsAreRegistered(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{
			name: "build apps",
			args: []string{"build", "apps", "--offline", "--refresh-charts", "--chart-cache-dir", "/tmp/charts", "--path", "missing"},
		},
		{
			name: "diff apps",
			args: []string{"diff", "apps", "--offline", "--refresh-charts", "--chart-cache-dir", "/tmp/charts", "--path", "missing", "--path-orig", "base"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := NewRootCommand(VersionInfo{})
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			cmd.SetOut(&stdout)
			cmd.SetErr(&stderr)
			cmd.SetArgs(tt.args)

			err := cmd.Execute()
			if err == nil {
				t.Fatal("Execute() error = nil, want runtime error after parsing")
			}
			if strings.Contains(err.Error(), "unknown flag") {
				t.Fatalf("Execute() error = %v, want registered chart cache flags", err)
			}
		})
	}
}
