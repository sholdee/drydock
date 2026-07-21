package ociartifact

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"

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
