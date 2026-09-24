package gitauth

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/transport"
	gitssh "github.com/go-git/go-git/v5/plumbing/transport/ssh"
	"golang.org/x/crypto/ssh"

	"github.com/sholdee/drydock/internal/giturl"
)

const (
	defaultSystemSSHConfigPath = "/etc/ssh/ssh_config"
	defaultSystemKnownHosts    = "/etc/ssh/ssh_known_hosts"

	// maxIdentities mirrors the OpenSSH default MaxAuthTries: offering more
	// public keys than the server accepts tries ends the handshake before a
	// usable identity is reached.
	maxIdentities = 6

	// ambientAuthName is the go-git auth method name reported for identities
	// that were discovered rather than configured.
	ambientAuthName = "ssh-ambient-identities"

	userSSHConfigLabel   = "~/.ssh/config"
	systemSSHConfigLabel = "system ssh_config"

	userKnownHostsLabel   = "~/.ssh/known_hosts"
	systemKnownHostsLabel = "system ssh_known_hosts"
)

// defaultIdentityFiles are the ~/.ssh key files OpenSSH tries when no
// IdentityFile directive applies, in the order it tries them.
var defaultIdentityFiles = []string{"id_ed25519", "id_ecdsa", "id_rsa"}

// SSHCredentials are the explicitly configured Git SSH credential values.
type SSHCredentials struct {
	PrivateKeyPath string
	PrivateKey     string
	Passphrase     string
	KnownHostsPath string
}

// Environment is the ambient state SSH identity resolution reads. It is
// injectable so tests never touch the developer's ~/.ssh, /etc/ssh, or agent.
// Default returns an Environment bound to the real process environment; the
// zero value of every field falls back to that same real environment.
type Environment struct {
	// LookupEnv reads an environment variable. nil uses os.LookupEnv.
	LookupEnv func(key string) (string, bool)
	// HomeDir is the user home directory. "" uses os.UserHomeDir.
	HomeDir string
	// SystemSSHConfigPath is the system ssh_config file. "" uses /etc/ssh/ssh_config.
	SystemSSHConfigPath string
	// SystemKnownHosts is the system known_hosts file. "" uses /etc/ssh/ssh_known_hosts.
	SystemKnownHosts string
	// SSHConfig returns every value configured for host and key. nil parses
	// HomeDir/.ssh/config and then SystemSSHConfigPath.
	SSHConfig func(host, key string) ([]string, error)
	// AgentDial connects to an agent socket. nil dials the unix socket.
	AgentDial func(socket string) (net.Conn, error)
}

// Default returns an Environment bound to the real process environment.
func Default() Environment {
	return normalizeEnvironment(Environment{})
}

func normalizeEnvironment(env Environment) Environment {
	if env.LookupEnv == nil {
		env.LookupEnv = os.LookupEnv
	}
	if env.HomeDir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			env.HomeDir = home
		}
	}
	if env.SystemSSHConfigPath == "" {
		env.SystemSSHConfigPath = defaultSystemSSHConfigPath
	}
	if env.SystemKnownHosts == "" {
		env.SystemKnownHosts = defaultSystemKnownHosts
	}
	if env.SSHConfig == nil {
		env.SSHConfig = fileSSHConfig(env.HomeDir, env.SystemSSHConfigPath)
	}
	if env.AgentDial == nil {
		env.AgentDial = dialAgentSocket
	}
	return env
}

// Resolution records which SSH identity sources were used, for diagnostics and
// for the no-credentials error. It never carries key material.
type Resolution struct {
	// Explicit reports that a configured key file or inline key was used.
	Explicit bool
	// Agent reports that an agent socket was found and answered List.
	Agent bool
	// AgentSource is "IdentityAgent", "SSH_AUTH_SOCK", or "" when no socket
	// was found or ssh_config disabled the agent.
	AgentSource string
	// AgentKeys is the number of keys the agent offered.
	AgentKeys int
	// IdentityFiles are the ambient key files actually loaded, in order.
	IdentityFiles []string
	// Skipped holds "<basename>: reason" entries for candidates that were
	// found but unusable.
	Skipped []string
	// KnownHosts is the exact file list handed to go-git.
	KnownHosts []string
	// HostWithPort is the address host key algorithms were selected for.
	HostWithPort string

	// agentDisabled records an ssh_config "IdentityAgent none" directive.
	agentDisabled bool
}

// String renders a one-line summary using basenames only, so it is safe to log
// and safe to put in a PR comment.
func (r Resolution) String() string {
	parts := make([]string, 0, 6)
	if r.Explicit {
		parts = append(parts, "explicit key")
	}
	switch {
	case r.Agent:
		parts = append(parts, fmt.Sprintf("agent %s (%d keys)", r.AgentSource, r.AgentKeys))
	case r.agentDisabled:
		parts = append(parts, "agent disabled")
	case r.AgentSource != "":
		parts = append(parts, "agent "+r.AgentSource+" (unavailable)")
	}
	if len(r.IdentityFiles) > 0 {
		parts = append(parts, "identities "+strings.Join(baseNames(r.IdentityFiles), ", "))
	}
	if len(r.KnownHosts) > 0 {
		parts = append(parts, "known_hosts "+strings.Join(baseNames(r.KnownHosts), ", "))
	}
	if r.HostWithPort != "" {
		parts = append(parts, "host "+r.HostWithPort)
	}
	if len(r.Skipped) > 0 {
		parts = append(parts, "skipped "+strings.Join(r.Skipped, ", "))
	}
	if len(parts) == 0 {
		return "no ssh identity"
	}
	return strings.Join(parts, "; ")
}

func (r Resolution) agentClause() string {
	switch {
	case r.agentDisabled:
		return "ssh_config disables the agent (IdentityAgent none)"
	case r.AgentSource == "":
		return "no ssh-agent (SSH_AUTH_SOCK is not set and ssh_config sets no IdentityAgent)"
	case !r.Agent:
		return "the ssh-agent at " + r.AgentSource + " did not answer"
	default:
		return "the ssh-agent offered no keys"
	}
}

// NoCredentialsError is returned when neither an explicit key nor any ambient
// identity is usable. It never contains the repository URL; callers wrap it
// with their redacted URL.
type NoCredentialsError struct{ Resolution Resolution }

func (e *NoCredentialsError) Error() string {
	var b strings.Builder
	b.WriteString("no SSH credentials: --git-ssh-key-file is not set, ")
	b.WriteString(e.Resolution.agentClause())
	b.WriteString(", and no usable identity was found (checked ")
	b.WriteString(userSSHConfigLabel)
	b.WriteString(" IdentityFile and ~/.ssh/id_ed25519, id_ecdsa, id_rsa)")
	if len(e.Resolution.Skipped) > 0 {
		b.WriteString("; skipped: ")
		b.WriteString(strings.Join(e.Resolution.Skipped, "; "))
	}
	return b.String()
}

// SSHAuth returns the go-git auth method for an ssh:// or scp-style repoURL.
// An explicit key always wins; otherwise the agent and the ~/.ssh identities
// are used. Host keys are verified against known_hosts either way.
func SSHAuth(creds SSHCredentials, repoURL string, env Environment) (transport.AuthMethod, Resolution, error) {
	env = normalizeEnvironment(env)
	rec := &recorder{}

	endpoint, err := transport.NewEndpoint(strings.TrimSpace(repoURL))
	if err != nil {
		return nil, Resolution{}, errors.New("invalid SSH repository URL")
	}
	user := strings.TrimSpace(endpoint.User)
	if user == "" {
		user = giturl.SSHUser(repoURL)
	}

	res := Resolution{HostWithPort: resolveHostWithPort(env, endpoint.Host, endpoint.Port, rec)}

	if hasExplicitKey(creds) {
		publicKeys, keyErr := explicitPublicKeys(creds, user)
		res.Explicit = true
		res.Skipped = rec.entries()
		if keyErr != nil {
			return nil, res, keyErr
		}
		if err := applyKnownHosts(&publicKeys.HostKeyCallbackHelper, creds, env, endpoint.Host, &res); err != nil {
			return nil, res, err
		}
		return publicKeys, res, nil
	}

	methods := ambientMethods(env, endpoint.Host, rec, &res)
	res.Skipped = rec.entries()
	if len(methods) == 0 {
		return nil, res, &NoCredentialsError{Resolution: res}
	}
	auth := &ambientAuth{user: user, methods: methods}
	if err := applyKnownHosts(&auth.HostKeyCallbackHelper, creds, env, endpoint.Host, &res); err != nil {
		return nil, res, err
	}
	return auth, res, nil
}

func hasExplicitKey(creds SSHCredentials) bool {
	return strings.TrimSpace(creds.PrivateKeyPath) != "" || strings.TrimSpace(creds.PrivateKey) != ""
}

func explicitPublicKeys(creds SSHCredentials, user string) (*gitssh.PublicKeys, error) {
	if strings.TrimSpace(creds.PrivateKey) != "" {
		return gitssh.NewPublicKeys(user, []byte(creds.PrivateKey), creds.Passphrase)
	}
	return gitssh.NewPublicKeysFromFile(user, creds.PrivateKeyPath, creds.Passphrase)
}

// resolveHostWithPort mirrors go-git's getHostWithPort so the host key
// algorithms are selected for the address go-git will dial.
func resolveHostWithPort(env Environment, alias string, port int, rec *recorder) string {
	host := alias
	hostnames, err := env.SSHConfig(alias, "Hostname")
	if err != nil {
		rec.sshConfigError(err)
	}
	if configured := firstValue(hostnames); configured != "" {
		host = configured
		ports, portErr := env.SSHConfig(alias, "Port")
		if portErr != nil {
			rec.sshConfigError(portErr)
		}
		if configuredPort := firstValue(ports); configuredPort != "" {
			if parsed, convErr := strconv.Atoi(configuredPort); convErr == nil {
				port = parsed
			}
		}
	}
	if port <= 0 {
		port = gitssh.DefaultPort
	}
	return net.JoinHostPort(host, strconv.Itoa(port))
}

// applyKnownHosts resolves the known_hosts file list exactly once and sets both
// the host key callback and the host key algorithms for the dialed address.
// go-git only derives algorithms itself when no callback is set, so both the
// explicit and the ambient path need this.
func applyKnownHosts(helper *gitssh.HostKeyCallbackHelper, creds SSHCredentials, env Environment, host string, res *Resolution) error {
	files := knownHostsFiles(creds, env)
	if len(files) == 0 {
		return fmt.Errorf("no known_hosts file: pass --git-known-hosts-file or create ~/.ssh/known_hosts (ssh-keyscan %s >> ~/.ssh/known_hosts)", keyscanHost(res.HostWithPort, host))
	}
	db, err := gitssh.NewKnownHostsDb(files...)
	if err != nil {
		return fmt.Errorf("load known_hosts: %s", knownHostsErrorReason(err, creds, env))
	}
	helper.HostKeyCallback = db.HostKeyCallback()
	helper.HostKeyAlgorithms = db.HostKeyAlgorithms(res.HostWithPort)
	res.KnownHosts = files
	return nil
}

func knownHostsFiles(creds SSHCredentials, env Environment) []string {
	if path := strings.TrimSpace(creds.KnownHostsPath); path != "" {
		// Explicit paths are not filtered: an unreadable path must still fail.
		return []string{path}
	}
	var files []string
	if value, ok := env.LookupEnv("SSH_KNOWN_HOSTS"); ok && value != "" {
		files = filepath.SplitList(value)
	} else {
		files = []string{filepath.Join(env.HomeDir, ".ssh", "known_hosts"), env.SystemKnownHosts}
	}
	return existingOnly(files)
}

// keyscanHost names the host go-git actually dials, so the ssh-keyscan hint
// stays runnable when an ssh_config Hostname directive rewrites the alias.
func keyscanHost(hostWithPort, alias string) string {
	if host, _, err := net.SplitHostPort(hostWithPort); err == nil && host != "" {
		return host
	}
	return alias
}

// knownHostsErrorReason renders a known_hosts failure for a user-facing error.
// An explicit --git-known-hosts-file path was typed by the operator, so it is
// echoed back verbatim; the ambient list is built from the home directory, and
// these strings land in PR comments, so every absolute path is replaced by the
// label the documentation uses.
func knownHostsErrorReason(err error, creds SSHCredentials, env Environment) string {
	if strings.TrimSpace(creds.KnownHostsPath) != "" {
		return err.Error()
	}
	reason := err.Error()
	if pathErr, ok := errors.AsType[*fs.PathError](err); ok {
		reason = knownHostsLabel(pathErr.Path, env) + ": " + pathErr.Err.Error()
	}
	for _, file := range knownHostsFiles(SSHCredentials{}, env) {
		reason = strings.ReplaceAll(reason, file, knownHostsLabel(file, env))
	}
	return reason
}

// knownHostsLabel names an ambient known_hosts file without its directory.
func knownHostsLabel(path string, env Environment) string {
	clean := filepath.Clean(path)
	switch clean {
	case filepath.Clean(filepath.Join(env.HomeDir, ".ssh", "known_hosts")):
		return userKnownHostsLabel
	case filepath.Clean(env.SystemKnownHosts):
		return systemKnownHostsLabel
	default:
		return filepath.Base(clean)
	}
}

func existingOnly(files []string) []string {
	out := make([]string, 0, len(files))
	for _, file := range files {
		if strings.TrimSpace(file) == "" {
			continue
		}
		if _, err := os.Stat(file); err == nil {
			out = append(out, file)
		}
	}
	return out
}

// ambientMethods collects the SSH auth methods discovered from the agent and
// the ~/.ssh identities, in OpenSSH's order.
func ambientMethods(env Environment, host string, rec *recorder, res *Resolution) []ssh.AuthMethod {
	var methods []ssh.AuthMethod
	agentFingerprints := agentMethod(env, host, rec, res, &methods)

	signers := loadIdentityFiles(env, host, rec, res, agentFingerprints)
	if len(signers) > 0 {
		methods = append(methods, ssh.PublicKeys(signers...))
	}
	return methods
}

// loadIdentityFiles parses every candidate key file, dropping the ones the
// agent already offers and capping the total identity count.
func loadIdentityFiles(env Environment, host string, rec *recorder, res *Resolution, agentFingerprints map[string]bool) []ssh.Signer {
	budget := maxIdentities - res.AgentKeys
	var signers []ssh.Signer
	for _, candidate := range identityCandidates(env, host, rec) {
		name := filepath.Base(candidate)
		signer, ok := loadIdentityFile(candidate, name, rec)
		if !ok {
			continue
		}
		if agentFingerprints[ssh.FingerprintSHA256(signer.PublicKey())] {
			rec.skip(name, "already offered by the agent")
			continue
		}
		if len(signers) >= budget {
			rec.skip(name, budgetSkipReason(res.AgentKeys))
			continue
		}
		signers = append(signers, signer)
		res.IdentityFiles = append(res.IdentityFiles, candidate)
	}
	return signers
}

// budgetSkipReason explains why a file identity is left out. The cap applies
// only to the identities drydock loads: an agent holding more than
// maxIdentities keys still offers all of them, so the reason names the agent's
// share rather than claiming a cap drydock does not enforce on the agent.
func budgetSkipReason(agentKeys int) string {
	switch {
	case agentKeys >= maxIdentities:
		return fmt.Sprintf("not offered; the agent's keys already fill the %d-identity budget", maxIdentities)
	case agentKeys > 0:
		return fmt.Sprintf("not offered; the agent already offers %d of the %d identities sent per connection", agentKeys, maxIdentities)
	default:
		return fmt.Sprintf("not offered; at most %d identities are sent per connection", maxIdentities)
	}
}

func loadIdentityFile(path, name string, rec *recorder) (ssh.Signer, bool) {
	contents, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			rec.skip(name, "unreadable: "+fileErrorReason(err))
		}
		return nil, false
	}
	signer, err := ssh.ParsePrivateKey(contents)
	if err == nil {
		return signer, true
	}
	if _, ok := errors.AsType[*ssh.PassphraseMissingError](err); ok {
		rec.skip(name, "passphrase-protected; pass it with --git-ssh-key-file (and --git-ssh-passphrase)")
		return nil, false
	}
	rec.skip(name, "unusable private key")
	return nil, false
}

// identityCandidates lists the ssh_config IdentityFile entries for host
// followed by the default ~/.ssh key files, de-duplicated by cleaned path.
func identityCandidates(env Environment, host string, rec *recorder) []string {
	configured, err := env.SSHConfig(host, "IdentityFile")
	if err != nil {
		rec.sshConfigError(err)
	}
	candidates := make([]string, 0, len(configured)+len(defaultIdentityFiles))
	for _, value := range configured {
		path, ok := expandPath(value, env.HomeDir)
		if !ok {
			rec.skip(filepath.Base(unquote(value)), "ssh_config IdentityFile uses an unsupported token")
			continue
		}
		candidates = append(candidates, path)
	}
	for _, name := range defaultIdentityFiles {
		candidates = append(candidates, filepath.Join(env.HomeDir, ".ssh", name))
	}
	return dedupe(candidates)
}

// expandPath expands the ~ and %d home-directory tokens OpenSSH accepts and
// rejects values that still carry an unsupported token.
func expandPath(value, home string) (string, bool) {
	value = unquote(value)
	if value == "" {
		return "", false
	}
	rest := value
	fromHome := false
	switch {
	case value == "~" || value == "%d":
		return home, true
	case strings.HasPrefix(value, "~/"):
		rest, fromHome = value[2:], true
	case strings.HasPrefix(value, "%d/"):
		rest, fromHome = value[3:], true
	}
	if strings.ContainsAny(rest, "%$") {
		return "", false
	}
	if !fromHome && filepath.IsAbs(rest) {
		return filepath.Clean(rest), true
	}
	return filepath.Join(home, rest), true
}

func unquote(value string) string {
	return strings.Trim(strings.TrimSpace(value), `"`)
}

func firstValue(values []string) string {
	for _, value := range values {
		if trimmed := unquote(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func dedupe(paths []string) []string {
	seen := make(map[string]struct{}, len(paths))
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		clean := filepath.Clean(path)
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		out = append(out, clean)
	}
	return out
}

func baseNames(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		out = append(out, filepath.Base(path))
	}
	return out
}

// fileErrorReason strips the absolute path a *fs.PathError carries: error
// strings reach PR comments verbatim.
func fileErrorReason(err error) string {
	if pathErr, ok := errors.AsType[*fs.PathError](err); ok {
		return pathErr.Err.Error()
	}
	return err.Error()
}

// recorder collects de-duplicated "<name>: <reason>" skip entries.
type recorder struct {
	skipped []string
	seen    map[string]struct{}
}

func (r *recorder) add(entry string) {
	if r.seen == nil {
		r.seen = map[string]struct{}{}
	}
	if _, ok := r.seen[entry]; ok {
		return
	}
	r.seen[entry] = struct{}{}
	r.skipped = append(r.skipped, entry)
}

func (r *recorder) skip(name, reason string) {
	if name == "" {
		name = "identity"
	}
	r.add(name + ": " + reason)
}

func (r *recorder) sshConfigError(err error) {
	r.add("ssh_config: " + err.Error())
}

func (r *recorder) entries() []string {
	return r.skipped
}

// ambientAuth is the go-git auth method built from discovered identities.
type ambientAuth struct {
	user    string
	methods []ssh.AuthMethod
	gitssh.HostKeyCallbackHelper
}

func (a *ambientAuth) Name() string { return ambientAuthName }

func (a *ambientAuth) String() string { return "user: " + a.user + ", name: " + a.Name() }

func (a *ambientAuth) ClientConfig() (*ssh.ClientConfig, error) {
	return a.SetHostKeyCallbackAndAlgorithms(&ssh.ClientConfig{User: a.user, Auth: a.methods})
}
