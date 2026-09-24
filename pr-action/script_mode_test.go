package praction

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// action.yml invokes every helper by bare path ("${GITHUB_ACTION_PATH}/x.sh"),
// so a script committed as 0644 fails at runtime inside somebody else's
// workflow. Files written by tooling default to 0644, and no other gate in
// this repository looks at the mode bits.
func TestActionScriptsAreExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file mode bits are not meaningful on windows")
	}

	scripts, err := filepath.Glob("*.sh")
	if err != nil {
		t.Fatalf("glob *.sh: %v", err)
	}
	if len(scripts) == 0 {
		t.Fatal("no *.sh scripts found in pr-action")
	}

	for _, script := range scripts {
		info, err := os.Stat(script)
		if err != nil {
			t.Fatalf("stat %s: %v", script, err)
		}
		if info.Mode().Perm()&0o111 == 0 {
			t.Errorf("%s mode = %04o, want the executable bit set", script, info.Mode().Perm())
		}
	}
}
