package gitlfs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/sholdee/drydock/internal/gitauth"
)

// Limits of git-lfs-authenticate.
const (
	maxSSHOutput    = 64 << 10
	sshTimeout      = time.Minute
	sshExpiryLeeway = 5 * time.Second
)

// SSHOptions are the SSH credentials of git-lfs-authenticate: the
// --git-ssh-* values and the ambient environment that gitauth.SSHAuth
// reads.
type SSHOptions struct {
	Credentials gitauth.SSHCredentials
	Environment gitauth.Environment
}

// sshAuth is the answer of git-lfs-authenticate: the endpoint and the
// headers for the batch API.
type sshAuth struct {
	href    string
	header  map[string]string
	expires time.Time
}

type sshAnswer struct {
	Href      string            `json:"href"`
	Header    map[string]string `json:"header"`
	ExpiresIn int64             `json:"expires_in"`
	ExpiresAt string            `json:"expires_at"`
}

// sshAuthCache keeps the answers of git-lfs-authenticate per user, host,
// port, and path until they expire, as git-lfs does within a process. An
// answer without an expiry lasts for the process.
var sshAuthCache sync.Map

type sshCacheEntry struct {
	auth sshAuth
}

// authenticateSSH runs `git-lfs-authenticate '<path>' download` on the SSH
// remote, as git-lfs 3.x does when the pure SSH protocol
// (git-lfs-transfer) is not available, and returns the batch endpoint and
// the headers of the answer. The href must be an https URL without
// userinfo; otherwise drydock takes the https endpoint that git-lfs
// derives for the remote. A failure of the command fails the fetch, as in
// git-lfs, which retries it first.
func authenticateSSH(ctx context.Context, remote SSHRemote, fallback string, opts SSHOptions, guard *dialGuard) (sshAuth, error) {
	key := remote.User + "\x00" + remote.Host + "\x00" + strconv.Itoa(remote.Port) + "\x00" + remote.Path
	if cached, ok := sshAuthCache.Load(key); ok {
		entry, _ := cached.(sshCacheEntry)
		if entry.auth.expires.IsZero() || time.Now().Add(sshExpiryLeeway).Before(entry.auth.expires) {
			return entry.auth, nil
		}
	}
	output, err := runSSHAuthenticate(ctx, remote, opts, guard)
	if err != nil {
		return sshAuth{}, err
	}
	auth, err := parseSSHAnswer(output, fallback, time.Now())
	if err != nil {
		return sshAuth{}, err
	}
	sshAuthCache.Store(key, sshCacheEntry{auth: auth})
	return auth, nil
}

// runSSHAuthenticate dials the remote with the SSH auth of gitauth.SSHAuth,
// whose host key callback checks known_hosts, and runs the command.
func runSSHAuthenticate(ctx context.Context, remote SSHRemote, opts SSHOptions, guard *dialGuard) ([]byte, error) {
	method, resolution, err := gitauth.SSHAuth(opts.Credentials, remote.URL, opts.Environment)
	if err != nil {
		return nil, fmt.Errorf("git-lfs-authenticate on %s: %s", remote.Host, sanitize(err.Error()))
	}
	configured, ok := method.(interface {
		ClientConfig() (*ssh.ClientConfig, error)
	})
	if !ok {
		return nil, fmt.Errorf("git-lfs-authenticate on %s: the SSH auth method has no client configuration", remote.Host)
	}
	config, err := configured.ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("git-lfs-authenticate on %s: %s", remote.Host, sanitize(err.Error()))
	}
	address := resolution.HostWithPort
	if address == "" {
		address = net.JoinHostPort(remote.Host, "22")
	}
	ctx, cancel := context.WithTimeout(ctx, sshTimeout)
	defer cancel()
	conn, err := guard.dialer(sshTimeout, true).DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, classifyTransport(fmt.Errorf("git-lfs-authenticate on %s: %s", remote.Host, transportError(err)), err)
	}
	defer func() { _ = conn.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	return runOverConn(conn, address, config, remote)
}

func runOverConn(conn net.Conn, address string, config *ssh.ClientConfig, remote SSHRemote) ([]byte, error) {
	clientConn, channels, requests, err := ssh.NewClientConn(conn, address, config)
	if err != nil {
		return nil, fmt.Errorf("git-lfs-authenticate on %s: the SSH connection failed: %s", remote.Host, sanitize(err.Error()))
	}
	client := ssh.NewClient(clientConn, channels, requests)
	defer func() { _ = client.Close() }()
	session, err := client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("git-lfs-authenticate on %s: %s", remote.Host, sanitize(err.Error()))
	}
	defer func() { _ = session.Close() }()
	stdout := &limitedBuffer{limit: maxSSHOutput}
	stderr := &limitedBuffer{limit: maxMessageLength}
	session.Stdout, session.Stderr = stdout, stderr
	if err := session.Run("git-lfs-authenticate " + shellQuote(remote.Path) + " download"); err != nil {
		message := sanitize(err.Error())
		if text := sanitize(stderr.String()); text != "" {
			message += ": " + text
		}
		return nil, fmt.Errorf("git-lfs-authenticate on %s failed: %s", remote.Host, message)
	}
	if stdout.overflow {
		return nil, fmt.Errorf("git-lfs-authenticate on %s wrote more than %d bytes", remote.Host, maxSSHOutput)
	}
	return stdout.Bytes(), nil
}

// parseSSHAnswer reads the JSON answer of git-lfs-authenticate.
func parseSSHAnswer(output []byte, fallback string, now time.Time) (sshAuth, error) {
	var answer sshAnswer
	if err := json.Unmarshal(output, &answer); err != nil {
		return sshAuth{}, errors.New("the answer of git-lfs-authenticate is not valid JSON")
	}
	if err := checkActionHeaders(answer.Header); err != nil {
		return sshAuth{}, err
	}
	auth := sshAuth{href: fallback, header: answer.Header}
	if href, err := checkEndpoint(strings.TrimSpace(answer.Href), "the href of git-lfs-authenticate"); err == nil && strings.HasPrefix(href, "https://") {
		auth.href = href
	}
	switch {
	case answer.ExpiresAt != "":
		if expires, err := time.Parse(time.RFC3339, answer.ExpiresAt); err == nil {
			auth.expires = expires
		}
	case answer.ExpiresIn > 0:
		auth.expires = now.Add(time.Duration(answer.ExpiresIn) * time.Second)
	}
	return auth, nil
}

// shellQuote quotes value for a POSIX shell, as git quotes the path of
// git-upload-pack.
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// limitedBuffer keeps at most limit bytes and records an overflow.
type limitedBuffer struct {
	bytes.Buffer
	limit    int
	overflow bool
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	room := b.limit - b.Len()
	if len(data) > room {
		b.overflow = true
		if room > 0 {
			b.Buffer.Write(data[:room])
		}
		return len(data), nil
	}
	return b.Buffer.Write(data)
}
