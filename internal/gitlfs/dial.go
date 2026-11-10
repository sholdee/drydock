package gitlfs

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"
)

// errRefusedAddress marks a connection that drydock refuses because of the
// address it resolves to.
var errRefusedAddress = errors.New("refused address")

// dialGuard decides which addresses a Fill may connect to. A .lfsconfig
// file and a batch response name hosts that drydock contacts, so they could
// point drydock at a service of the machine or its network. The guard
// checks the address that drydock dials, after name resolution:
//
//   - a link-local address (169.254.0.0/16, fe80::/10), where cloud
//     metadata services listen, never;
//   - a loopback or unspecified address only when the batch host itself is
//     loopback, and, for a batch URL from .lfsconfig, the repository host
//     too.
//
// A request through an HTTP proxy dials the proxy, which the guard allows;
// it then checks the addresses that the target host name resolves to.
type dialGuard struct {
	allowLoopback bool
	proxies       sync.Map
	resolver      *net.Resolver
}

func newDialGuard(endpoint Endpoint) *dialGuard {
	batchHost := ""
	if parsed, err := url.Parse(endpoint.URL); err == nil {
		batchHost = parsed.Hostname()
	}
	if endpoint.SSH != nil {
		batchHost = endpoint.SSH.Host
	}
	allow := isLoopbackHost(batchHost) && (!endpoint.FromConfig || isLoopbackHost(endpoint.RepoHost))
	return &dialGuard{allowLoopback: allow, resolver: net.DefaultResolver}
}

// check fails for an IP address that the guard refuses.
func (g *dialGuard) check(ip net.IP) error {
	switch {
	case ip == nil:
		return fmt.Errorf("%w: not an IP address", errRefusedAddress)
	case ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast():
		return fmt.Errorf("%w: a link-local address", errRefusedAddress)
	case (ip.IsLoopback() || ip.IsUnspecified()) && !g.allowLoopback:
		return fmt.Errorf("%w: a loopback address for a Git LFS server that is not on loopback", errRefusedAddress)
	}
	return nil
}

// control is the net.Dialer Control hook: address is the IP and port that
// the dialer connects to.
func (g *dialGuard) control(_, address string, _ syscall.RawConn) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("%w: %w", errRefusedAddress, err)
	}
	return g.check(net.ParseIP(host))
}

func (g *dialGuard) dialer(timeout time.Duration, guarded bool) *net.Dialer {
	dialer := &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
	if guarded {
		dialer.Control = g.control
	}
	return dialer
}

// dialContext dials address, and checks it unless it is a proxy that a
// request of this guard uses.
func (g *dialGuard) dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	_, proxy := g.proxies.Load(address)
	return g.dialer(30*time.Second, !proxy).DialContext(ctx, network, address)
}

// proxy is http.ProxyFromEnvironment. For a proxied request it checks the
// addresses of the target host, because the proxy resolves and dials it.
func (g *dialGuard) proxy(request *http.Request) (*url.URL, error) {
	proxyURL, err := http.ProxyFromEnvironment(request)
	if err != nil || proxyURL == nil {
		return proxyURL, err
	}
	if err := g.checkHost(request.Context(), request.URL.Hostname()); err != nil {
		return nil, err
	}
	g.proxies.Store(proxyAddress(proxyURL), true)
	return proxyURL, nil
}

func (g *dialGuard) checkHost(ctx context.Context, host string) error {
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
		return g.check(ip)
	}
	addresses, err := g.resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return err
	}
	for _, address := range addresses {
		if err := g.check(address.IP); err != nil {
			return err
		}
	}
	return nil
}

func proxyAddress(proxyURL *url.URL) string {
	if port := proxyURL.Port(); port != "" {
		return net.JoinHostPort(proxyURL.Hostname(), port)
	}
	port := "80"
	switch proxyURL.Scheme {
	case "https":
		port = "443"
	case "socks5", "socks5h":
		port = "1080"
	}
	return net.JoinHostPort(proxyURL.Hostname(), port)
}
