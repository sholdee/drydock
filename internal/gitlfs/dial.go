package gitlfs

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
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
//   - a link-local address (169.254.0.0/16, fe80::/10) or another cloud
//     metadata address (fd00:ec2::254 of AWS, 100.100.100.200 of Alibaba
//     Cloud) never, also inside a NAT64 address (64:ff9b::/96);
//   - a loopback or unspecified address only when the batch host itself is
//     loopback, and, for a batch URL from .lfsconfig, the repository host
//     too.
//
// A request through an HTTP proxy dials the proxy, which the guard allows.
// It then checks the target host: an IP address, or the addresses that
// the name resolves to here. A name that does not resolve here goes to
// the proxy, which is then the policy point. The proxy resolves the name
// again, so DNS rebinding stays open through a proxy.
type dialGuard struct {
	allowLoopback bool
	proxies       sync.Map
	resolver      *net.Resolver
	// fromEnvironment finds the proxy of a request. Tests replace it.
	fromEnvironment func(*http.Request) (*url.URL, error)
}

// metadataAddresses are cloud metadata addresses outside the link-local
// ranges.
var metadataAddresses = []net.IP{
	net.ParseIP("fd00:ec2::254"),
	net.ParseIP("100.100.100.200"),
}

// nat64Prefix is the well-known NAT64 prefix, whose last 4 bytes are an
// IPv4 address.
var nat64Prefix = net.IPNet{IP: net.ParseIP("64:ff9b::"), Mask: net.CIDRMask(96, 128)}

func newDialGuard(endpoint Endpoint) *dialGuard {
	batchHost := ""
	if parsed, err := url.Parse(endpoint.URL); err == nil {
		batchHost = parsed.Hostname()
	}
	if endpoint.SSH != nil {
		batchHost = endpoint.SSH.Host
	}
	allow := isLoopbackHost(batchHost) && (!endpoint.FromConfig || isLoopbackHost(endpoint.RepoHost))
	return &dialGuard{allowLoopback: allow, resolver: net.DefaultResolver, fromEnvironment: http.ProxyFromEnvironment}
}

// check fails for an IP address that the guard refuses.
func (g *dialGuard) check(ip net.IP) error {
	switch {
	case ip == nil:
		return fmt.Errorf("%w: not an IP address", errRefusedAddress)
	case ip.To4() == nil && nat64Prefix.Contains(ip):
		return g.check(ip[12:16])
	case slices.ContainsFunc(metadataAddresses, ip.Equal):
		return fmt.Errorf("%w: a cloud metadata address", errRefusedAddress)
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
	proxyURL, err := g.fromEnvironment(request)
	if err != nil || proxyURL == nil {
		return proxyURL, err
	}
	if err := g.checkHost(request.Context(), request.URL.Hostname()); err != nil {
		return nil, err
	}
	g.proxies.Store(proxyAddress(proxyURL), true)
	return proxyURL, nil
}

// checkHost checks the target host of a proxied request: an IP address,
// localhost, or the addresses that the name resolves to here. A name that
// does not resolve here passes, so a name that only the proxy knows works.
func (g *dialGuard) checkHost(ctx context.Context, host string) error {
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil {
		return g.check(ip)
	}
	if isLoopbackHost(host) {
		return g.check(net.IPv6loopback)
	}
	// A failed lookup leaves no addresses to check.
	addresses, _ := g.resolver.LookupIPAddr(ctx, host)
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
