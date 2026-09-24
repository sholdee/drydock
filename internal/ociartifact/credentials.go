package ociartifact

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"os"

	"helm.sh/helm/v4/pkg/registry"

	"github.com/argoproj/argo-cd/v3/util/oci"
)

// Credentials carries OCI registry auth and TLS material in flag-shaped form
// (file PATHS, not contents), mirroring chart.ChartCredentials. The single
// global set is presented to every OCI registry a run touches (helm/git
// parity); per-registry maps stay a recorded follow-up.
type Credentials struct {
	Username           string
	Password           string
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
