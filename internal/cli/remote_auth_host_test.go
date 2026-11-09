package cli

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

// TestRemoteCredentialsNeedAuthHost checks that every command that takes
// the --remote-* credentials fails before it fetches anything when no
// --remote-auth-host names a host that receives them. The error names the
// new flag and no credential value.
func TestRemoteCredentialsNeedAuthHost(t *testing.T) {
	fixtureRoot := homeOpsPatternFixtureRoot("remote-kustomize")
	current := filepath.Join(fixtureRoot, "current")
	cacheDir := filepath.Join(t.TempDir(), "remote-cache")
	commands := map[string][]string{
		"build apps":  {"build", "apps", "--path", current},
		"test apps":   {"test", "apps", "--path", current},
		"get apps":    {"get", "apps", "--path", current},
		"diag":        {"diag", "--path", current},
		"diff apps":   {"diff", "apps", "--path-orig", filepath.Join(fixtureRoot, "baseline"), "--path", current},
		"diff images": {"diff", "images", "--path-orig", filepath.Join(fixtureRoot, "baseline"), "--path", current},
	}
	credentials := map[string][]string{
		"bearer token": {"--remote-bearer-token", "fixture-secret-token"},
		"basic auth":   {"--remote-username", "fixture-user", "--remote-password", "fixture-secret-password"},
	}
	for commandName, command := range commands {
		for credentialName, flags := range credentials {
			t.Run(commandName+"/"+credentialName, func(t *testing.T) {
				acquirer := &recordingCLIRemoteAcquirer{}
				args := append(append([]string{}, command...), "--offline", "--remote-cache-dir", cacheDir)
				args = append(args, flags...)
				var stdout, stderr bytes.Buffer
				cmd := NewRootCommandWithDependencies(VersionInfo{}, remoteFixtureDependencies(acquirer))
				cmd.SetArgs(args)
				cmd.SetOut(&stdout)
				cmd.SetErr(&stderr)
				err := cmd.Execute()
				if err == nil || !strings.Contains(err.Error(), "--remote-auth-host") {
					t.Fatalf("Execute() error = %v, want an error that names --remote-auth-host\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
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
			})
		}
	}
}
