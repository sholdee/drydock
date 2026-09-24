package gitauth

import (
	"errors"
	"net"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

const (
	agentSourceIdentityAgent = "IdentityAgent"
	agentSourceEnv           = "SSH_AUTH_SOCK"
)

// agentConnection keeps an agent client and the connection it signs through.
// Agent signers sign through the connection, so it stays open for the process
// lifetime once opened.
type agentConnection struct {
	client agent.ExtendedAgent
	conn   net.Conn
}

var (
	agentCacheMu sync.Mutex
	agentCache   = map[string]*agentConnection{}
)

func dialAgentSocket(socket string) (net.Conn, error) {
	return net.Dial("unix", socket)
}

// agentMethod selects an agent socket, appends its auth method when the agent
// offers keys, and returns the fingerprints it already offers.
func agentMethod(env Environment, host string, rec *recorder, res *Resolution, methods *[]ssh.AuthMethod) map[string]bool {
	socket, source, disabled := selectAgentSocket(env, host, rec)
	res.AgentSource = source
	res.agentDisabled = disabled
	if socket == "" {
		return nil
	}
	client, err := cachedAgent(socket, env.AgentDial)
	if err != nil {
		rec.skip("agent", agentErrorReason(err))
		return nil
	}
	keys, err := client.List()
	if err != nil {
		forgetAgent(socket)
		rec.skip("agent", agentErrorReason(err))
		return nil
	}
	res.Agent = true
	res.AgentKeys = len(keys)
	if len(keys) == 0 {
		return nil
	}
	fingerprints := make(map[string]bool, len(keys))
	for _, key := range keys {
		fingerprints[ssh.FingerprintSHA256(key)] = true
	}
	*methods = append(*methods, ssh.PublicKeysCallback(client.Signers))
	return fingerprints
}

// selectAgentSocket mirrors OpenSSH's IdentityAgent handling: the directive
// wins over SSH_AUTH_SOCK, "none" disables the agent entirely, and the literal
// SSH_AUTH_SOCK selects the variable.
func selectAgentSocket(env Environment, host string, rec *recorder) (socket, source string, disabled bool) {
	configured, err := env.SSHConfig(host, "IdentityAgent")
	if err != nil {
		rec.sshConfigError(err)
	}
	if value := firstValue(configured); value != "" {
		switch {
		case strings.EqualFold(value, "none"):
			return "", "", true
		case value == agentSourceEnv:
			// Fall through to the environment variable.
		default:
			if path, ok := expandPath(value, env.HomeDir); ok {
				return path, agentSourceIdentityAgent, false
			}
			rec.skip("agent", "ssh_config IdentityAgent uses an unsupported token")
		}
	}
	if value, ok := env.LookupEnv(agentSourceEnv); ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value), agentSourceEnv, false
	}
	return "", "", false
}

func cachedAgent(socket string, dial func(socket string) (net.Conn, error)) (agent.ExtendedAgent, error) {
	agentCacheMu.Lock()
	defer agentCacheMu.Unlock()
	if cached, ok := agentCache[socket]; ok {
		return cached.client, nil
	}
	conn, err := dial(socket)
	if err != nil {
		return nil, err
	}
	cached := &agentConnection{client: agent.NewClient(conn), conn: conn}
	agentCache[socket] = cached
	return cached.client, nil
}

func forgetAgent(socket string) {
	agentCacheMu.Lock()
	defer agentCacheMu.Unlock()
	cached, ok := agentCache[socket]
	if !ok {
		return
	}
	delete(agentCache, socket)
	_ = cached.conn.Close()
}

// agentErrorReason strips the socket path a dial error carries: error strings
// reach PR comments verbatim.
func agentErrorReason(err error) string {
	if opErr, ok := errors.AsType[*net.OpError](err); ok && opErr.Err != nil {
		return opErr.Err.Error()
	}
	return err.Error()
}
