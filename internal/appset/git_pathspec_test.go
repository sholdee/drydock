package appset

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// gitPathspecTestFiles are the files of the repository that the pathspec
// tests list. They cover nested directories, hidden directories, names
// with glob characters, mixed case, and a name that only shares a prefix
// with a directory.
var gitPathspecTestFiles = []string{
	"apps/a/config.yaml",
	"apps/a/b/config.yaml",
	"apps/c/config.yaml",
	"apps/c/config.yml",
	"apps/top.yaml",
	"apps/sub/nested.yaml",
	"apps/.hidden/config.yaml",
	"apps-extra/config.yaml",
	"appsfile",
	"apps.yaml",
	"Apps/Upper.YAML",
	"glob/*",
	"glob/star*name",
	"glob/q?",
	"glob/[ab]",
	"glob/a",
	"glob/back\\slash",
	"glob/x-y",
	"glob/dir/*/file",
	"space dir/file one.json",
	".github/workflows/ci.yaml",
	"root.yaml",
	"deep/one/two/three/four.yaml",
	"deep/one/four.yaml",
}

// gitPathspecTestPatterns are compared with real `git ls-files`. Each one
// either lists files, lists none, or makes git fail.
var gitPathspecTestPatterns = []string{
	// The audit examples: "*" matches "/", and a directory is a prefix.
	"apps/*/config.yaml",
	"apps/*.yaml",
	"apps",
	"apps/",
	"apps/a",
	"app",
	"apps*",
	// Normalization.
	".",
	"./apps/*.yaml",
	"apps//a/config.yaml",
	"apps/../apps/c",
	"apps/a/..",
	"./",
	"apps/./c/config.yaml",
	// Patterns git refuses.
	"..",
	"../apps",
	"apps/../../x",
	"/apps",
	"/",
	":(foo)apps",
	":(glob",
	":#apps",
	":(literal,glob)apps",
	// Wildcards.
	"*",
	"*.yaml",
	"*config*",
	"?pps/top.yaml",
	"apps/?/config.yaml",
	"apps?a?config.yaml",
	"**",
	"**/config.yaml",
	"apps/**/config.yaml",
	"apps/**",
	"deep/**/four.yaml",
	"deep/*/four.yaml",
	"de**ur.yaml",
	"*/*/*.yaml",
	"[ab]pps/*",
	"[!a]pps/*",
	"[^a]pps/*",
	"[A-Z]pps/*",
	"[a-z]pps/top.yaml",
	"[[:upper:]]pps/*",
	"[[:alpha:]]pps/top.yaml",
	"[[:digit:]]*",
	"[[:bogus:]]*",
	"[a-",
	"apps/[",
	"glob/[ab]",
	"glob/\\[ab]",
	"glob/\\*",
	"glob/*",
	"glob/q?",
	"glob/q\\?",
	"glob/back\\\\slash",
	"glob/x[--z]y",
	"glob/[]]",
	"glob/[!]]",
	"glob/dir/\\*/file",
	"space dir/*",
	"*.json",
	".github",
	".*/**",
	"*/.hidden/*",
	"apps/*/b/*",
	"apps/a*",
	"apps/a*/config.yaml",
	"apps/*a",
	"*apps",
	"apps/top.yaml*",
	// Magic.
	":(glob)apps/*",
	":(glob)apps/*.yaml",
	":(glob)apps/**/config.yaml",
	":(glob)**/config.yaml",
	":(glob)apps/**",
	":(glob)**",
	":(glob)apps/**/",
	":(glob)a**s/top.yaml",
	":(glob)apps/*/config.yaml",
	":(literal)glob/*",
	":(literal)apps/*.yaml",
	":(icase)apps/a",
	":(icase)APPS/*.YAML",
	":(icase,glob)APPS/*",
	":(icase)[a]pps/top.yaml",
	":(icase)[A]PPS/top.yaml",
	":(icase)glob/\\Q?",
	":(top)apps/c",
	":/apps/c",
	":/./apps/c",
	":!apps",
	":^apps",
	":(exclude)apps/*",
	":(exclude,glob)apps/*",
	":!",
	":",
	"::apps/c",
	":apps/c",
	":!/apps",
	// Prefix magic: strtol, no normalization, a case-sensitive prefix
	// under icase, and git's BUG check.
	":(prefix:0)./apps",
	":(prefix:)apps",
	":(prefix:99)apps",
	":(prefix:4)apps/*.yaml",
	":(prefix:5)apps/*.yaml",
	":(prefix:2)a*",
	":(icase,prefix:4)APPS/*",
	":(icase,prefix:4)apps/TOP.yaml",
	":(icase,prefix:1)aPPS/top.yaml",
	":(prefix:x)apps",
	":(prefix: 2)apps",
	":(prefix:+2)apps",
	":(prefix:-1)./apps",
	":(prefix:2,prefix:0)apps",
	":(literal,prefix:4)apps",
	// strtol clamps to a long, and git keeps the low 32 bits in an int.
	":(prefix:4294967296)apps",
	":(prefix:4294967302)apps/a/config.yaml",
	":(prefix:2147483648)apps",
	":(prefix:99999999999999999999)apps",
	":(prefix:-4294967296)apps",
	":(prefix:-99999999999999999999)apps",
	// Gitlinks match as directories, also with a trailing slash.
	"mods/sub",
	"mods/sub/",
	"mods/",
	"mods/*",
	"MODS/SUB/",
	":(icase)MODS/SUB/",
	":(literal)mods/sub/",
	":(glob)mods/*/",
	"mods/sub/x",
	"mods/su",
	":!mods/sub/",
}

// gitPathspecTestGitlinks are the gitlinks of the pathspec test repository.
var gitPathspecTestGitlinks = []string{
	"mods/sub",
	"mods/other",
}

// TestGitPathspecMatchesGitLsFiles proves the pathspec port against the git
// CLI: for every pattern, the files it matches are the files that
// `git ls-files --full-name -z -- <pattern>` lists in a repository that
// tracks gitPathspecTestFiles, and it fails exactly when git fails. The
// Argo CD v3.6.1 Git files generator runs that command
// (util/git/client.go:763-770).
func TestGitPathspecMatchesGitLsFiles(t *testing.T) {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not on PATH")
	}
	repo := t.TempDir()
	for _, rel := range gitPathspecTestFiles {
		abs := filepath.Join(repo, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte("x: y\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runTestGit(t, gitBin, repo, "init", "-q")
	runTestGit(t, gitBin, repo, "add", "-A")
	// Gitlinks: index entries of mode 160000, which ls-files matches as
	// directories.
	for _, link := range gitPathspecTestGitlinks {
		runTestGit(t, gitBin, repo, "update-index", "--add", "--cacheinfo", "160000,1111111111111111111111111111111111111111,"+link)
	}
	tracked := strings.Split(strings.TrimSuffix(runTestGit(t, gitBin, repo, "ls-files", "-z"), "\x00"), "\x00")
	if len(tracked) != len(gitPathspecTestFiles)+len(gitPathspecTestGitlinks) {
		t.Fatalf("git tracks %d files, want %d: %q", len(tracked), len(gitPathspecTestFiles), tracked)
	}

	for _, pattern := range gitPathspecTestPatterns {
		t.Run(pattern, func(t *testing.T) {
			want, gitErr := gitLsFiles(gitBin, repo, pattern)
			spec, err := parseGitPathspec(pattern)
			if gitErr != nil {
				if err == nil {
					t.Fatalf("git fails (%v), drydock parses the pattern", gitErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("git lists %q, drydock fails: %v", want, err)
			}
			var got []string
			for _, name := range tracked {
				if spec.matchesEntry(name, slices.Contains(gitPathspecTestGitlinks, name)) {
					got = append(got, name)
				}
			}
			slices.Sort(got)
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Fatalf("matches = %q, git ls-files = %q", got, want)
			}
		})
	}
}

func runTestGit(t *testing.T, gitBin, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(gitBin, append([]string{"-C", dir, "-c", "core.quotepath=false"}, args...)...)
	cmd.Env = testGitEnv()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func gitLsFiles(gitBin, dir, pattern string) ([]string, error) {
	cmd := exec.Command(gitBin, "-C", dir, "ls-files", "--full-name", "-z", "--", pattern)
	cmd.Env = testGitEnv()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, errors.New(strings.TrimSpace(stderr.String()))
	}
	out := strings.TrimSuffix(stdout.String(), "\x00")
	if out == "" {
		return nil, nil
	}
	return strings.Split(out, "\x00"), nil
}

// testGitEnv runs git without the user's configuration and without the
// pathspec environment variables, as the Argo CD repo-server runs it.
func testGitEnv() []string {
	var env []string
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "GIT_") {
			continue
		}
		env = append(env, entry)
	}
	return append(env,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=drydock", "GIT_AUTHOR_EMAIL=drydock@example.invalid",
		"GIT_COMMITTER_NAME=drydock", "GIT_COMMITTER_EMAIL=drydock@example.invalid",
	)
}
