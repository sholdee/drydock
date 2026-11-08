// Package submoduletest builds local Git repositories with submodules for
// tests. Every remote is a bare repository in a temporary directory, so no
// test needs the network.
package submoduletest

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
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

// LocalPathURLFixture builds a superproject whose .gitmodules URL is
// prefix + "/../../../../../../../../.." + the path of a secret bare
// repository, such as "x y:" or "git@x y:". git reads that URL as ssh to
// the host "x y". It returns the file URL of the superproject.
func LocalPathURLFixture(t testing.TB, prefix string) string {
	t.Helper()
	dir := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	secret := NewRepo(t, dir, "secret", map[string]string{"app/cm.yaml": configMap("leaked")})
	NewRepo(t, dir, "parent", map[string]string{"apps/a.yaml": configMap("a")})
	work := filepath.Join(dir, "parent-work")
	url := prefix + "/../../../../../../../../.." + filepath.Join(dir, "secret.git")
	WriteFile(t, work, ".gitmodules", "[submodule \"sub\"]\n\tpath = sub\n\turl = "+url+"\n")
	Run(t, work, "update-index", "--add", "--cacheinfo", "160000,"+secret+",sub")
	Run(t, work, "add", ".gitmodules")
	Run(t, work, "commit", "-q", "-m", "local path submodule")
	Run(t, work, "push", "-q", filepath.Join(dir, "parent.git"), "HEAD:main")
	return FileURL(filepath.Join(dir, "parent.git"))
}

// UnreachableCommitFixture builds a superproject whose submodule pins a
// commit that no branch or tag of the submodule's repository holds: the
// branch that held it was deleted. The repository serves unadvertised
// commits (uploadpack.allowAnySHA1InWant). It returns the file URL of the
// superproject.
func UnreachableCommitFixture(t testing.TB) string {
	t.Helper()
	dir := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	NewRepo(t, dir, "child", map[string]string{"app/cm.yaml": configMap("main")})
	work := filepath.Join(dir, "child-work")
	Run(t, work, "checkout", "-q", "-b", "feature")
	WriteFile(t, work, "app/cm.yaml", configMap("feature"))
	Run(t, work, "commit", "-q", "-am", "feature")
	pinned := Run(t, work, "rev-parse", "HEAD")
	Run(t, work, "push", "-q", filepath.Join(dir, "child.git"), "feature")
	Run(t, filepath.Join(dir, "child.git"), "branch", "-D", "feature")
	// go-git fetches with protocol v0, where upload-pack serves an
	// unadvertised commit only with this setting, as hosting services do.
	Run(t, filepath.Join(dir, "child.git"), "config", "uploadpack.allowAnySHA1InWant", "true")
	NewRepo(t, dir, "parent", map[string]string{"top.yaml": configMap("top")})
	parent := filepath.Join(dir, "parent-work")
	WriteFile(t, parent, ".gitmodules", "[submodule \"sub\"]\n\tpath = sub\n\turl = ../child.git\n")
	Run(t, parent, "update-index", "--add", "--cacheinfo", "160000,"+pinned+",sub")
	Run(t, parent, "add", ".gitmodules")
	Run(t, parent, "commit", "-q", "-m", "pin feature")
	Run(t, parent, "push", "-q", filepath.Join(dir, "parent.git"), "HEAD:main")
	return FileURL(filepath.Join(dir, "parent.git"))
}

// SubmoduleURLFixture builds a superproject with one gitlink "sub" whose
// .gitmodules URL is url, pinned to a commit of a local child repository.
// It returns the file URL of the superproject.
func SubmoduleURLFixture(t testing.TB, url string) string {
	t.Helper()
	dir := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	child := NewRepo(t, dir, "child", map[string]string{"app/cm.yaml": configMap("child")})
	NewRepo(t, dir, "parent", map[string]string{"top.yaml": configMap("top")})
	work := filepath.Join(dir, "parent-work")
	WriteFile(t, work, ".gitmodules", "[submodule \"sub\"]\n\tpath = sub\n\turl = "+url+"\n")
	Run(t, work, "update-index", "--add", "--cacheinfo", "160000,"+child+",sub")
	Run(t, work, "add", ".gitmodules")
	Run(t, work, "commit", "-q", "-m", "submodule")
	Run(t, work, "push", "-q", filepath.Join(dir, "parent.git"), "HEAD:main")
	return FileURL(filepath.Join(dir, "parent.git"))
}

// ServeGitDaemon serves every repository below dir with `git daemon` on a
// free local port and returns the git:// base URL. It skips the test when
// git daemon does not start. The daemon stops with the test.
func ServeGitDaemon(t testing.TB, dir string) string {
	t.Helper()
	bin := Git(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("no local port: %v", err)
	}
	addr, ok := listener.Addr().(*net.TCPAddr)
	_ = listener.Close()
	if !ok {
		t.Skip("no TCP port")
	}
	port := addr.Port
	cmd := exec.Command(bin, "daemon", "--reuseaddr", "--export-all", "--listen=127.0.0.1",
		"--port="+strconv.Itoa(port), "--base-path="+dir, dir)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
	if err := cmd.Start(); err != nil {
		t.Skipf("git daemon: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	address := "127.0.0.1:" + strconv.Itoa(port)
	for range 100 {
		if conn, err := net.Dial("tcp", address); err == nil {
			_ = conn.Close()
			return "git://" + address
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Skip("git daemon did not start")
	return ""
}

// ManyPinsFixture builds a superproject with count gitlinks sub0, sub1,
// ... into one child repository, each pinned to another commit of it, with
// the relative URL "../child.git". It returns the file URL of the
// superproject.
func ManyPinsFixture(t testing.TB, count int) string {
	t.Helper()
	dir := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	NewRepo(t, dir, "child", map[string]string{"app/cm.yaml": configMap("c0")})
	child := filepath.Join(dir, "child-work")
	commits := []string{Run(t, child, "rev-parse", "HEAD")}
	for i := 1; i < count; i++ {
		WriteFile(t, child, "app/cm.yaml", configMap("c"+strconv.Itoa(i)))
		Run(t, child, "commit", "-q", "-am", "c"+strconv.Itoa(i))
		commits = append(commits, Run(t, child, "rev-parse", "HEAD"))
	}
	Run(t, child, "push", "-q", filepath.Join(dir, "child.git"), "HEAD:main")
	NewRepo(t, dir, "parent", map[string]string{"top.yaml": configMap("top")})
	parent := filepath.Join(dir, "parent-work")
	var modules strings.Builder
	for i, commit := range commits {
		name := "sub" + strconv.Itoa(i)
		modules.WriteString("[submodule \"" + name + "\"]\n\tpath = " + name + "\n\turl = ../child.git\n")
		Run(t, parent, "update-index", "--add", "--cacheinfo", "160000,"+commit+","+name)
	}
	WriteFile(t, parent, ".gitmodules", modules.String())
	Run(t, parent, "add", ".gitmodules")
	Run(t, parent, "commit", "-q", "-m", "many pins")
	Run(t, parent, "push", "-q", filepath.Join(dir, "parent.git"), "HEAD:main")
	return FileURL(filepath.Join(dir, "parent.git"))
}
