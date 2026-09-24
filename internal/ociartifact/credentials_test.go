package ociartifact

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"oras.land/oras-go/v2/registry/remote/retry"

	"github.com/sholdee/drydock/internal/ociartifact/ocitest"
)

// TestResolveExtractWithBasicAuth pins the credentialed happy path against a
// Basic-auth registry: ORAS sends an unauthenticated attempt first, parses
// the fixture's Www-Authenticate: Basic challenge on the 401, and retries
// with the static credential (oras-go v2.6.1 auth/client.go:308-323).
func TestResolveExtractWithBasicAuth(t *testing.T) {
	reg := ocitest.StartRegistry(t)
	pushedDigest := ocitest.PushHelmChartArtifact(t, reg, "charts/auth", "1.2.3", ocitest.HelmChartSpec{Name: "auth", Version: "1.2.3"})
	reg.EnableBasicAuth("oci-user", "oci-pass")
	repoURL := reg.RepoURL("charts/auth")
	opts := Options{CacheDir: t.TempDir(), Credentials: Credentials{Username: "oci-user", Password: "oci-pass"}}
	acquirer := DefaultAcquirer{}

	digest, err := acquirer.Resolve(t.Context(), repoURL, "1.2.3", opts)
	if err != nil {
		t.Fatalf("Resolve() with correct creds error = %v", err)
	}
	if digest != pushedDigest {
		t.Fatalf("Resolve() = %q, want %q", digest, pushedDigest)
	}
	dir, release, err := acquirer.Extract(t.Context(), repoURL, digest, opts)
	if err != nil {
		t.Fatalf("Extract() with correct creds error = %v", err)
	}
	defer release()
	if _, err := os.Stat(filepath.Join(dir, "Chart.yaml")); err != nil {
		t.Fatalf("Chart.yaml not at extraction root: %v", err)
	}
}

// Wrong credentials surface as a clear errcode.ErrorResponse-shaped auth
// error: the retried request still gets a 401 and ORAS returns the response
// as an error (no transport failure involved).
func TestResolveWrongBasicAuthFailsClearly(t *testing.T) {
	reg := ocitest.StartRegistry(t)
	ocitest.PushHelmChartArtifact(t, reg, "charts/auth", "1.2.3", ocitest.HelmChartSpec{Name: "auth", Version: "1.2.3"})
	reg.EnableBasicAuth("oci-user", "oci-pass")
	opts := Options{CacheDir: t.TempDir(), Credentials: Credentials{Username: "oci-user", Password: "wrong-pass"}}

	_, err := (DefaultAcquirer{}).Resolve(t.Context(), reg.RepoURL("charts/auth"), "1.2.3", opts)
	if err == nil {
		t.Fatal("Resolve() with wrong creds should fail")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Fatalf("Resolve() error = %q, want errcode-shaped 401 auth error", err)
	}
	if !strings.Contains(err.Error(), "resolve OCI artifact") {
		t.Fatalf("Resolve() error = %q, want artifact-naming wrap", err)
	}
}

// TestTLSRegistryTLSVsLoopbackRule pins the TLS-vs-loopback rule end-to-end
// against a real https loopback registry: any TLS-implying flag disables the
// loopback plain-HTTP default, and the resulting TLS handshake honors the
// specific flag set.
func TestTLSRegistryTLSVsLoopbackRule(t *testing.T) {
	reg := ocitest.StartTLSRegistry(t)
	pushedDigest := ocitest.PushHelmChartArtifact(t, reg, "charts/tls", "1.2.3", ocitest.HelmChartSpec{Name: "tls", Version: "1.2.3"})
	repoURL := reg.RepoURL("charts/tls")
	acquirer := DefaultAcquirer{}

	t.Run("ca file renders", func(t *testing.T) {
		opts := Options{CacheDir: t.TempDir(), Credentials: Credentials{CAFile: reg.CAFilePath(t)}}
		digest, err := acquirer.Resolve(t.Context(), repoURL, "1.2.3", opts)
		if err != nil {
			t.Fatalf("Resolve() with --oci-ca-file error = %v", err)
		}
		if digest != pushedDigest {
			t.Fatalf("Resolve() = %q, want %q", digest, pushedDigest)
		}
		dir, release, err := acquirer.Extract(t.Context(), repoURL, digest, opts)
		if err != nil {
			t.Fatalf("Extract() with --oci-ca-file error = %v", err)
		}
		defer release()
		if _, err := os.Stat(filepath.Join(dir, "Chart.yaml")); err != nil {
			t.Fatalf("Chart.yaml not at extraction root: %v", err)
		}
	})

	t.Run("tls-implying flag without ca trust fails x509", func(t *testing.T) {
		// A valid client pair is TLS-implying, so PlainHTTP is disabled, but
		// the self-signed server cert fails system-pool verification.
		certFile, keyFile := ocitest.GenerateClientCertFiles(t)
		opts := Options{CacheDir: t.TempDir(), Credentials: Credentials{ClientCertFile: certFile, ClientKeyFile: keyFile}}
		_, err := acquirer.Resolve(t.Context(), repoURL, "1.2.3", opts)
		if err == nil {
			t.Fatal("Resolve() without CA trust should fail")
		}
		if !strings.Contains(err.Error(), "x509") {
			t.Fatalf("Resolve() error = %q, want x509 verification failure", err)
		}
	})

	t.Run("insecure skip verify renders", func(t *testing.T) {
		opts := Options{CacheDir: t.TempDir(), Credentials: Credentials{InsecureSkipVerify: true}}
		digest, err := acquirer.Resolve(t.Context(), repoURL, "1.2.3", opts)
		if err != nil {
			t.Fatalf("Resolve() with --oci-insecure-skip-verify error = %v", err)
		}
		if digest != pushedDigest {
			t.Fatalf("Resolve() = %q, want %q", digest, pushedDigest)
		}
	})
}

// TestMTLSRegistryClientCertPair pins mutual TLS: the registry requires and
// verifies a client certificate.
func TestMTLSRegistryClientCertPair(t *testing.T) {
	reg, certFile, keyFile := ocitest.StartMTLSRegistry(t)
	pushedDigest := ocitest.PushHelmChartArtifact(t, reg, "charts/mtls", "1.2.3", ocitest.HelmChartSpec{Name: "mtls", Version: "1.2.3"})
	repoURL := reg.RepoURL("charts/mtls")
	acquirer := DefaultAcquirer{}

	opts := Options{CacheDir: t.TempDir(), Credentials: Credentials{
		CAFile:         reg.CAFilePath(t),
		ClientCertFile: certFile,
		ClientKeyFile:  keyFile,
	}}
	digest, err := acquirer.Resolve(t.Context(), repoURL, "1.2.3", opts)
	if err != nil {
		t.Fatalf("Resolve() with client pair error = %v", err)
	}
	if digest != pushedDigest {
		t.Fatalf("Resolve() = %q, want %q", digest, pushedDigest)
	}
	dir, release, err := acquirer.Extract(t.Context(), repoURL, digest, opts)
	if err != nil {
		t.Fatalf("Extract() with client pair error = %v", err)
	}
	defer release()
	if _, err := os.Stat(filepath.Join(dir, "Chart.yaml")); err != nil {
		t.Fatalf("Chart.yaml not at extraction root: %v", err)
	}

	// Missing pair: the server rejects the handshake with a clear TLS error.
	bare := Options{CacheDir: t.TempDir(), Credentials: Credentials{CAFile: reg.CAFilePath(t)}}
	if _, err := acquirer.Resolve(t.Context(), repoURL, "1.2.3", bare); err == nil {
		t.Fatal("Resolve() without client pair should fail")
	} else if !strings.Contains(err.Error(), "tls") {
		t.Fatalf("Resolve() error = %q, want TLS handshake failure", err)
	}
}

// TestClientCredsConstructionGuard is the seam-level truth table for the
// exact oci.Creds handed to the vendored client constructor, for loopback
// and non-loopback URLs under every flag combination:
//   - InsecureHTTPOnly is true ONLY for loopback with no TLS-implying flag;
//   - InsecureHTTPOnly is NEVER true for non-loopback, and no user flag can
//     ENABLE it (flags only disable it via the TLS-implying rule);
//   - InsecureSkipVerify comes only from its flag;
//   - CertData/KeyData are both populated exactly when the pair is given.
//
// TestIsLoopbackURLPlainHTTPBoundary stays authoritative for host
// classification; this guard pins what construction does with it.
func TestClientCredsConstructionGuard(t *testing.T) {
	certFile, keyFile := ocitest.GenerateClientCertFiles(t)
	caFile, _ := ocitest.GenerateClientCertFiles(t)
	urls := []struct {
		url      string
		loopback bool
	}{
		{"oci://127.0.0.1:5000/org/app", true},
		{"oci://localhost:5000/org/app", true},
		{"oci://ghcr.io/org/app", false},
	}
	for _, target := range urls {
		for mask := range 16 {
			combo := credsCombo{
				useUserPass:   mask&1 != 0,
				useCA:         mask&2 != 0,
				usePair:       mask&4 != 0,
				useSkipVerify: mask&8 != 0,
				caFile:        caFile,
				certFile:      certFile,
				keyFile:       keyFile,
			}
			assertClientCreds(t, target.url, target.loopback, combo)
		}
	}
}

type credsCombo struct {
	useUserPass   bool
	useCA         bool
	usePair       bool
	useSkipVerify bool
	caFile        string
	certFile      string
	keyFile       string
}

func (combo credsCombo) credentials() Credentials {
	credentials := Credentials{InsecureSkipVerify: combo.useSkipVerify}
	if combo.useUserPass {
		credentials.Username = "user"
		credentials.Password = "pass"
	}
	if combo.useCA {
		credentials.CAFile = combo.caFile
	}
	if combo.usePair {
		credentials.ClientCertFile = combo.certFile
		credentials.ClientKeyFile = combo.keyFile
	}
	return credentials
}

// tlsImplying mirrors the rule under test from the flag side: any TLS flag —
// and ONLY a TLS flag — disables the loopback plain-HTTP default.
func (combo credsCombo) tlsImplying() bool {
	return combo.useCA || combo.usePair || combo.useSkipVerify
}

func assertClientCreds(t *testing.T, url string, loopback bool, combo credsCombo) {
	t.Helper()
	credentials := combo.credentials()
	got, err := clientCreds(url, credentials)
	if err != nil {
		t.Fatalf("clientCreds(%q, %+v) error = %v", url, credentials, err)
	}
	wantPlainHTTP := loopback && !combo.tlsImplying()
	if got.InsecureHTTPOnly != wantPlainHTTP {
		t.Fatalf("clientCreds(%q, %+v).InsecureHTTPOnly = %v, want %v", url, credentials, got.InsecureHTTPOnly, wantPlainHTTP)
	}
	if !loopback && got.InsecureHTTPOnly {
		t.Fatalf("clientCreds(%q, %+v) enabled InsecureHTTPOnly for a non-loopback host", url, credentials)
	}
	if got.InsecureSkipVerify != combo.useSkipVerify {
		t.Fatalf("clientCreds(%q, %+v).InsecureSkipVerify = %v, want %v", url, credentials, got.InsecureSkipVerify, combo.useSkipVerify)
	}
	if (got.Username == "user") != combo.useUserPass || (got.Password == "pass") != combo.useUserPass {
		t.Fatalf("clientCreds(%q, %+v) user/pass = %q/%q", url, credentials, got.Username, got.Password)
	}
	if (got.CAPath == combo.caFile) != combo.useCA {
		t.Fatalf("clientCreds(%q, %+v).CAPath = %q", url, credentials, got.CAPath)
	}
	if (len(got.CertData) > 0) != combo.usePair || (len(got.KeyData) > 0) != combo.usePair {
		t.Fatalf("clientCreds(%q, %+v) cert/key data lengths = %d/%d", url, credentials, len(got.CertData), len(got.KeyData))
	}
}

// TestCredentialsValidate pins the fail-fast validation errors, each naming
// the responsible flag: the vendored client would otherwise silently ignore
// a non-PEM CA (empty pool replacing the system pool) or a lone cert/key
// half (client.go:482,486).
func TestCredentialsValidate(t *testing.T) {
	certFile, keyFile := ocitest.GenerateClientCertFiles(t)
	otherCertFile, _ := ocitest.GenerateClientCertFiles(t)
	nonPEM := filepath.Join(t.TempDir(), "not-a-cert.pem")
	if err := os.WriteFile(nonPEM, []byte("not pem data"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, testCase := range []struct {
		name        string
		credentials Credentials
		wantSubstr  []string
	}{
		{
			name:        "missing CA file",
			credentials: Credentials{CAFile: filepath.Join(t.TempDir(), "missing.pem")},
			wantSubstr:  []string{"--oci-ca-file"},
		},
		{
			name:        "non-PEM CA file",
			credentials: Credentials{CAFile: nonPEM},
			wantSubstr:  []string{"--oci-ca-file", "PEM"},
		},
		{
			name:        "cert without key",
			credentials: Credentials{ClientCertFile: certFile},
			wantSubstr:  []string{"--oci-client-cert-file", "--oci-client-key-file", "together"},
		},
		{
			name:        "key without cert",
			credentials: Credentials{ClientKeyFile: keyFile},
			wantSubstr:  []string{"--oci-client-cert-file", "--oci-client-key-file", "together"},
		},
		{
			name:        "mismatched pair",
			credentials: Credentials{ClientCertFile: otherCertFile, ClientKeyFile: keyFile},
			wantSubstr:  []string{"--oci-client-cert-file", "--oci-client-key-file", "mismatched"},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			err := testCase.credentials.Validate()
			if err == nil {
				t.Fatalf("Validate(%+v) = nil, want error", testCase.credentials)
			}
			for _, want := range testCase.wantSubstr {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("Validate(%+v) error = %q, want substring %q", testCase.credentials, err, want)
				}
			}
			// Errors must name paths, never echo file contents — a private
			// key mistakenly passed to a file flag must not print.
			if strings.Contains(err.Error(), "not pem data") {
				t.Fatalf("Validate(%+v) error %q echoes file contents", testCase.credentials, err)
			}
		})
	}
	if err := (Credentials{}).Validate(); err != nil {
		t.Fatalf("Validate(zero) = %v, want nil", err)
	}
	if err := (Credentials{ClientCertFile: certFile, ClientKeyFile: keyFile}).Validate(); err != nil {
		t.Fatalf("Validate(valid pair) = %v, want nil", err)
	}
}

// TestURLUserinfoRejected pins the early rejection of URL-carried
// credentials: the secret never reaches the vendored client (whose logrus
// and oras error paths would echo it) and the rejection error names the
// credential flags while carrying only the redacted URL.
func TestURLUserinfoRejected(t *testing.T) {
	acquirer := DefaultAcquirer{}
	opts := Options{CacheDir: t.TempDir()}

	// The slash-shifted shapes bypassed the original authority-only scan: an
	// unencoded "/" inside the embedded credentials moves the "@" past the
	// first path cut, and the username variant also parses with a nil
	// url.User, defeating parse-based redaction.
	for _, repoURL := range []string{
		"oci://leak-user:leak-secret@127.0.0.1:1/org/app",
		"oci://leak-user:leak-se/cret-tail@127.0.0.1:1/org/app",
		"oci://leak-us/er:leak-secret@127.0.0.1:1/org/app",
		"oci://leak-user:leak-secret%40127.0.0.1:1/org/app",
	} {
		testURLUserinfoRejected(t, acquirer, opts, repoURL)
	}
}

func testURLUserinfoRejected(t *testing.T, acquirer DefaultAcquirer, opts Options, repoURL string) {
	t.Helper()
	for name, call := range map[string]func() error{
		"resolve tag": func() error {
			_, err := acquirer.Resolve(t.Context(), repoURL, "1.0.0", opts)
			return err
		},
		"resolve digest": func() error {
			_, err := acquirer.Resolve(t.Context(), repoURL, "sha256:"+strings.Repeat("ab", 32), opts)
			return err
		},
		"extract": func() error {
			_, _, err := acquirer.Extract(t.Context(), repoURL, "sha256:"+strings.Repeat("ab", 32), opts)
			return err
		},
	} {
		err := call()
		if err == nil {
			t.Fatalf("%s: expected userinfo rejection error", name)
		}
		for _, flag := range []string{"--oci-username", "--oci-password"} {
			if !strings.Contains(err.Error(), flag) {
				t.Fatalf("%s: error %q should name %s", name, err, flag)
			}
		}
		for _, leaked := range []string{"leak-se", "leak-us"} {
			if strings.Contains(err.Error(), leaked) {
				t.Fatalf("%s: error %q leaks %q", name, err, leaked)
			}
		}
	}
}

// TestNewClientErrorsNameRedactedURL pins the defense-in-depth RedactURL wrap
// on client construction errors, forced through an invalid-but-userinfo-free
// URL (no repository path, so oras reference parsing fails).
func TestNewClientErrorsNameRedactedURL(t *testing.T) {
	acquirer := DefaultAcquirer{}
	opts := Options{CacheDir: t.TempDir()}

	_, err := acquirer.Resolve(t.Context(), "oci://hostonly", "1.0.0", opts)
	if err == nil {
		t.Fatal("Resolve(invalid reference) should fail")
	}
	if !strings.Contains(err.Error(), "initialize OCI client for oci://hostonly") {
		t.Fatalf("Resolve() error = %q, want RedactURL-wrapped construction error", err)
	}

	_, _, err = acquirer.Extract(t.Context(), "oci://hostonly", "sha256:"+strings.Repeat("ab", 32), opts)
	if err == nil {
		t.Fatal("Extract(invalid reference) should fail")
	}
	if !strings.Contains(err.Error(), "initialize OCI client for oci://hostonly") {
		t.Fatalf("Extract() error = %q, want RedactURL-wrapped construction error", err)
	}
}

// TestConstraintResolveSingleTagListFetch pins the housekeeping contract: one
// successful constraint resolve performs exactly ONE /tags/list wire fetch
// (prefetch seeds the memo; ResolveRevision's fallback reads it back). The
// pin runs on the anonymous fixture — auth would double wire requests via
// the 401-retry dance. The timeout guard turns the lazy-fill deadlock
// (non-reentrant indexLock held across cache callbacks) into a test failure
// instead of a hung suite.
func TestConstraintResolveSingleTagListFetch(t *testing.T) {
	reg := ocitest.StartRegistry(t)
	ocitest.PushHelmChartArtifact(t, reg, "charts/count", "1.2.3", ocitest.HelmChartSpec{Version: "1.2.3"})
	want := ocitest.PushHelmChartArtifact(t, reg, "charts/count", "1.2.4", ocitest.HelmChartSpec{Version: "1.2.4"})
	ocitest.PushHelmChartArtifact(t, reg, "charts/count", "2.0.0", ocitest.HelmChartSpec{Version: "2.0.0"})
	repoURL := reg.RepoURL("charts/count")
	opts := Options{CacheDir: t.TempDir()}
	acquirer := DefaultAcquirer{}

	var digest string
	var err error
	done := make(chan struct{})
	go func() {
		defer close(done)
		digest, err = acquirer.Resolve(t.Context(), repoURL, "~1.2", opts)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("Resolve(~1.2) deadlocked: the tags memo must be prefetch-populated, never lazy-filled")
	}
	if err != nil {
		t.Fatalf("Resolve(~1.2) error = %v", err)
	}
	if digest != want {
		t.Fatalf("Resolve(~1.2) = %q, want %q", digest, want)
	}
	if got := reg.TagListRequests(); got != 1 {
		t.Fatalf("constraint resolve made %d /tags/list fetches, want exactly 1", got)
	}

	// Exact-tag resolves never touch the tag list.
	if _, err := acquirer.Resolve(t.Context(), repoURL, "1.2.3", opts); err != nil {
		t.Fatalf("Resolve(1.2.3) error = %v", err)
	}
	if got := reg.TagListRequests(); got != 1 {
		t.Fatalf("exact resolve changed /tags/list fetches to %d, want still 1", got)
	}
}

// TestCredentialedCacheNeutrality pins that credentials never enter cache
// keys or disk state: a credentialed run produces the same entry keys, the
// same records, and the same metadata (timestamps aside) as an anonymous
// run, and a differently-credentialed run reuses the existing entry.
func TestCredentialedCacheNeutrality(t *testing.T) {
	reg := ocitest.StartRegistry(t)
	tagDigest := ocitest.PushHelmChartArtifact(t, reg, "charts/neutral", "1.2.3", ocitest.HelmChartSpec{Name: "neutral", Version: "1.2.3"})
	repoURL := reg.RepoURL("charts/neutral")
	acquirer := DefaultAcquirer{}

	runAcquisition := func(t *testing.T, opts Options) {
		t.Helper()
		if _, err := acquirer.Resolve(t.Context(), repoURL, "1.2.3", opts); err != nil {
			t.Fatalf("Resolve(tag) error = %v", err)
		}
		if _, err := acquirer.Resolve(t.Context(), repoURL, "~1.2", opts); err != nil {
			t.Fatalf("Resolve(constraint) error = %v", err)
		}
		if _, release, err := acquirer.Extract(t.Context(), repoURL, tagDigest, opts); err != nil {
			t.Fatalf("Extract() error = %v", err)
		} else {
			release()
		}
	}

	anonymousDir := t.TempDir()
	runAcquisition(t, Options{CacheDir: anonymousDir})

	reg.EnableBasicAuth("oci-user", "oci-pass")
	credentialedDir := t.TempDir()
	credentialed := Options{CacheDir: credentialedDir, Credentials: Credentials{Username: "oci-user", Password: "oci-pass"}}
	runAcquisition(t, credentialed)

	anonymousState := cacheDiskState(t, anonymousDir)
	credentialedState := cacheDiskState(t, credentialedDir)
	if len(anonymousState) != len(credentialedState) {
		t.Fatalf("cache trees differ: anonymous %v, credentialed %v", anonymousState, credentialedState)
	}
	for path, content := range anonymousState {
		got, ok := credentialedState[path]
		if !ok {
			t.Fatalf("credentialed cache missing %q (keys differ)", path)
		}
		if got != content {
			t.Fatalf("cache file %q differs:\nanonymous:    %q\ncredentialed: %q", path, content, got)
		}
	}

	// Different creds against the SAME cache reuse the entry: the extract is
	// an image-cache hit and no new entries appear.
	before := cacheDiskState(t, credentialedDir)
	fromCache := false
	// DIFFERENT credential values against the same cache: the cached extract
	// must not fetch (creds are not part of the entry key), so the wrong
	// password never even reaches the registry.
	other := Options{
		CacheDir:    credentialedDir,
		Credentials: Credentials{Username: "other-user", Password: "other-pass"},
		OnAcquired:  func(fromImageCache bool) { fromCache = fromImageCache },
	}
	if _, release, err := acquirer.Extract(t.Context(), repoURL, tagDigest, other); err != nil {
		t.Fatalf("re-Extract() error = %v", err)
	} else {
		release()
	}
	if !fromCache {
		t.Fatal("differently-credentialed Extract() did not reuse the cached entry")
	}
	after := cacheDiskState(t, credentialedDir)
	if len(after) != len(before) {
		t.Fatalf("re-Extract changed the entry set: before %d files, after %d", len(before), len(after))
	}
}

// cacheDiskState maps relative path -> comparable content: records and
// metadata are canonicalized (metadata timestamps zeroed — WriteMetadata
// refreshes them per run); the image tar is tracked by presence only (its
// bytes embed fetch-time tar header timestamps).
func cacheDiskState(t *testing.T, cacheDir string) map[string]string {
	t.Helper()
	state := map[string]string{}
	err := filepath.Walk(cacheDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		rel, err := filepath.Rel(cacheDir, path)
		if err != nil {
			return err
		}
		if filepath.Base(path) == imageTarName {
			state[rel] = "<image-tar>"
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if filepath.Base(path) == "metadata.json" {
			var decoded map[string]any
			if err := json.Unmarshal(data, &decoded); err != nil {
				return err
			}
			delete(decoded, "createdAt")
			delete(decoded, "updatedAt")
			canonical, err := json.Marshal(decoded)
			if err != nil {
				return err
			}
			state[rel] = string(canonical)
			return nil
		}
		state[rel] = string(data)
		return nil
	})
	if err != nil {
		t.Fatalf("walk cache dir %s: %v", cacheDir, err)
	}
	return state
}

// HTTPClient is the OCI *Helm chart* seam: it reports configured = false when
// no TLS-implying flag is set, so the Helm OCI puller keeps helm's default
// client (and no `nil, nil` return sneaks past nilnil).
func TestHTTPClientUnconfiguredWithoutTLSFlags(t *testing.T) {
	for _, tt := range []struct {
		name        string
		credentials Credentials
	}{
		{name: "zero value", credentials: Credentials{}},
		{name: "basic auth only", credentials: Credentials{Username: "oci-user", Password: "oci-pass"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client, configured, err := tt.credentials.HTTPClient()
			if err != nil {
				t.Fatalf("HTTPClient() error = %v", err)
			}
			if configured {
				t.Fatal("HTTPClient() configured = true, want false without TLS flags")
			}
			if client != nil {
				t.Fatalf("HTTPClient() client = %#v, want nil", client)
			}
		})
	}
}

// --oci-ca-file makes the chart client trust a registry the system pool does
// not, which is what lets the parity smoke's self-signed registry serve OCI
// Helm charts.
func TestHTTPClientTrustsCAFile(t *testing.T) {
	reg := ocitest.StartTLSRegistry(t)
	client, configured, err := Credentials{CAFile: reg.CAFilePath(t)}.HTTPClient()
	if err != nil {
		t.Fatalf("HTTPClient() error = %v", err)
	}
	if !configured {
		t.Fatal("HTTPClient() configured = false, want true with --oci-ca-file")
	}
	response, err := client.Get("https://" + reg.Host + "/v2/")
	if err != nil {
		t.Fatalf("configured client GET error = %v", err)
	}
	_ = response.Body.Close()

	systemPoolResponse, err := http.DefaultClient.Get("https://" + reg.Host + "/v2/")
	if err == nil {
		_ = systemPoolResponse.Body.Close()
		t.Fatal("system-pool client reached the self-signed registry, fixture is not proving anything")
	}
	if !strings.Contains(err.Error(), "x509") {
		t.Fatalf("system-pool client error = %v, want x509 verification failure", err)
	}
}

// --oci-insecure-skip-verify is the one flag in the family whose entire
// purpose is to change TLS verification behaviour, so it needs its own
// end-to-end pin: the field must reach the chart client's tls.Config, not
// merely flip hasTLSConfig(). The control leg proves the registry really is
// untrusted by the system pool, so a dropped InsecureSkipVerify assignment
// cannot pass by accident.
func TestHTTPClientSkipsVerificationWithInsecureFlag(t *testing.T) {
	reg := ocitest.StartTLSRegistry(t)
	client, configured, err := Credentials{InsecureSkipVerify: true}.HTTPClient()
	if err != nil {
		t.Fatalf("HTTPClient() error = %v", err)
	}
	if !configured {
		t.Fatal("HTTPClient() configured = false, want true with --oci-insecure-skip-verify")
	}
	response, err := client.Get("https://" + reg.Host + "/v2/")
	if err != nil {
		t.Fatalf("insecure client GET error = %v", err)
	}
	_ = response.Body.Close()

	systemPoolResponse, err := http.DefaultClient.Get("https://" + reg.Host + "/v2/")
	if err == nil {
		_ = systemPoolResponse.Body.Close()
		t.Fatal("system-pool client reached the self-signed registry, fixture is not proving anything")
	}
	if !strings.Contains(err.Error(), "x509") {
		t.Fatalf("system-pool client error = %v, want x509 verification failure", err)
	}
}

// A bad --oci-ca-file fails closed with the flag-naming error rather than
// silently narrowing the pool.
func TestHTTPClientRejectsUnreadableAndNonPEMCAFile(t *testing.T) {
	bogus := filepath.Join(t.TempDir(), "not-a-pem.txt")
	if err := os.WriteFile(bogus, []byte("not pem\n"), 0o600); err != nil {
		t.Fatalf("write bogus CA file: %v", err)
	}
	for _, tt := range []struct {
		name string
		file string
		want string
	}{
		{name: "missing", file: filepath.Join(t.TempDir(), "absent.pem"), want: "--oci-ca-file"},
		{name: "non-PEM", file: bogus, want: "contains no PEM certificates"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client, configured, err := Credentials{CAFile: tt.file}.HTTPClient()
			if err == nil {
				t.Fatal("HTTPClient() error = nil, want a flag-naming error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("HTTPClient() error = %q, want it to contain %q", err, tt.want)
			}
			if client != nil || configured {
				t.Fatalf("HTTPClient() = (%#v, %v), want (nil, false) on error", client, configured)
			}
		})
	}
}

// The seam test above only proves rootCAPool augments whatever base it is
// given. This pins the other half of the rule: the client HTTPClient returns
// carries a pool that is strictly wider than the --oci-ca-file bundle, i.e.
// the system roots survived. Dropping the x509.SystemCertPool() base in
// HTTPClient turns this red on both macOS (where the system pool exposes no
// Subjects) and Linux, because CertPool.Equal compares the system-pool flag
// as well as the certificates.
func TestHTTPClientCAFileAugmentsSystemPool(t *testing.T) {
	reg := ocitest.StartTLSRegistry(t)
	caPEM, err := os.ReadFile(reg.CAFilePath(t))
	if err != nil {
		t.Fatalf("read CA file: %v", err)
	}
	client, configured, err := Credentials{CAFile: reg.CAFilePath(t)}.HTTPClient()
	if err != nil {
		t.Fatalf("HTTPClient() error = %v", err)
	}
	if !configured {
		t.Fatal("HTTPClient() configured = false, want true with --oci-ca-file")
	}
	transport, ok := client.Transport.(*retry.Transport)
	if !ok {
		t.Fatalf("client.Transport = %T, want *retry.Transport", client.Transport)
	}
	base, ok := transport.Base.(*http.Transport)
	if !ok {
		t.Fatalf("transport.Base = %T, want *http.Transport", transport.Base)
	}
	caOnly := x509.NewCertPool()
	if !caOnly.AppendCertsFromPEM(caPEM) {
		t.Fatal("CA file did not parse")
	}
	if base.TLSClientConfig.RootCAs.Equal(caOnly) {
		t.Fatal("--oci-ca-file replaced the system pool instead of adding to it")
	}
}

// The mutual-TLS half of the flags: --oci-client-cert-file/--oci-client-key-file
// must reach the chart client's TLS config, not just Validate(). The control
// leg proves the registry really demands a client certificate, so a dropped
// Certificates assignment cannot pass by accident.
func TestHTTPClientPresentsClientCertPair(t *testing.T) {
	reg, certFile, keyFile := ocitest.StartMTLSRegistry(t)
	withPair, configured, err := Credentials{CAFile: reg.CAFilePath(t), ClientCertFile: certFile, ClientKeyFile: keyFile}.HTTPClient()
	if err != nil {
		t.Fatalf("HTTPClient() error = %v", err)
	}
	if !configured {
		t.Fatal("HTTPClient() configured = false, want true with the client certificate pair")
	}
	response, err := withPair.Get("https://" + reg.Host + "/v2/")
	if err != nil {
		t.Fatalf("client with --oci-client-cert-file GET error = %v", err)
	}
	_ = response.Body.Close()

	caOnly, _, err := Credentials{CAFile: reg.CAFilePath(t)}.HTTPClient()
	if err != nil {
		t.Fatalf("HTTPClient() without the pair error = %v", err)
	}
	if bare, err := caOnly.Get("https://" + reg.Host + "/v2/"); err == nil {
		_ = bare.Body.Close()
		t.Fatal("client without the pair reached the mutual-TLS registry, fixture is not proving anything")
	}
}

// Augment, not replace: a run that passes --oci-ca-file for a private
// artifact registry must keep pulling public charts. rootCAPool is the seam
// that makes the pool-building rule testable without the network. That
// HTTPClient actually hands it the system pool is pinned separately by
// TestHTTPClientCAFileAugmentsSystemPool.
func TestRootCAPoolAugmentsBasePool(t *testing.T) {
	serverA, pemA := selfSignedTLSServer(t)
	serverB, pemB := selfSignedTLSServer(t)

	base := x509.NewCertPool()
	if !base.AppendCertsFromPEM(pemA) {
		t.Fatal("base pool did not accept cert A")
	}
	pool, err := rootCAPool(base, pemB)
	if err != nil {
		t.Fatalf("rootCAPool() error = %v", err)
	}
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}}}
	for name, server := range map[string]*httptest.Server{"base cert A": serverA, "added cert B": serverB} {
		t.Run(name, func(t *testing.T) {
			response, err := client.Get(server.URL)
			if err != nil {
				t.Fatalf("GET %s error = %v, want the augmented pool to trust it", name, err)
			}
			_ = response.Body.Close()
		})
	}
	if _, err := rootCAPool(base, []byte("not pem\n")); err == nil {
		t.Fatal("rootCAPool() with non-PEM data error = nil")
	}
}

// selfSignedTLSServer starts an https loopback server with its own throwaway
// self-signed certificate and returns that certificate as PEM. Two calls
// produce two distinct roots, which httptest's shared built-in certificate
// cannot.
func selfSignedTLSServer(t *testing.T) (*httptest.Server, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate server key: %v", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("generate serial: %v", err)
	}
	template := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "drydock-rootca-test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.IPv6loopback},
		DNSNames:              []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create server cert: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal server key: %v", err)
	}
	pair, err := tls.X509KeyPair(certPEM, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	if err != nil {
		t.Fatalf("build server key pair: %v", err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	server.TLS = &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	t.Cleanup(server.Close)
	return server, certPEM
}
