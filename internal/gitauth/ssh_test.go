package gitauth

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/crypto/ssh/knownhosts"
)

const testRepoURL = "ssh://git@example.test/org/repo.git"

func TestSSHAuthExplicitKeyWinsOverAmbientIdentities(t *testing.T) {
	env := newTestEnv(t)
	key := newTestKey(t, "")
	env.writeKey(t, filepath.Join(env.home, ".ssh", "id_ed25519"), key)
	env.setVar("SSH_AUTH_SOCK", startTestAgent(t, key))
	env.writeKnownHosts(t)

	explicit := newTestKey(t, "")
	explicitPath := env.writeKey(t, filepath.Join(env.home, "explicit_key"), explicit)

	auth, resolution, err := SSHAuth(SSHCredentials{PrivateKeyPath: explicitPath}, testRepoURL, env.env)
	if err != nil {
		t.Fatalf("SSHAuth() error = %v", err)
	}
	publicKeys, ok := auth.(*gitssh.PublicKeys)
	if !ok {
		t.Fatalf("auth = %T, want *ssh.PublicKeys", auth)
	}
	if publicKeys.User != "git" {
		t.Fatalf("User = %q, want %q", publicKeys.User, "git")
	}
	if !resolution.Explicit {
		t.Fatal("Explicit = false, want true")
	}
	if resolution.Agent {
		t.Fatal("Agent = true, want false for an explicit key")
	}
	if len(resolution.IdentityFiles) != 0 {
		t.Fatalf("IdentityFiles = %v, want none", resolution.IdentityFiles)
	}
}

func TestSSHAuthUsesTheAgent(t *testing.T) {
	env := newTestEnv(t)
	env.setVar("SSH_AUTH_SOCK", startTestAgent(t, newTestKey(t, "")))
	env.writeKnownHosts(t)

	auth, resolution, err := SSHAuth(SSHCredentials{}, testRepoURL, env.env)
	if err != nil {
		t.Fatalf("SSHAuth() error = %v", err)
	}
	if !resolution.Agent || resolution.AgentKeys != 1 {
		t.Fatalf("Agent = %t, AgentKeys = %d, want true and 1", resolution.Agent, resolution.AgentKeys)
	}
	if resolution.AgentSource != agentSourceEnv {
		t.Fatalf("AgentSource = %q, want %q", resolution.AgentSource, agentSourceEnv)
	}
	if auth.Name() != ambientAuthName {
		t.Fatalf("Name() = %q, want %q", auth.Name(), ambientAuthName)
	}
	config := clientConfig(t, auth)
	if len(config.Auth) != 1 {
		t.Fatalf("Auth = %d methods, want 1", len(config.Auth))
	}
	if config.User != "git" {
		t.Fatalf("User = %q, want %q", config.User, "git")
	}
}

func TestSSHAuthUsesDefaultIdentityFiles(t *testing.T) {
	env := newTestEnv(t)
	env.writeKey(t, filepath.Join(env.home, ".ssh", "id_ed25519"), newTestKey(t, ""))
	env.writeKey(t, filepath.Join(env.home, ".ssh", "id_rsa"), newTestKey(t, ""))
	env.writeKnownHosts(t)

	auth, resolution, err := SSHAuth(SSHCredentials{}, testRepoURL, env.env)
	if err != nil {
		t.Fatalf("SSHAuth() error = %v", err)
	}
	want := []string{
		filepath.Join(env.home, ".ssh", "id_ed25519"),
		filepath.Join(env.home, ".ssh", "id_rsa"),
	}
	if !equalStrings(resolution.IdentityFiles, want) {
		t.Fatalf("IdentityFiles = %v, want %v", resolution.IdentityFiles, want)
	}
	if resolution.Agent {
		t.Fatal("Agent = true, want false")
	}
	if config := clientConfig(t, auth); len(config.Auth) != 1 {
		t.Fatalf("Auth = %d methods, want 1", len(config.Auth))
	}
}

func TestSSHAuthUsesSSHConfigIdentityFilesAndHostname(t *testing.T) {
	env := newTestEnv(t)
	deployKey := env.writeKey(t, filepath.Join(env.home, ".ssh", "deploy_key"), newTestKey(t, ""))
	tokenKey := env.writeKey(t, filepath.Join(env.home, ".ssh", "token_key"), newTestKey(t, ""))
	defaultKey := env.writeKey(t, filepath.Join(env.home, ".ssh", "id_ed25519"), newTestKey(t, ""))
	env.writeKnownHosts(t)
	env.writeSSHConfig(t,
		"Host example.test",
		"  Hostname code.example.fi",
		"  Port 2222",
		"  IdentityFile ~/.ssh/deploy_key",
		"  IdentityFile %d/.ssh/token_key",
		"  IdentityFile %h/.ssh/unsupported_key",
	)

	_, resolution, err := SSHAuth(SSHCredentials{}, testRepoURL, env.env)
	if err != nil {
		t.Fatalf("SSHAuth() error = %v", err)
	}
	want := []string{deployKey, tokenKey, defaultKey}
	if !equalStrings(resolution.IdentityFiles, want) {
		t.Fatalf("IdentityFiles = %v, want %v", resolution.IdentityFiles, want)
	}
	if resolution.HostWithPort != "code.example.fi:2222" {
		t.Fatalf("HostWithPort = %q, want %q", resolution.HostWithPort, "code.example.fi:2222")
	}
	if !hasSkipped(resolution, "unsupported_key: ssh_config IdentityFile uses an unsupported token") {
		t.Fatalf("Skipped = %v, want the unsupported token entry", resolution.Skipped)
	}
}

func TestSSHAuthHonorsIdentityAgentDirective(t *testing.T) {
	identitySocket := startTestAgent(t, newTestKey(t, ""))
	emptySocket := startTestAgent(t)

	tests := []struct {
		name        string
		directive   string
		authSock    string
		wantSource  string
		wantAgent   bool
		wantKeys    int
		wantMethods int
	}{
		{
			name:        "quoted path wins over the environment",
			directive:   fmt.Sprintf("IdentityAgent %q", identitySocket),
			authSock:    emptySocket,
			wantSource:  agentSourceIdentityAgent,
			wantAgent:   true,
			wantKeys:    1,
			wantMethods: 1,
		},
		{
			name:      "none disables the agent",
			directive: "IdentityAgent none",
			authSock:  identitySocket,
		},
		{
			name:        "the literal SSH_AUTH_SOCK selects the variable",
			directive:   "IdentityAgent SSH_AUTH_SOCK",
			authSock:    identitySocket,
			wantSource:  agentSourceEnv,
			wantAgent:   true,
			wantKeys:    1,
			wantMethods: 1,
		},
		{
			name:        "an unsupported token falls back to the variable",
			directive:   "IdentityAgent ${HOME}/agent.sock",
			authSock:    identitySocket,
			wantSource:  agentSourceEnv,
			wantAgent:   true,
			wantKeys:    1,
			wantMethods: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newTestEnv(t)
			env.writeKnownHosts(t)
			env.setVar("SSH_AUTH_SOCK", tt.authSock)
			env.writeSSHConfig(t, "Host *", "  "+tt.directive)

			auth, resolution, err := SSHAuth(SSHCredentials{}, testRepoURL, env.env)
			if tt.wantMethods == 0 {
				if _, ok := errors.AsType[*NoCredentialsError](err); !ok {
					t.Fatalf("SSHAuth() error = %v, want *NoCredentialsError", err)
				}
				if resolution.Agent {
					t.Fatal("Agent = true, want false")
				}
				if !strings.Contains(err.Error(), "ssh_config disables the agent") {
					t.Fatalf("error = %q, want the disabled-agent clause", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("SSHAuth() error = %v", err)
			}
			if resolution.AgentSource != tt.wantSource {
				t.Fatalf("AgentSource = %q, want %q", resolution.AgentSource, tt.wantSource)
			}
			if resolution.Agent != tt.wantAgent || resolution.AgentKeys != tt.wantKeys {
				t.Fatalf("Agent = %t, AgentKeys = %d, want %t and %d", resolution.Agent, resolution.AgentKeys, tt.wantAgent, tt.wantKeys)
			}
			if config := clientConfig(t, auth); len(config.Auth) != tt.wantMethods {
				t.Fatalf("Auth = %d methods, want %d", len(config.Auth), tt.wantMethods)
			}
		})
	}
}

func TestSSHAuthReportsUnsupportedSSHConfigAndKeepsDefaults(t *testing.T) {
	env := newTestEnv(t)
	env.writeKnownHosts(t)
	defaultKey := env.writeKey(t, filepath.Join(env.home, ".ssh", "id_ed25519"), newTestKey(t, ""))
	env.writeSSHConfig(t, "Match host example.test", "  IdentityFile ~/.ssh/other_key")

	_, resolution, err := SSHAuth(SSHCredentials{}, testRepoURL, env.env)
	if err != nil {
		t.Fatalf("SSHAuth() error = %v", err)
	}
	if !equalStrings(resolution.IdentityFiles, []string{defaultKey}) {
		t.Fatalf("IdentityFiles = %v, want %v", resolution.IdentityFiles, []string{defaultKey})
	}
	found := false
	for _, entry := range resolution.Skipped {
		if strings.HasPrefix(entry, "ssh_config: "+userSSHConfigLabel+": ") {
			found = true
		}
	}
	if !found {
		t.Fatalf("Skipped = %v, want an ssh_config entry", resolution.Skipped)
	}
}

func TestSSHAuthDropsIdentitiesTheAgentAlreadyOffers(t *testing.T) {
	env := newTestEnv(t)
	shared := newTestKey(t, "")
	env.writeKey(t, filepath.Join(env.home, ".ssh", "id_ed25519"), shared)
	other := env.writeKey(t, filepath.Join(env.home, ".ssh", "id_rsa"), newTestKey(t, ""))
	env.setVar("SSH_AUTH_SOCK", startTestAgent(t, shared))
	env.writeKnownHosts(t)

	auth, resolution, err := SSHAuth(SSHCredentials{}, testRepoURL, env.env)
	if err != nil {
		t.Fatalf("SSHAuth() error = %v", err)
	}
	if !equalStrings(resolution.IdentityFiles, []string{other}) {
		t.Fatalf("IdentityFiles = %v, want %v", resolution.IdentityFiles, []string{other})
	}
	if !hasSkipped(resolution, "id_ed25519: already offered by the agent") {
		t.Fatalf("Skipped = %v, want the agent duplicate entry", resolution.Skipped)
	}
	if config := clientConfig(t, auth); len(config.Auth) != 2 {
		t.Fatalf("Auth = %d methods, want 2", len(config.Auth))
	}
}

func TestSSHAuthCapsTheIdentityCount(t *testing.T) {
	env := newTestEnv(t)
	env.writeKnownHosts(t)
	lines := make([]string, 0, maxIdentities+2)
	lines = append(lines, "Host example.test")
	for i := range maxIdentities + 1 {
		name := fmt.Sprintf("key_%d", i)
		env.writeKey(t, filepath.Join(env.home, ".ssh", name), newTestKey(t, ""))
		lines = append(lines, "  IdentityFile ~/.ssh/"+name)
	}
	env.writeSSHConfig(t, lines...)

	_, resolution, err := SSHAuth(SSHCredentials{}, testRepoURL, env.env)
	if err != nil {
		t.Fatalf("SSHAuth() error = %v", err)
	}
	if len(resolution.IdentityFiles) != maxIdentities {
		t.Fatalf("IdentityFiles = %d entries, want %d", len(resolution.IdentityFiles), maxIdentities)
	}
	if !hasSkipped(resolution, fmt.Sprintf("key_%d: not offered; at most %d identities are sent per connection", maxIdentities, maxIdentities)) {
		t.Fatalf("Skipped = %v, want the cap entry", resolution.Skipped)
	}
}

func TestSSHAuthReportsTheAgentFillingTheIdentityBudget(t *testing.T) {
	env := newTestEnv(t)
	agentKeys := make([]testKey, 0, maxIdentities+1)
	for range maxIdentities + 1 {
		agentKeys = append(agentKeys, newTestKey(t, ""))
	}
	env.setVar("SSH_AUTH_SOCK", startTestAgent(t, agentKeys...))
	env.writeKnownHosts(t)
	env.writeKey(t, filepath.Join(env.home, ".ssh", "id_ed25519"), newTestKey(t, ""))

	_, resolution, err := SSHAuth(SSHCredentials{}, testRepoURL, env.env)
	if err != nil {
		t.Fatalf("SSHAuth() error = %v", err)
	}
	if len(resolution.IdentityFiles) != 0 {
		t.Fatalf("IdentityFiles = %v, want none", resolution.IdentityFiles)
	}
	want := fmt.Sprintf("id_ed25519: not offered; the agent's keys already fill the %d-identity budget", maxIdentities)
	if !hasSkipped(resolution, want) {
		t.Fatalf("Skipped = %v, want %q", resolution.Skipped, want)
	}
}

func TestSSHAuthReportsPassphraseProtectedIdentities(t *testing.T) {
	env := newTestEnv(t)
	env.writeKnownHosts(t)
	env.writeKey(t, filepath.Join(env.home, ".ssh", "id_ed25519"), newTestKey(t, "secret-passphrase"))

	_, _, err := SSHAuth(SSHCredentials{}, testRepoURL, env.env)
	if _, ok := errors.AsType[*NoCredentialsError](err); !ok {
		t.Fatalf("SSHAuth() error = %v, want *NoCredentialsError", err)
	}
	message := err.Error()
	want := "id_ed25519: passphrase-protected; pass it with --git-ssh-key-file (and --git-ssh-passphrase)"
	if !strings.Contains(message, want) {
		t.Fatalf("error = %q, want it to contain %q", message, want)
	}
	assertNoLeaks(t, message, env.home)
}

func TestSSHAuthNoCredentialsMessage(t *testing.T) {
	env := newTestEnv(t)
	env.writeKnownHosts(t)

	_, _, err := SSHAuth(SSHCredentials{}, testRepoURL, env.env)
	if _, ok := errors.AsType[*NoCredentialsError](err); !ok {
		t.Fatalf("SSHAuth() error = %v, want *NoCredentialsError", err)
	}
	want := "no SSH credentials: --git-ssh-key-file is not set, " +
		"no ssh-agent (SSH_AUTH_SOCK is not set and ssh_config sets no IdentityAgent), " +
		"and no usable identity was found (checked ~/.ssh/config IdentityFile and ~/.ssh/id_ed25519, id_ecdsa, id_rsa)"
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err, want)
	}
}

func TestSSHAuthReportsAnAgentWithoutKeys(t *testing.T) {
	env := newTestEnv(t)
	env.writeKnownHosts(t)
	env.setVar("SSH_AUTH_SOCK", startTestAgent(t))

	_, resolution, err := SSHAuth(SSHCredentials{}, testRepoURL, env.env)
	if _, ok := errors.AsType[*NoCredentialsError](err); !ok {
		t.Fatalf("SSHAuth() error = %v, want *NoCredentialsError", err)
	}
	if !resolution.Agent || resolution.AgentKeys != 0 {
		t.Fatalf("Agent = %t, AgentKeys = %d, want true and 0", resolution.Agent, resolution.AgentKeys)
	}
	if !strings.Contains(err.Error(), "the ssh-agent offered no keys") {
		t.Fatalf("error = %q, want the empty-agent clause", err)
	}
}

func TestSSHAuthResolvesKnownHostsFiles(t *testing.T) {
	t.Run("explicit path wins and is not filtered", func(t *testing.T) {
		env := newTestEnv(t)
		env.writeKnownHosts(t)
		explicit := filepath.Join(env.home, "explicit_known_hosts")
		if err := os.WriteFile(explicit, []byte{}, 0o600); err != nil {
			t.Fatalf("write known_hosts: %v", err)
		}
		keyPath := env.writeKey(t, filepath.Join(env.home, "explicit_key"), newTestKey(t, ""))

		_, resolution, err := SSHAuth(SSHCredentials{PrivateKeyPath: keyPath, KnownHostsPath: explicit}, testRepoURL, env.env)
		if err != nil {
			t.Fatalf("SSHAuth() error = %v", err)
		}
		if !equalStrings(resolution.KnownHosts, []string{explicit}) {
			t.Fatalf("KnownHosts = %v, want %v", resolution.KnownHosts, []string{explicit})
		}
	})

	t.Run("a missing explicit path fails", func(t *testing.T) {
		env := newTestEnv(t)
		env.writeKnownHosts(t)
		keyPath := env.writeKey(t, filepath.Join(env.home, "explicit_key"), newTestKey(t, ""))

		_, _, err := SSHAuth(SSHCredentials{
			PrivateKeyPath: keyPath,
			KnownHostsPath: filepath.Join(env.home, "missing_known_hosts"),
		}, testRepoURL, env.env)
		if err == nil {
			t.Fatal("SSHAuth() error = nil, want a known_hosts error")
		}
		if !strings.Contains(err.Error(), "load known_hosts") {
			t.Fatalf("error = %q, want a load known_hosts error", err)
		}
	})

	t.Run("SSH_KNOWN_HOSTS is respected", func(t *testing.T) {
		env := newTestEnv(t)
		env.writeKnownHosts(t)
		alternate := filepath.Join(env.home, "alternate_known_hosts")
		if err := os.WriteFile(alternate, []byte{}, 0o600); err != nil {
			t.Fatalf("write known_hosts: %v", err)
		}
		env.setVar("SSH_KNOWN_HOSTS", alternate)
		env.writeKey(t, filepath.Join(env.home, ".ssh", "id_ed25519"), newTestKey(t, ""))

		_, resolution, err := SSHAuth(SSHCredentials{}, testRepoURL, env.env)
		if err != nil {
			t.Fatalf("SSHAuth() error = %v", err)
		}
		if !equalStrings(resolution.KnownHosts, []string{alternate}) {
			t.Fatalf("KnownHosts = %v, want %v", resolution.KnownHosts, []string{alternate})
		}
	})

	t.Run("the default list is filtered to existing files", func(t *testing.T) {
		env := newTestEnv(t)
		knownHosts := env.writeKnownHosts(t)
		env.writeKey(t, filepath.Join(env.home, ".ssh", "id_ed25519"), newTestKey(t, ""))

		_, resolution, err := SSHAuth(SSHCredentials{}, testRepoURL, env.env)
		if err != nil {
			t.Fatalf("SSHAuth() error = %v", err)
		}
		if !equalStrings(resolution.KnownHosts, []string{knownHosts}) {
			t.Fatalf("KnownHosts = %v, want %v", resolution.KnownHosts, []string{knownHosts})
		}
	})

	t.Run("no known_hosts file at all", func(t *testing.T) {
		env := newTestEnv(t)
		env.writeKey(t, filepath.Join(env.home, ".ssh", "id_ed25519"), newTestKey(t, ""))

		_, _, err := SSHAuth(SSHCredentials{}, testRepoURL, env.env)
		if err == nil {
			t.Fatal("SSHAuth() error = nil, want the known_hosts error")
		}
		want := "no known_hosts file: pass --git-known-hosts-file or create ~/.ssh/known_hosts (ssh-keyscan example.test >> ~/.ssh/known_hosts)"
		if err.Error() != want {
			t.Fatalf("error = %q, want %q", err, want)
		}
	})

	t.Run("the ssh-keyscan hint names the host that is dialed", func(t *testing.T) {
		env := newTestEnv(t)
		env.writeKey(t, filepath.Join(env.home, ".ssh", "id_ed25519"), newTestKey(t, ""))
		env.writeSSHConfig(t, "Host example.test", "  Hostname code.example.fi")

		_, _, err := SSHAuth(SSHCredentials{}, testRepoURL, env.env)
		if err == nil {
			t.Fatal("SSHAuth() error = nil, want the known_hosts error")
		}
		want := "no known_hosts file: pass --git-known-hosts-file or create ~/.ssh/known_hosts (ssh-keyscan code.example.fi >> ~/.ssh/known_hosts)"
		if err.Error() != want {
			t.Fatalf("error = %q, want %q", err, want)
		}
	})
}

func TestSSHAuthKnownHostsErrorsHideAmbientPaths(t *testing.T) {
	t.Run("an ambient file that cannot be read", func(t *testing.T) {
		env := newTestEnv(t)
		knownHosts := env.writeKnownHosts(t)
		makeUnreadable(t, knownHosts)
		env.writeKey(t, filepath.Join(env.home, ".ssh", "id_ed25519"), newTestKey(t, ""))

		_, _, err := SSHAuth(SSHCredentials{}, testRepoURL, env.env)
		assertAmbientKnownHostsError(t, err, env.home)
	})

	t.Run("an ambient file that cannot be parsed", func(t *testing.T) {
		env := newTestEnv(t)
		env.writeKnownHosts(t, "example.test not-a-host-key")
		env.writeKey(t, filepath.Join(env.home, ".ssh", "id_ed25519"), newTestKey(t, ""))

		_, _, err := SSHAuth(SSHCredentials{}, testRepoURL, env.env)
		assertAmbientKnownHostsError(t, err, env.home)
	})

	t.Run("an explicit path is still named", func(t *testing.T) {
		env := newTestEnv(t)
		explicit := filepath.Join(env.home, "explicit_known_hosts")
		if err := os.WriteFile(explicit, []byte{}, 0o600); err != nil {
			t.Fatalf("write known_hosts: %v", err)
		}
		makeUnreadable(t, explicit)
		keyPath := env.writeKey(t, filepath.Join(env.home, "explicit_key"), newTestKey(t, ""))

		_, _, err := SSHAuth(SSHCredentials{PrivateKeyPath: keyPath, KnownHostsPath: explicit}, testRepoURL, env.env)
		if err == nil {
			t.Fatal("SSHAuth() error = nil, want a known_hosts error")
		}
		if !strings.Contains(err.Error(), explicit) {
			t.Fatalf("error = %q, want it to name %q", err, explicit)
		}
	})
}

func TestSSHAuthSystemFileErrorsUseLabels(t *testing.T) {
	t.Run("an unparsable system known_hosts is named by its label", func(t *testing.T) {
		env := newTestEnv(t)
		systemKnownHosts := filepath.Join(env.home, "system_known_hosts")
		if err := os.WriteFile(systemKnownHosts, []byte("example.test not-a-host-key\n"), 0o600); err != nil {
			t.Fatalf("write system known_hosts: %v", err)
		}
		env.env.SystemKnownHosts = systemKnownHosts
		env.writeKey(t, filepath.Join(env.home, ".ssh", "id_ed25519"), newTestKey(t, ""))

		_, _, err := SSHAuth(SSHCredentials{}, testRepoURL, env.env)
		if err == nil {
			t.Fatal("SSHAuth() error = nil, want a known_hosts error")
		}
		if !strings.Contains(err.Error(), systemKnownHostsLabel) {
			t.Fatalf("error = %q, want it to name %q", err, systemKnownHostsLabel)
		}
		assertNoLeaks(t, err.Error(), env.home)
	})

	t.Run("an unparsable system ssh_config is named by its label", func(t *testing.T) {
		env := newTestEnv(t)
		systemConfig := filepath.Join(env.home, "system_ssh_config")
		if err := os.WriteFile(systemConfig, []byte("Match host example.test\n  IdentityFile ~/.ssh/other\n"), 0o600); err != nil {
			t.Fatalf("write system ssh_config: %v", err)
		}
		env.env.SystemSSHConfigPath = systemConfig
		env.env.SSHConfig = fileSSHConfig(env.home, systemConfig)
		env.writeKnownHosts(t)
		env.writeKey(t, filepath.Join(env.home, ".ssh", "id_ed25519"), newTestKey(t, ""))

		_, resolution, err := SSHAuth(SSHCredentials{}, testRepoURL, env.env)
		if err != nil {
			t.Fatalf("SSHAuth() error = %v, want the default key to still load", err)
		}
		entry := skippedWithPrefix(resolution, "ssh_config:")
		if entry == "" {
			t.Fatalf("Skipped = %q, want an ssh_config parse entry", resolution.Skipped)
		}
		if !strings.Contains(entry, systemSSHConfigLabel) {
			t.Fatalf("skip entry = %q, want it to name %q", entry, systemSSHConfigLabel)
		}
		assertNoLeaks(t, entry, env.home)
		if len(resolution.IdentityFiles) != 1 {
			t.Fatalf("IdentityFiles = %q, want the default key", resolution.IdentityFiles)
		}
	})
}

func TestSSHAuthReportsAnAgentThatDoesNotAnswer(t *testing.T) {
	env := newTestEnv(t)
	env.writeKnownHosts(t)
	socket := filepath.Join(env.home, "agent.sock")
	env.setVar("SSH_AUTH_SOCK", socket)
	env.env.AgentDial = func(string) (net.Conn, error) {
		return nil, &net.OpError{
			Op:   "dial",
			Net:  "unix",
			Addr: &net.UnixAddr{Name: socket, Net: "unix"},
			Err:  errors.New("connect: connection refused"),
		}
	}

	_, resolution, err := SSHAuth(SSHCredentials{}, testRepoURL, env.env)
	if _, ok := errors.AsType[*NoCredentialsError](err); !ok {
		t.Fatalf("SSHAuth() error = %v, want *NoCredentialsError", err)
	}
	if resolution.Agent {
		t.Fatal("Agent = true, want false when the agent never answered")
	}
	if resolution.AgentSource != agentSourceEnv {
		t.Fatalf("AgentSource = %q, want %q", resolution.AgentSource, agentSourceEnv)
	}
	if !strings.Contains(err.Error(), "the ssh-agent at "+agentSourceEnv+" did not answer") {
		t.Fatalf("error = %q, want the unanswered-agent clause", err)
	}
	entry := skippedWithPrefix(resolution, "agent: ")
	if entry == "" {
		t.Fatalf("Skipped = %v, want an agent entry", resolution.Skipped)
	}
	if strings.Contains(entry, socket) || strings.Contains(entry, env.home) {
		t.Fatalf("Skipped entry = %q, leaked the socket path", entry)
	}
}

func TestSSHAuthReportsTheAgentsShareOfTheIdentityBudget(t *testing.T) {
	env := newTestEnv(t)
	agentKeys := make([]testKey, 0, maxIdentities-1)
	for range maxIdentities - 1 {
		agentKeys = append(agentKeys, newTestKey(t, ""))
	}
	env.setVar("SSH_AUTH_SOCK", startTestAgent(t, agentKeys...))
	env.writeKnownHosts(t)
	loaded := env.writeKey(t, filepath.Join(env.home, ".ssh", "id_ed25519"), newTestKey(t, ""))
	env.writeKey(t, filepath.Join(env.home, ".ssh", "id_rsa"), newTestKey(t, ""))

	_, resolution, err := SSHAuth(SSHCredentials{}, testRepoURL, env.env)
	if err != nil {
		t.Fatalf("SSHAuth() error = %v", err)
	}
	if resolution.AgentKeys != maxIdentities-1 {
		t.Fatalf("AgentKeys = %d, want %d", resolution.AgentKeys, maxIdentities-1)
	}
	if !equalStrings(resolution.IdentityFiles, []string{loaded}) {
		t.Fatalf("IdentityFiles = %v, want %v", resolution.IdentityFiles, []string{loaded})
	}
	want := fmt.Sprintf("id_rsa: not offered; the agent already offers %d of the %d identities sent per connection", maxIdentities-1, maxIdentities)
	if !hasSkipped(resolution, want) {
		t.Fatalf("Skipped = %v, want %q", resolution.Skipped, want)
	}
}

func TestSSHAuthVerifiesHostKeys(t *testing.T) {
	hostKey := newTestKey(t, "").public
	otherKey := newTestKey(t, "").public

	tests := []struct {
		name     string
		explicit bool
	}{
		{name: "explicit key", explicit: true},
		{name: "ambient identities"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newTestEnv(t)
			env.writeKnownHosts(t, knownhosts.Line([]string{"example.test:22"}, hostKey))
			creds := SSHCredentials{}
			if tt.explicit {
				creds.PrivateKeyPath = env.writeKey(t, filepath.Join(env.home, "explicit_key"), newTestKey(t, ""))
			} else {
				env.writeKey(t, filepath.Join(env.home, ".ssh", "id_ed25519"), newTestKey(t, ""))
			}

			auth, _, err := SSHAuth(creds, testRepoURL, env.env)
			if err != nil {
				t.Fatalf("SSHAuth() error = %v", err)
			}
			config := clientConfig(t, auth)
			remote := &net.TCPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 22}
			if err := config.HostKeyCallback("example.test:22", remote, hostKey); err != nil {
				t.Fatalf("HostKeyCallback() error = %v, want nil", err)
			}
			err = config.HostKeyCallback("example.test:22", remote, otherKey)
			if _, ok := errors.AsType[*knownhosts.KeyError](err); !ok {
				t.Fatalf("HostKeyCallback() error = %v, want *knownhosts.KeyError", err)
			}
			if !containsString(config.HostKeyAlgorithms, ssh.KeyAlgoED25519) {
				t.Fatalf("HostKeyAlgorithms = %v, want it to contain %q", config.HostKeyAlgorithms, ssh.KeyAlgoED25519)
			}
		})
	}
}

func TestSSHAuthCachesTheAgentConnection(t *testing.T) {
	env := newTestEnv(t)
	env.writeKnownHosts(t)
	env.setVar("SSH_AUTH_SOCK", startTestAgent(t, newTestKey(t, "")))

	for range 2 {
		if _, _, err := SSHAuth(SSHCredentials{}, testRepoURL, env.env); err != nil {
			t.Fatalf("SSHAuth() error = %v", err)
		}
	}
	if env.dials != 1 {
		t.Fatalf("agent dials = %d, want 1", env.dials)
	}
}

func TestResolutionStringHidesPathsAndKeyMaterial(t *testing.T) {
	env := newTestEnv(t)
	env.writeKnownHosts(t)
	env.writeKey(t, filepath.Join(env.home, ".ssh", "id_ed25519"), newTestKey(t, ""))
	env.setVar("SSH_AUTH_SOCK", startTestAgent(t, newTestKey(t, "")))

	_, resolution, err := SSHAuth(SSHCredentials{}, testRepoURL, env.env)
	if err != nil {
		t.Fatalf("SSHAuth() error = %v", err)
	}
	summary := resolution.String()
	if strings.Contains(summary, "PRIVATE KEY") {
		t.Fatalf("String() = %q, leaked key material", summary)
	}
	if strings.Contains(summary, "/") {
		t.Fatalf("String() = %q, want basenames only", summary)
	}
	for _, want := range []string{"agent SSH_AUTH_SOCK", "id_ed25519", "known_hosts", "example.test:22"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("String() = %q, want it to contain %q", summary, want)
		}
	}
}

// testEnv is a synthetic Environment: a temporary HOME, an in-memory variable
// set, and system paths that do not exist, so no test reads the developer's
// ~/.ssh, /etc/ssh, or agent.
type testEnv struct {
	home  string
	vars  map[string]string
	dials int
	env   Environment
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatalf("create .ssh: %v", err)
	}
	fixture := &testEnv{home: home, vars: map[string]string{}}
	absent := filepath.Join(home, "absent")
	fixture.env = Environment{
		LookupEnv: func(key string) (string, bool) {
			value, ok := fixture.vars[key]
			return value, ok
		},
		HomeDir:             home,
		SystemSSHConfigPath: filepath.Join(absent, "ssh_config"),
		SystemKnownHosts:    filepath.Join(absent, "ssh_known_hosts"),
		AgentDial: func(socket string) (net.Conn, error) {
			fixture.dials++
			return net.Dial("unix", socket)
		},
	}
	return fixture
}

func (e *testEnv) setVar(key, value string) {
	e.vars[key] = value
}

func (e *testEnv) writeKnownHosts(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(e.home, ".ssh", "known_hosts")
	contents := ""
	if len(lines) > 0 {
		contents = strings.Join(lines, "\n") + "\n"
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}
	return path
}

func (e *testEnv) writeSSHConfig(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(e.home, ".ssh", "config")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatalf("write ssh config: %v", err)
	}
	return path
}

func (e *testEnv) writeKey(t *testing.T, path string, key testKey) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create key directory: %v", err)
	}
	if err := os.WriteFile(path, key.pem, 0o600); err != nil {
		t.Fatalf("write private key: %v", err)
	}
	return path
}

type testKey struct {
	private ed25519.PrivateKey
	pem     []byte
	public  ssh.PublicKey
}

func newTestKey(t *testing.T, passphrase string) testKey {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}
	var block *pem.Block
	if passphrase == "" {
		block, err = ssh.MarshalPrivateKey(privateKey, "test@example.com")
	} else {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(privateKey, "test@example.com", []byte(passphrase))
	}
	if err != nil {
		t.Fatalf("MarshalPrivateKey() error = %v", err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatalf("NewSignerFromKey() error = %v", err)
	}
	return testKey{private: privateKey, pem: pem.EncodeToMemory(block), public: signer.PublicKey()}
}

// startTestAgent serves an in-process keyring over a unix socket and returns
// its path.
func startTestAgent(t *testing.T, keys ...testKey) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("ssh-agent sockets are unix-only")
	}
	// t.TempDir() paths overflow the ~104 byte unix sun_path limit on macOS.
	directory, err := os.MkdirTemp("", "dd-agent") //nolint:usetesting // unix sun_path is ~104 bytes on macOS; t.TempDir() paths overflow it
	if err != nil {
		t.Fatalf("MkdirTemp() error = %v", err)
	}
	keyring := agent.NewKeyring()
	for _, key := range keys {
		if err := keyring.Add(agent.AddedKey{PrivateKey: key.private}); err != nil {
			t.Fatalf("Keyring.Add() error = %v", err)
		}
	}
	socket := filepath.Join(directory, "agent.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	t.Cleanup(func() {
		_ = listener.Close()
		_ = os.RemoveAll(directory)
	})
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() { _ = agent.ServeAgent(keyring, conn) }()
		}
	}()
	return socket
}

func clientConfig(t *testing.T, auth any) *ssh.ClientConfig {
	t.Helper()
	method, ok := auth.(gitssh.AuthMethod)
	if !ok {
		t.Fatalf("auth = %T, want an ssh.AuthMethod", auth)
	}
	config, err := method.ClientConfig()
	if err != nil {
		t.Fatalf("ClientConfig() error = %v", err)
	}
	return config
}

// makeUnreadable strips every permission bit so opening the file fails. Root
// ignores the mode, so tests that need the failure skip when running as root.
func makeUnreadable(t *testing.T, path string) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("root reads a 0o000 file regardless of its mode")
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
}

// assertAmbientKnownHostsError pins the property the plan requires of every
// ambient known_hosts failure: the file is named by its documented label and
// the home directory never appears.
func assertAmbientKnownHostsError(t *testing.T, err error, home string) {
	t.Helper()
	if err == nil {
		t.Fatal("SSHAuth() error = nil, want a known_hosts error")
	}
	message := err.Error()
	if !strings.Contains(message, userKnownHostsLabel) {
		t.Fatalf("error = %q, want it to name %q", message, userKnownHostsLabel)
	}
	assertNoLeaks(t, message, home)
}

func skippedWithPrefix(resolution Resolution, prefix string) string {
	for _, entry := range resolution.Skipped {
		if strings.HasPrefix(entry, prefix) {
			return entry
		}
	}
	return ""
}

func hasSkipped(resolution Resolution, entry string) bool {
	return containsString(resolution.Skipped, entry)
}

func containsString(values []string, want string) bool {
	return slices.Contains(values, want)
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}

func assertNoLeaks(t *testing.T, message, home string) {
	t.Helper()
	if strings.Contains(message, home) {
		t.Fatalf("message = %q, leaked the home directory", message)
	}
	if strings.Contains(message, "PRIVATE KEY") {
		t.Fatalf("message = %q, leaked key material", message)
	}
}
