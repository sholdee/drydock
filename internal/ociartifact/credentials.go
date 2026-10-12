package ociartifact

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"strings"

	"helm.sh/helm/v4/pkg/registry"
	orascreds "oras.land/oras-go/v2/registry/remote/credentials"

	"github.com/argoproj/argo-cd/v3/util/oci"
)

// Credentials carries OCI registry auth and TLS material in flag-shaped form
// (file PATHS, not contents), mirroring chart.ChartCredentials. Auth comes
// from two sources: RegistryConfig is a Docker/Helm registry config file
// (`helm registry login` format) consulted per registry host by
// ForRepository, so one run can authenticate to several registries; the
// Username/Password pair is the global override, presented to every OCI
// artifact registry a run touches and short-circuiting the file whenever
// either half is set. The TLS fields remain one global set (helm/git
// parity).
type Credentials struct {
	Username           string
	Password           string
	RegistryConfig     string
	CAFile             string
	ClientCertFile     string
	ClientKeyFile      string
	InsecureSkipVerify bool
}

// hasTLSConfig reports whether any TLS-implying field is set. Presence of any
// of them disables the loopback plain-HTTP default (see clientCreds):
// username/password are deliberately NOT TLS-implying — basic auth works on
// plain HTTP independently (vendored client.go:154-161 StaticCredential), so
// credentialed hermetic loopback fixtures keep working without TLS flags.
func (c Credentials) hasTLSConfig() bool {
	return c.CAFile != "" || c.ClientCertFile != "" || c.ClientKeyFile != "" || c.InsecureSkipVerify
}

// Validate fails fast at request construction with errors naming the flag.
// The vendored client silently degrades on both mistakes guarded here: the
// AppendCertsFromPEM return for CAPath is ignored (vendored client.go:482 —
// a non-PEM CA file would silently yield an EMPTY RootCAs pool that also
// REPLACES the system pool), and CertData/KeyData are ignored unless BOTH
// are set (client.go:486 — a lone cert or key would be silently dropped).
// RegistryConfig is deliberately NOT checked here: Validate runs on every
// build and diff whether or not an oci:// source exists, so the file is
// validated lazily by ForRepository at the first OCI artifact acquisition —
// the same contract as the chart flow's validateRegistryConfig at first
// fetch. CI that passes --registry-config unconditionally keeps building
// git-only repositories.
func (c Credentials) Validate() error {
	if c.CAFile != "" {
		caData, err := os.ReadFile(c.CAFile)
		if err != nil {
			return fmt.Errorf("--oci-ca-file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caData) {
			return fmt.Errorf("--oci-ca-file %q contains no PEM certificates", c.CAFile)
		}
	}
	if (c.ClientCertFile == "") != (c.ClientKeyFile == "") {
		return fmt.Errorf("--oci-client-cert-file and --oci-client-key-file must be set together")
	}
	if c.ClientCertFile != "" {
		if _, err := tls.LoadX509KeyPair(c.ClientCertFile, c.ClientKeyFile); err != nil {
			return fmt.Errorf("--oci-client-cert-file/--oci-client-key-file: invalid or mismatched pair: %w", err)
		}
	}
	return nil
}

// HTTPClient builds the http.Client that OCI *Helm chart* pulls use, from the
// same --oci-* TLS flags the artifact path consumes. It reports configured =
// false when no TLS-implying flag is set, so the caller leaves the Helm OCI
// puller on helm's own default client. Credentials stay out of it:
// username/password remain artifact-only and OCI Helm chart auth remains
// --registry-config.
//
// The pool rule differs from the artifact path deliberately. The vendored
// Argo client REPLACES the system pool with CAPath (client.go:482); for chart
// pulls the bundle is ADDED to the system pool, because a run that passes
// --oci-ca-file for a private artifact registry must keep pulling public
// charts from e.g. ghcr.io. A system pool that cannot be loaded is an error
// naming the flag rather than a silently narrowed pool.
func (c Credentials) HTTPClient() (client *http.Client, configured bool, err error) {
	if err := c.Validate(); err != nil {
		return nil, false, err
	}
	if !c.hasTLSConfig() {
		return nil, false, nil
	}
	config := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: c.InsecureSkipVerify}
	if c.CAFile != "" {
		caData, err := os.ReadFile(c.CAFile)
		if err != nil {
			return nil, false, fmt.Errorf("--oci-ca-file: %w", err)
		}
		systemPool, err := x509.SystemCertPool()
		if err != nil {
			return nil, false, fmt.Errorf("--oci-ca-file %q: load system certificate pool: %w", c.CAFile, err)
		}
		pool, err := rootCAPool(systemPool, caData)
		if err != nil {
			return nil, false, fmt.Errorf("--oci-ca-file %q %w", c.CAFile, err)
		}
		config.RootCAs = pool
	}
	// Validate proved the pair loads and that neither half stands alone.
	if c.ClientCertFile != "" && c.ClientKeyFile != "" {
		pair, err := tls.LoadX509KeyPair(c.ClientCertFile, c.ClientKeyFile)
		if err != nil {
			return nil, false, fmt.Errorf("--oci-client-cert-file/--oci-client-key-file: %w", err)
		}
		config.Certificates = []tls.Certificate{pair}
	}
	// helm's own transport: a clone of http.DefaultTransport wrapped in the
	// ORAS retry policy (pkg/registry/transport.go). Cloning keeps the TLS
	// config off the process-wide default transport. No client timeout —
	// helm's default client has none and chart layers can be large. A helm
	// bump that changes the base type fails closed with a clear error rather
	// than dropping the flags and surfacing a puzzling x509 failure later.
	transport := registry.NewTransport(false)
	base, ok := transport.Base.(*http.Transport)
	if !ok {
		return nil, false, fmt.Errorf("cannot apply --oci-* TLS material: helm registry transport base is %T, want *http.Transport", transport.Base)
	}
	base.TLSClientConfig = config
	return &http.Client{Transport: transport}, true, nil
}

// rootCAPool returns base plus the certificates in caData, leaving base
// untouched. A nil base means "start empty"; x509.SystemCertPool() is what
// HTTPClient passes.
func rootCAPool(base *x509.CertPool, caData []byte) (*x509.CertPool, error) {
	pool := x509.NewCertPool()
	if base != nil {
		pool = base.Clone()
	}
	if !pool.AppendCertsFromPEM(caData) {
		return nil, errors.New("contains no PEM certificates")
	}
	return pool, nil
}

// ForRepository resolves the credentials to present to repoURL's registry.
// The explicit Username/Password pair wins for every host without opening
// the file; otherwise RegistryConfig is read through oras's own credential
// store — plain `auths` entries, legacy `https://host/` keys, `credsStore`
// and `credHelpers` — under the key `docker login` and `helm registry login`
// write for that host. A host without an entry stays anonymous. The file is
// validated here, at the first oci:// acquisition, rather than in Validate
// (see there). Once the file has been consulted the result carries
// RegistryConfig cleared, so the second call made by DefaultAcquirer.newClient
// is a no-op; with the explicit pair set it is a no-op by the first rule.
//
// Two shapes fail loudly instead of degrading to an anonymous pull that
// would resurface later as an opaque 401 on the private registry: an
// identity/registry token entry (the vendored Argo client sends username
// and password only, util/oci/client.go:90-98,160-165 — helm forwards the
// whole credential, this client cannot), and a credential helper that cannot
// run (oras maps only "credentials not found" to the empty credential,
// native_store.go:87-91; an exec failure such as a missing
// docker-credential-<helper> binary surfaces with the helper named, kept via
// %w). DetectDefaultNativeStore stays false: helm sets it true, which for an
// auth-less file reads the OS keychain — ambient credentials by any
// definition. It is moot for real `helm registry login` files, which oras
// writes `credsStore` back into on the first native-store put.
func (c Credentials) ForRepository(ctx context.Context, repoURL string) (Credentials, error) {
	if c.Username != "" || c.Password != "" {
		return c, nil
	}
	// Trimmed like the chart flow's validateRegistryConfig, so one padded
	// --registry-config value behaves the same in both flows.
	registryConfig := strings.TrimSpace(c.RegistryConfig)
	if registryConfig == "" {
		return c, nil
	}
	// The host segment is about to be echoed in error text and used as a
	// lookup key; a userinfo-bearing spelling would put the embedded secret
	// in both. The acquirer rejects the same shapes before any network call.
	if err := rejectURLUserinfo(repoURL); err != nil {
		return Credentials{}, err
	}
	if err := validateRegistryConfigFile(registryConfig); err != nil {
		return Credentials{}, err
	}
	host, server := registryConfigKey(repoURL)
	store, err := orascreds.NewStore(registryConfig, orascreds.StoreOptions{})
	if err != nil {
		return Credentials{}, fmt.Errorf("--registry-config %q: %w", registryConfig, err)
	}
	cred, err := store.Get(ctx, server)
	if err != nil {
		return Credentials{}, fmt.Errorf("--registry-config %q: credentials for %s: %w", registryConfig, server, err)
	}
	// A token entry has no usable password. The Docker CLI writes such
	// entries with an `auth` field of base64("<token>:") beside
	// `identitytoken`, so the username may be a placeholder rather than
	// empty; the empty password plus a token is the reliable signal.
	if cred.Password == "" && (cred.RefreshToken != "" || cred.AccessToken != "") {
		return Credentials{}, fmt.Errorf("--registry-config %q: the credential for %s is an identity token, which OCI artifact sources cannot use (the OCI client sends username/password only); log in to %s with a username and password or pass --oci-username/--oci-password", registryConfig, server, host)
	}
	resolved := c
	resolved.Username = cred.Username
	resolved.Password = cred.Password
	resolved.RegistryConfig = ""
	return resolved, nil
}

// validateRegistryConfigFile is the lazy existence check behind
// ForRepository, with the chart flow's wording (repository_oci.go
// validateRegistryConfig) and the flag named. oras's config.Load treats a
// missing file as an empty store (credentials/internal/config/config.go:120),
// which would silently turn a typo in the path into anonymous pulls.
func validateRegistryConfigFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("--registry-config %q does not exist", path)
		}
		return fmt.Errorf("--registry-config %q: %w", path, err)
	}
	if info.IsDir() {
		return fmt.Errorf("--registry-config %q must be a file", path)
	}
	return nil
}

// registryConfigKey returns the registry host named by repoURL and the key
// under which Docker-format config files store that host's credentials. oras
// connects to registry-1.docker.io for the docker.io alias
// (registry.Reference.Host, reference.go:240-245) and the Docker CLI keys
// both under https://index.docker.io/v1/ (credentials/registry.go:84-104);
// every other host is its own key, with legacy https://host/ spellings
// matched by the store itself (credentials/internal/config/config.go:170-186).
func registryConfigKey(repoURL string) (host, server string) {
	host, _, _ = strings.Cut(NormalizeURL(repoURL), "/")
	if host == "docker.io" {
		host = "registry-1.docker.io"
	}
	return host, orascreds.ServerAddressFromHostname(host)
}

// clientCreds builds the exact oci.Creds handed to the vendored client
// constructor. The TLS-vs-loopback rule: InsecureHTTPOnly (PlainHTTP) is
// true ONLY for a loopback registry with NO TLS-implying flag set. Any
// TLS-implying flag deterministically disables the loopback plain-HTTP
// default — the vendored client builds its TLS config only when !PlainHTTP
// (client.go:131-139), so every TLS field would otherwise be dead on
// loopback. No flag can ENABLE plain HTTP: flags only disable it via the
// TLS-implying rule, and non-loopback hosts always negotiate TLS. Because
// the credential family is global, one TLS-implying flag flips every
// loopback registry in the run to TLS; anonymous runs are unaffected.
func clientCreds(repoURL string, credentials Credentials) (oci.Creds, error) {
	if err := credentials.Validate(); err != nil {
		return oci.Creds{}, err
	}
	creds := oci.Creds{
		Username:           credentials.Username,
		Password:           credentials.Password,
		CAPath:             credentials.CAFile,
		InsecureSkipVerify: credentials.InsecureSkipVerify,
		InsecureHTTPOnly:   isLoopbackURL(repoURL) && !credentials.hasTLSConfig(),
	}
	// CertData and KeyData must BOTH be set or the vendored client silently
	// ignores them (client.go:486); Validate proved the pair valid above.
	if credentials.ClientCertFile != "" && credentials.ClientKeyFile != "" {
		certData, err := os.ReadFile(credentials.ClientCertFile)
		if err != nil {
			return oci.Creds{}, fmt.Errorf("--oci-client-cert-file: %w", err)
		}
		keyData, err := os.ReadFile(credentials.ClientKeyFile)
		if err != nil {
			return oci.Creds{}, fmt.Errorf("--oci-client-key-file: %w", err)
		}
		creds.CertData = certData
		creds.KeyData = keyData
	}
	return creds, nil
}
