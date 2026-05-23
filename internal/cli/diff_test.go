package cli

import "testing"

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
