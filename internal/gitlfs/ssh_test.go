package gitlfs

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/pem"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"github.com/sholdee/drydock/internal/gitauth"
	"github.com/sholdee/drydock/internal/gitlfs/lfstest"
)

// sshServer is an in-process SSH server that answers exec requests.
type sshServer struct {
	addr        string
	connections atomic.Int64
	mu          sync.Mutex
	commands    []string
	answer      func(command string) (string, uint32)
	options     SSHOptions
}

func (s *sshServer) Commands() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.commands...)
}

// startSSHServer starts the server with a new host key and a new client
// key, and returns SSHOptions that trust the host key in a known_hosts
// file and log in with the client key. It never reads ~/.ssh.
func startSSHServer(t *testing.T, answer func(command string) (string, uint32)) *sshServer {
	t.Helper()
	_, hostPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostSigner, err := ssh.NewSignerFromKey(hostPrivate)
	if err != nil {
		t.Fatal(err)
	}
	clientPublic, clientPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authorized, err := ssh.NewPublicKey(clientPublic)
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{PublicKeyCallback: func(_ ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
		if string(key.Marshal()) != string(authorized.Marshal()) {
			return nil, os.ErrPermission
		}
		return &ssh.Permissions{}, nil
	}}
	config.AddHostKey(hostSigner)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	server := &sshServer{addr: listener.Addr().String(), answer: answer}
	go server.serve(listener, config)

	block, err := ssh.MarshalPrivateKey(clientPrivate, "")
	if err != nil {
		t.Fatal(err)
	}
	knownHostsPath := filepath.Join(t.TempDir(), "known_hosts")
	line := knownhosts.Line([]string{knownhosts.Normalize(server.addr)}, hostSigner.PublicKey())
	if err := os.WriteFile(knownHostsPath, []byte(line+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	server.options = SSHOptions{
		Credentials: gitauth.SSHCredentials{PrivateKey: string(pem.EncodeToMemory(block)), KnownHostsPath: knownHostsPath},
		Environment: gitauth.Environment{
			LookupEnv:           func(string) (string, bool) { return "", false },
			HomeDir:             home,
			SystemSSHConfigPath: filepath.Join(home, "no-ssh-config"),
			SystemKnownHosts:    filepath.Join(home, "no-known-hosts"),
			SSHConfig:           func(string, string) ([]string, error) { return nil, nil },
		},
	}
	return server
}

func (s *sshServer) serve(listener net.Listener, config *ssh.ServerConfig) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		s.connections.Add(1)
		go s.handle(conn, config)
	}
}

func (s *sshServer) handle(conn net.Conn, config *ssh.ServerConfig) {
	defer func() { _ = conn.Close() }()
	_, channels, requests, err := ssh.NewServerConn(conn, config)
	if err != nil {
		return
	}
	go ssh.DiscardRequests(requests)
	for newChannel := range channels {
		channel, channelRequests, err := newChannel.Accept()
		if err != nil {
			continue
		}
		for request := range channelRequests {
			if request.Type != "exec" || len(request.Payload) < 4 {
				_ = request.Reply(false, nil)
				continue
			}
			command := string(request.Payload[4:])
			_ = request.Reply(true, nil)
			s.mu.Lock()
			s.commands = append(s.commands, command)
			s.mu.Unlock()
			stdout, status := s.answer(command)
			_, _ = channel.Write([]byte(stdout))
			code := make([]byte, 4)
			binary.BigEndian.PutUint32(code, status)
			_, _ = channel.SendRequest("exit-status", false, code)
			_ = channel.Close()
		}
	}
}

func sshRepoURL(server *sshServer) string {
	return "ssh://git@" + server.addr + "/org/repo.git"
}

// TestFillAuthenticatesOverSSH pins the SSH path of git-lfs: drydock runs
// `git-lfs-authenticate '<path>' download` on the SSH remote, checks the
// host key against known_hosts, sends the header of the answer to the
// batch href and never to the object host, never sends the --git-* HTTP
// credentials for an SSH remote, reuses the answer until it expires, and
// never runs SSH with --offline.
func TestFillAuthenticatesOverSSH(t *testing.T) {
	lfs := lfstest.New(t, true, true)
	lfs.Add(testContent)
	server := startSSHServer(t, func(string) (string, uint32) {
		return `{"href":"` + lfs.Batch.URL + `/org/repo.git/info/lfs","header":{"Authorization":"RemoteAuth ssh-token"},"expires_in":3600}`, 0
	})
	endpoint, err := FindEndpoint(sshRepoURL(server), nil)
	if err != nil || endpoint.SSH == nil {
		t.Fatalf("FindEndpoint() = %+v, %v", endpoint, err)
	}
	options := func(c fillCase) Options {
		return Options{
			CacheDir: c.store, AllowNetwork: true, Endpoint: endpoint, SSH: server.options, TLSConfig: lfs.TLSConfig(),
			Credentials: gitauth.Credentials{BearerToken: gitSecret, Hosts: []string{hostEntry(t, lfs.Batch.URL)}},
		}
	}
	c := newFillCase(t, testContent)
	if result := c.fill(t, options(c)); len(result.Pointers()) != 0 || c.file(t) != testContent {
		t.Fatalf("Fill(ssh) = %+v, want the content", result)
	}
	if commands := server.Commands(); len(commands) != 1 || commands[0] != "git-lfs-authenticate '/org/repo.git' download" {
		t.Fatalf("SSH commands = %q", commands)
	}
	batch := lfs.RequestsTo("batch")
	if len(batch) != 1 || batch[0].Header.Get("Authorization") != "RemoteAuth ssh-token" {
		t.Fatalf("batch requests = %+v, want the header of git-lfs-authenticate", batch)
	}
	if headersHold(lfs.Requests(), gitSecret) || headersHold(lfs.RequestsTo("objects"), "ssh-token") {
		t.Fatal("a request carries the --git-* token, or the object host got the SSH header")
	}

	c = newFillCase(t, testContent)
	c.fill(t, options(c))
	if len(server.Commands()) != 1 {
		t.Fatalf("SSH commands = %d, want the cached answer reused", len(server.Commands()))
	}
	c = newFillCase(t, testContent)
	offline := options(c)
	offline.AllowNetwork = false
	offline.CacheDir = filepath.Join(t.TempDir(), "lfs")
	before := server.connections.Load()
	if result := c.fill(t, offline); len(result.Missing) != 1 || server.connections.Load() != before {
		t.Fatalf("Fill(offline) = %+v, SSH connections %d -> %d; want none", result, before, server.connections.Load())
	}
}

// TestFillFailsWhenSSHAuthenticateFails pins that a failing
// git-lfs-authenticate fails the fill, as git-lfs fails the fetch after
// its retries, with the stderr-free status in the message.
func TestFillFailsWhenSSHAuthenticateFails(t *testing.T) {
	server := startSSHServer(t, func(string) (string, uint32) { return "", 1 })
	endpoint, err := FindEndpoint(sshRepoURL(server), nil)
	if err != nil {
		t.Fatal(err)
	}
	c := newFillCase(t, testContent)
	message := c.fail(t, Options{CacheDir: c.store, AllowNetwork: true, Endpoint: endpoint, SSH: server.options})
	if !strings.Contains(message, "git-lfs-authenticate") {
		t.Fatalf("error %q, want the git-lfs-authenticate failure", message)
	}
}

// TestFillDropsTheSSHHeaderOnCrossOriginBatchRedirect pins that the header
// of git-lfs-authenticate goes to the batch href alone: a redirect to
// another port loses it.
func TestFillDropsTheSSHHeaderOnCrossOriginBatchRedirect(t *testing.T) {
	target := lfstest.New(t, true, true)
	target.Add(testContent)
	redirector := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.Batch.URL+request.URL.Path, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(redirector.Close)
	server := startSSHServer(t, func(string) (string, uint32) {
		return `{"href":"` + redirector.URL + `/lfs","header":{"X-Ssh-Token":"ssh-token"}}`, 0
	})
	endpoint, err := FindEndpoint(sshRepoURL(server), nil)
	if err != nil {
		t.Fatal(err)
	}
	c := newFillCase(t, testContent)
	c.fill(t, Options{CacheDir: c.store, AllowNetwork: true, Endpoint: endpoint, SSH: server.options, TLSConfig: target.TLSConfig()})
	if second := target.RequestsTo("batch"); len(second) != 1 || headersHold(second, "ssh-token") {
		t.Fatalf("second hop requests = %+v, want one without the SSH header", second)
	}
}

// TestParseSSHAnswerChecksTheHref pins that only an https href without
// userinfo replaces the https endpoint that git-lfs derives, and how the
// expiry is read.
func TestParseSSHAnswerChecksTheHref(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	const fallback = "https://git.example.test/org/repo.git/info/lfs"
	for answer, want := range map[string]string{
		`{"href":"https://lfs.example.test/r"}`:     "https://lfs.example.test/r",
		`{"href":"http://lfs.example.test/r"}`:      fallback,
		`{"href":"https://u:p@lfs.example.test/r"}`: fallback,
		`{"href":""}`: fallback,
		`{"href":"https://lfs.example.test/r?token=x"}`: fallback,
	} {
		auth, err := parseSSHAnswer([]byte(answer), fallback, now)
		if err != nil || auth.href != want {
			t.Errorf("parseSSHAnswer(%s) = %q, %v; want %q", answer, auth.href, err, want)
		}
	}
	auth, _ := parseSSHAnswer([]byte(`{"expires_in":60}`), fallback, now)
	if !auth.expires.Equal(now.Add(time.Minute)) {
		t.Errorf("expires_in: expires = %s", auth.expires)
	}
	auth, _ = parseSSHAnswer([]byte(`{"expires_at":"2026-02-01T00:00:00Z","expires_in":60}`), fallback, now)
	if !auth.expires.Equal(time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("expires_at: expires = %s", auth.expires)
	}
	if _, err := parseSSHAnswer([]byte("not json"), fallback, now); err == nil {
		t.Error("parseSSHAnswer(not json) succeeded")
	}
}

// TestFillChecksTheSSHHostKey pins that git-lfs-authenticate runs only on
// a host whose key known_hosts holds.
func TestFillChecksTheSSHHostKey(t *testing.T) {
	server := startSSHServer(t, func(string) (string, uint32) { return `{"href":"https://lfs.example.test/r"}`, 0 })
	other := startSSHServer(t, func(string) (string, uint32) { return "", 0 })
	options := server.options
	options.Credentials.KnownHostsPath = other.options.Credentials.KnownHostsPath
	endpoint, err := FindEndpoint(sshRepoURL(server), nil)
	if err != nil {
		t.Fatal(err)
	}
	c := newFillCase(t, testContent)
	c.fail(t, Options{CacheDir: c.store, AllowNetwork: true, Endpoint: endpoint, SSH: options})
	if len(server.Commands()) != 0 {
		t.Fatalf("SSH commands = %q, want none on an unknown host key", server.Commands())
	}
}
