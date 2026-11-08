// Package submoduletest builds local Git repositories with submodules for
// tests. Every remote is a bare repository in a temporary directory, so no
// test needs the network.
package submoduletest

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Fixture is a superproject with one submodule, which has a submodule of
// its own:
//
//	parent: apps/top.yaml, .gitmodules, mods/child -> child.git
//	child:  child/app/cm.yaml, .gitmodules, deep -> grandchild.git
//	grandchild: deep/cm.yaml
//
// Every URL in a .gitmodules file is relative ("../child.git").
type Fixture struct {
	// Dir holds the bare repositories parent.git, child.git, and
	// grandchild.git.
	Dir string
	// Work is a clone of parent.git whose submodules are initialized: its
	// .git/modules holds the child and grandchild repositories.
	Work string
	// ParentURL, ChildURL, and GrandchildURL are file URLs of the bare
	// repositories.
	ParentURL     string
	ChildURL      string
	GrandchildURL string
	// ParentCommit, ChildCommit, and GrandchildCommit are the commits that
	// the fixture pins.
	ParentCommit     string
	ChildCommit      string
	GrandchildCommit string
}

// Git returns the git binary, or skips the test when there is none.
func Git(t testing.TB) string {
	t.Helper()
	bin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed")
	}
	return bin
}

// Run runs git in dir with an isolated configuration and returns its
// trimmed output. File transport is allowed for submodules, which the
// fixture's remotes need.
func Run(t testing.TB, dir string, args ...string) string {
	t.Helper()
	bin := Git(t)
	full := append([]string{"-c", "protocol.file.allow=always", "-c", "init.defaultBranch=main", "-c", "core.hooksPath=/dev/null", "-C", dir}, args...)
	cmd := exec.Command(bin, full...)
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_AUTHOR_NAME=drydock", "GIT_AUTHOR_EMAIL=drydock@example.invalid",
		"GIT_COMMITTER_NAME=drydock", "GIT_COMMITTER_EMAIL=drydock@example.invalid",
		"GIT_TERMINAL_PROMPT=0",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// WriteFile writes body to dir/rel, creating its parents.
func WriteFile(t testing.TB, dir, rel, body string) {
	t.Helper()
	target := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// NewRepo creates a work repository at dir/name with files, commits it,
// and clones it to the bare repository dir/name.git. It returns the
// commit.
func NewRepo(t testing.TB, dir, name string, files map[string]string) string {
	t.Helper()
	work := filepath.Join(dir, name+"-work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	Run(t, work, "init", "-q")
	for rel, body := range files {
		WriteFile(t, work, rel, body)
	}
	Run(t, work, "add", "-A")
	Run(t, work, "commit", "-q", "-m", "seed "+name)
	Run(t, dir, "clone", "-q", "--bare", work, filepath.Join(dir, name+".git"))
	return Run(t, work, "rev-parse", "HEAD")
}

// AddSubmodule adds the bare repository url as a submodule at path of the
// work repository of name below dir, with the URL written as rel in
// .gitmodules, commits, and pushes to name.git. It returns the commit.
func AddSubmodule(t testing.TB, dir, name, url, path, rel string) string {
	t.Helper()
	work := filepath.Join(dir, name+"-work")
	Run(t, work, "submodule", "add", "-q", url, path)
	Run(t, work, "config", "-f", ".gitmodules", "submodule."+path+".url", rel)
	Run(t, work, "add", "-A")
	Run(t, work, "commit", "-q", "-m", "add submodule "+path)
	Run(t, work, "push", "-q", filepath.Join(dir, name+".git"), "HEAD:main")
	return Run(t, work, "rev-parse", "HEAD")
}

// FileURL returns the file URL of path.
func FileURL(path string) string {
	return "file://" + filepath.ToSlash(path)
}

// New builds the Fixture.
func New(t testing.TB) Fixture {
	t.Helper()
	dir := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	f := Fixture{Dir: dir}
	f.GrandchildCommit = NewRepo(t, dir, "grandchild", map[string]string{"deep/cm.yaml": configMap("grandchild")})
	NewRepo(t, dir, "child", map[string]string{"child/app/cm.yaml": configMap("child")})
	f.ChildCommit = AddSubmodule(t, dir, "child", filepath.Join(dir, "grandchild.git"), "deep", "../grandchild.git")
	NewRepo(t, dir, "parent", map[string]string{"apps/top.yaml": configMap("top")})
	f.ParentCommit = AddSubmodule(t, dir, "parent", filepath.Join(dir, "child.git"), "mods/child", "../child.git")
	f.ParentURL = FileURL(filepath.Join(dir, "parent.git"))
	f.ChildURL = FileURL(filepath.Join(dir, "child.git"))
	f.GrandchildURL = FileURL(filepath.Join(dir, "grandchild.git"))
	f.Work = filepath.Join(dir, "clone")
	Run(t, dir, "clone", "-q", "--recurse-submodules", filepath.Join(dir, "parent.git"), f.Work)
	return f
}

func configMap(name string) string {
	return "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: " + name + "\n"
}
