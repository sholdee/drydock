package cli

import (
	"bytes"
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
