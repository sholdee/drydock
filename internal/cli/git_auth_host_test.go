package cli

import (
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sholdee/drydock/internal/app"
	"github.com/sholdee/drydock/internal/gittree/submoduletest"
)

// TestGitCredentialsNeedAuthHost checks that every command that takes the
// --git-* HTTP credentials fails before it fetches anything when no
// --git-auth-host names a host that receives them. The error names the
// new flag and no credential value.
func TestGitCredentialsNeedAuthHost(t *testing.T) {
	fixtureRoot := homeOpsPatternFixtureRoot("remote-kustomize")
	current := filepath.Join(fixtureRoot, "current")
	baseline := filepath.Join(fixtureRoot, "baseline")
	commands := map[string][]string{
		"build apps":  {"build", "apps", "--path", current},
		"build app":   {"build", "app", "remote-kustomize", "--path", current},
		"test apps":   {"test", "apps", "--path", current},
		"test app":    {"test", "app", "remote-kustomize", "--path", current},
		"get apps":    {"get", "apps", "--path", current},
		"get images":  {"get", "images", "--path", current},
		"diag":        {"diag", "--path", current},
		"diff apps":   {"diff", "apps", "--path-orig", baseline, "--path", current},
		"diff app":    {"diff", "app", "remote-kustomize", "--path-orig", baseline, "--path", current},
		"diff images": {"diff", "images", "--path-orig", baseline, "--path", current},
	}
	credentials := map[string][]string{
		"bearer token": {"--git-bearer-token", "fixture-secret-token"},
		"basic auth":   {"--git-username", "fixture-user", "--git-password", "fixture-secret-password"},
	}
	for commandName, command := range commands {
		for credentialName, flags := range credentials {
			t.Run(commandName+"/"+credentialName, func(t *testing.T) {
				acquirer := &recordingCLIRemoteAcquirer{}
				gitAcquirer := &recordingCLIGitAcquirer{path: t.TempDir()}
				args := append(append([]string{}, command...), "--offline", "--remote-cache-dir", t.TempDir(), "--git-cache-dir", t.TempDir())
				args = append(args, flags...)
				var stdout, stderr bytes.Buffer
				cmd := NewRootCommandWithDependencies(VersionInfo{}, Dependencies{Orchestrator: app.Orchestrator{RemoteResourceAcquirer: acquirer, GitAcquirer: gitAcquirer}})
				cmd.SetArgs(args)
				cmd.SetOut(&stdout)
				cmd.SetErr(&stderr)
				err := cmd.Execute()
				if err == nil || !strings.Contains(err.Error(), "--git-auth-host") {
					t.Fatalf("Execute() error = %v, want an error that names --git-auth-host\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
				}
				output := err.Error() + stdout.String() + stderr.String()
				for _, secret := range []string{"fixture-secret-token", "fixture-secret-password"} {
					if strings.Contains(output, secret) {
						t.Fatalf("output leaks %q:\n%s", secret, output)
					}
				}
				if requests, _ := acquirer.records(); len(requests) != 0 {
					t.Fatalf("remote acquire calls = %d, want 0", len(requests))
				}
				if len(gitAcquirer.options) != 0 {
					t.Fatalf("git acquire calls = %d, want 0", len(gitAcquirer.options))
				}
			})
		}
	}
}

// TestBuildAppsSendsGitCredentialsOnlyToAuthHosts runs `drydock build
// apps` with the default Git acquirer against two local smart HTTP Git
// servers. One Application uses the listed server and gets the token. The
// other Application, which a pull request could add, uses the other
// server and gets no Authorization header.
func TestBuildAppsSendsGitCredentialsOnlyToAuthHosts(t *testing.T) {
	repos := t.TempDir()
	submoduletest.NewRepo(t, repos, "repo", map[string]string{"manifests/cm.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: fetched\n"})
	listed := submoduletest.ServeSmartHTTP(t, repos)
	other := submoduletest.ServeSmartHTTP(t, repos)
	root := t.TempDir()
	for name, server := range map[string]*submoduletest.HTTPServer{"trusted": listed, "attacker": other} {
		writeCLITestFile(t, filepath.Join(root, "apps", name+".yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: `+name+`
  namespace: argocd
spec:
  source:
    repoURL: `+server.URL+`/repo.git
    targetRevision: main
    path: manifests
  destination:
    name: in-cluster
    namespace: `+name+`
`)
	}
	var stdout, stderr bytes.Buffer
	cmd := NewRootCommandWithDependencies(VersionInfo{}, Dependencies{Orchestrator: app.Orchestrator{}})
	cmd.SetArgs([]string{
		"build", "apps",
		"--path", root,
		"--git-cache-dir", t.TempDir(),
		"--render-cache=false",
		"--git-bearer-token", "cli-secret-token",
		"--git-auth-host", listed.URL,
	})
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
	}
	if got := strings.Count(stdout.String(), "name: fetched"); got != 2 {
		t.Fatalf("stdout renders %d ConfigMaps, want 2:\n%s", got, stdout.String())
	}
	if len(listed.Requests()) == 0 || len(other.Requests()) == 0 {
		t.Fatalf("requests: listed %d, other %d; want both servers used", len(listed.Requests()), len(other.Requests()))
	}
	for _, request := range listed.Requests() {
		if request.Authorization != "Bearer cli-secret-token" {
			t.Fatalf("listed host request %s %s has Authorization %q, want the token", request.Method, request.Path, request.Authorization)
		}
	}
	if got := other.AuthorizationHeaders(); len(got) != 0 {
		t.Fatalf("other host received Authorization headers %q, want none", got)
	}
}

// writeCLIGitApplication writes an Application named name whose source is
// repoURL at revision main, path manifests.
func writeCLIGitApplication(t *testing.T, root, name, repoURL string) {
	t.Helper()
	writeCLITestFile(t, filepath.Join(root, "apps", name+".yaml"), `apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: `+name+`
  namespace: argocd
spec:
  source:
    repoURL: "`+repoURL+`"
    targetRevision: main
    path: manifests
  destination:
    name: in-cluster
    namespace: `+name+`
`)
}

// TestBuildAppsNeverSendsTheTokenThroughACacheKeyCollision runs the attack
// of the security review through `drydock build apps --refresh-git`. One
// Application uses "<listed>?@<attacker>/repo.git", which net/url keys as
// the listed host. Another uses the listed host. The listed server serves
// its repository at the root, so both share one cache key. The test runs
// both orders in one run, and the poisoning Application in an earlier run
// with the same --git-cache-dir. The attacker host never gets the token,
// and no output holds the attacker content. A run with only the listed
// Application renders the listed content. Before the fix, drydock cloned
// the attacker repository into the entry of the listed host, served its
// content for the listed Application, and fetched it with the token.
func TestBuildAppsNeverSendsTheTokenThroughACacheKeyCollision(t *testing.T) {
	for _, tc := range []struct {
		name string
		runs [][2]string
	}{
		{name: "poison first in one run", runs: [][2]string{{"a-poison", "b-listed"}}},
		{name: "listed first in one run", runs: [][2]string{{"b-poison", "a-listed"}}},
		{name: "poison in an earlier run", runs: [][2]string{{"a-poison", ""}, {"", "a-listed"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			listedDir := t.TempDir()
			submoduletest.NewRepo(t, listedDir, "repo", map[string]string{"manifests/cm.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: listed-content\n"})
			listed := submoduletest.ServeSmartHTTP(t, filepath.Join(listedDir, "repo.git"))
			attackerDir := t.TempDir()
			submoduletest.NewRepo(t, attackerDir, "repo", map[string]string{"manifests/cm.yaml": "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: attacker-content\n"})
			attacker := submoduletest.ServeSmartHTTP(t, attackerDir)
			poisonURL := listed.URL + "?@" + strings.TrimPrefix(attacker.URL, "http://") + "/repo.git"
			cacheDir := t.TempDir()
			for _, run := range tc.runs {
				root := t.TempDir()
				if run[0] != "" {
					writeCLIGitApplication(t, root, run[0], poisonURL)
				}
				if run[1] != "" {
					writeCLIGitApplication(t, root, run[1], listed.URL)
				}
				var stdout, stderr bytes.Buffer
				cmd := NewRootCommandWithDependencies(VersionInfo{}, Dependencies{Orchestrator: app.Orchestrator{}})
				cmd.SetArgs([]string{
					"build", "apps", "--path", root, "--git-cache-dir", cacheDir, "--render-cache=false", "--refresh-git",
					"--git-bearer-token", "cli-secret-token", "--git-auth-host", listed.URL,
				})
				cmd.SetOut(&stdout)
				cmd.SetErr(&stderr)
				err := cmd.Execute()
				if run[0] != "" && (err == nil || !strings.Contains(fmt.Sprint(err)+stderr.String(), "host part of the URL")) {
					t.Fatalf("Execute() error = %v, want the refusal of the two-host URL\nstderr:\n%s", err, stderr.String())
				}
				if run[0] == "" && !strings.Contains(stdout.String(), "name: listed-content") {
					t.Fatalf("stdout lacks the listed content:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
				}
				if output := stdout.String() + stderr.String(); strings.Contains(output, "attacker-content") {
					t.Fatalf("output holds the attacker content:\n%s", output)
				}
			}
			for _, header := range attacker.AuthorizationHeaders() {
				if strings.Contains(header, "cli-secret-token") {
					t.Fatal("the attacker host received the --git-bearer-token")
				}
			}
		})
	}
}
