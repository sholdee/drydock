package digestpath

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestCanonicalVariants(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		git     bool
		want    string
		wantErr bool
	}{
		{name: "plain", value: "a/b", want: "a/b"},
		{name: "backslashes", value: `a\b`, want: "a/b"},
		{name: "redundant", value: "a/./b/../c", want: "a/c"},
		{name: "dot", value: ".", want: "."},
		{name: "empty-git-allowed", value: "", git: true, want: "."},
		{name: "empty-filesystem-rejected", value: "", wantErr: true},
		{name: "nul", value: "a\x00b", wantErr: true},
		{name: "absolute", value: "/etc", wantErr: true},
		{name: "parent", value: "../x", wantErr: true},
		{name: "parent-exact", value: "..", wantErr: true},
		{name: "drive", value: `C:\x`, wantErr: true},
		{name: "lowercase-drive", value: `c:\x`, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			canonicalize := CanonicalFilesystemPath
			if tc.git {
				canonicalize = CanonicalGitPath
			}
			got, err := canonicalize(tc.value)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("canonicalize(%q) = %q, want error", tc.value, got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("canonicalize(%q) = %q, %v, want %q", tc.value, got, err, tc.want)
			}
		})
	}
}

func TestWriteRecordFraming(t *testing.T) {
	digest := sha256.New()
	WriteRecord(digest, "path", "a/b", "present")
	got := hex.EncodeToString(digest.Sum(nil))

	manual := sha256.New()
	manual.Write([]byte("path\x00a/b\x00present\x00\n"))
	want := hex.EncodeToString(manual.Sum(nil))
	if got != want {
		t.Fatalf("WriteRecord framing = %s, want %s", got, want)
	}
}
