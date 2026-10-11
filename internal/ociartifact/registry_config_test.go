package ociartifact

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sholdee/drydock/internal/ociartifact/ocitest"
)

// writeRegistryConfig writes a Docker-format registry config file (the shape
// `helm registry login` and `docker login` produce) and returns its path.
func writeRegistryConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write registry config: %v", err)
	}
	return path
}

// basicAuthsEntry renders a config file with one plain `auths` entry keyed by
// key, carrying base64(username:password) in the `auth` field.
func basicAuthsEntry(key, username, password string) string {
	return `{"auths":{"` + key + `":{"auth":"` + base64.StdEncoding.EncodeToString([]byte(username+":"+password)) + `"}}}`
}

// resolveExtractChart runs the credentialed happy path end-to-end through the
// REAL acquirer: Resolve must hit the pushed digest and Extract must land
// Chart.yaml at the extraction root.
func resolveExtractChart(t *testing.T, repoURL, revision, wantDigest string, opts Options) {
	t.Helper()
	acquirer := DefaultAcquirer{}
	digest, err := acquirer.Resolve(t.Context(), repoURL, revision, opts)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if digest != wantDigest {
		t.Fatalf("Resolve() = %q, want %q", digest, wantDigest)
	}
	dir, release, err := acquirer.Extract(t.Context(), repoURL, digest, opts)
	if err != nil {
		t.Fatalf("Extract() error = %v", err)
	}
	defer release()
	if _, err := os.Stat(filepath.Join(dir, "Chart.yaml")); err != nil {
		t.Fatalf("Chart.yaml not at extraction root: %v", err)
	}
}

// assertResolveAnonymous pins that no credential reached the wire. The
// correct pair is in the fixture, so a pull that used it would succeed; the
// anonymous attempt instead meets the Basic challenge with the empty
// StaticCredential the vendored client always installs
// (util/oci/client.go:160-166), and ORAS answers ErrBasicCredentialNotFound
// ("basic credential not found", auth/client.go:387) before any retry. A
// surfaced 401 is accepted too, in case a future client passes the response
// through instead.
func assertResolveAnonymous(t *testing.T, repoURL string, opts Options) {
	t.Helper()
	_, err := (DefaultAcquirer{}).Resolve(t.Context(), repoURL, "1.2.3", opts)
	if err == nil {
		t.Fatal("Resolve() error = nil, want the anonymous pull to be refused")
	}
	if !strings.Contains(err.Error(), "basic credential not found") && !strings.Contains(err.Error(), "401") {
		t.Fatalf("Resolve() error = %q, want the anonymous Basic-challenge failure", err)
	}
	if strings.Contains(err.Error(), "oci-pass") {
		t.Fatalf("Resolve() error = %q, carries the file credential that must not have been used", err)
	}
}

// startBasicAuthChartRegistry pushes a chart, then turns Basic auth on so
// every subsequent request needs oci-user/oci-pass.
func startBasicAuthChartRegistry(t *testing.T) (reg *ocitest.Registry, repoURL, pushedDigest string) {
	t.Helper()
	reg = ocitest.StartRegistry(t)
	pushedDigest = ocitest.PushHelmChartArtifact(t, reg, "charts/auth", "1.2.3", ocitest.HelmChartSpec{Name: "auth", Version: "1.2.3"})
	reg.EnableBasicAuth("oci-user", "oci-pass")
	return reg, reg.RepoURL("charts/auth"), pushedDigest
}

// TestResolveExtractWithRegistryConfig pins the per-host happy path: the only
// credential lives in --registry-config under the registry's host key, and
// the resolved struct carries it with RegistryConfig cleared (so the second
// ForRepository call inside newClient is a no-op).
func TestResolveExtractWithRegistryConfig(t *testing.T) {
	reg, repoURL, pushedDigest := startBasicAuthChartRegistry(t)
	config := writeRegistryConfig(t, basicAuthsEntry(reg.Host, "oci-user", "oci-pass"))
	credentials := Credentials{RegistryConfig: config}

	resolved, err := credentials.ForRepository(t.Context(), repoURL)
	if err != nil {
		t.Fatalf("ForRepository() error = %v", err)
	}
	if resolved.Username != "oci-user" || resolved.Password != "oci-pass" || resolved.RegistryConfig != "" {
		t.Fatalf("ForRepository() = %#v, want file credentials with RegistryConfig cleared", resolved)
	}
	resolveExtractChart(t, repoURL, "1.2.3", pushedDigest, Options{CacheDir: t.TempDir(), Credentials: credentials})
}

// TestRegistryConfigExplicitFlagsOverride pins the global override: an
// explicit --oci-username/--oci-password pair wins over the file for every
// host, and the file is never opened when the pair is set.
func TestRegistryConfigExplicitFlagsOverride(t *testing.T) {
	reg, repoURL, pushedDigest := startBasicAuthChartRegistry(t)
	explicit := Credentials{Username: "oci-user", Password: "oci-pass"}

	t.Run("flags win over a wrong file entry", func(t *testing.T) {
		credentials := explicit
		credentials.RegistryConfig = writeRegistryConfig(t, basicAuthsEntry(reg.Host, "oci-user", "wrong-pass"))
		resolveExtractChart(t, repoURL, "1.2.3", pushedDigest, Options{CacheDir: t.TempDir(), Credentials: credentials})
	})

	t.Run("flags never open the file", func(t *testing.T) {
		credentials := explicit
		credentials.RegistryConfig = filepath.Join(t.TempDir(), "missing.json")
		resolveExtractChart(t, repoURL, "1.2.3", pushedDigest, Options{CacheDir: t.TempDir(), Credentials: credentials})
	})
}

// A file that names other registries only leaves this host anonymous: the
// lookup is per host, never "any credential in the file".
func TestRegistryConfigUnknownHostStaysAnonymous(t *testing.T) {
	_, repoURL, _ := startBasicAuthChartRegistry(t)
	config := writeRegistryConfig(t, basicAuthsEntry("other.example.test", "oci-user", "oci-pass"))
	assertResolveAnonymous(t, repoURL, Options{CacheDir: t.TempDir(), Credentials: Credentials{RegistryConfig: config}})
}

// Legacy config files key entries as https://host/; oras's store resolves
// them to the bare host (credentials/internal/config/config.go:170-186).
func TestRegistryConfigLegacyHTTPSKey(t *testing.T) {
	reg, repoURL, pushedDigest := startBasicAuthChartRegistry(t)
	config := writeRegistryConfig(t, basicAuthsEntry("https://"+reg.Host+"/", "oci-user", "oci-pass"))
	resolveExtractChart(t, repoURL, "1.2.3", pushedDigest, Options{CacheDir: t.TempDir(), Credentials: Credentials{RegistryConfig: config}})
}

// An auth-less file stays anonymous on every platform: DetectDefaultNativeStore
// is false, so the OS keychain (osxkeychain/wincred/pass) is never consulted
// for artifact sources — helm's chart loader would consult it.
func TestRegistryConfigAuthlessFileStaysAnonymous(t *testing.T) {
	_, repoURL, _ := startBasicAuthChartRegistry(t)
	config := writeRegistryConfig(t, `{}`)
	assertResolveAnonymous(t, repoURL, Options{CacheDir: t.TempDir(), Credentials: Credentials{RegistryConfig: config}})
}

// An identity-token entry is an error naming the host and the remedy, never
// a silent anonymous pull: the vendored client sends username/password only.
func TestRegistryConfigIdentityTokenRejected(t *testing.T) {
	reg := ocitest.StartRegistry(t)
	config := writeRegistryConfig(t, `{"auths":{"`+reg.Host+`":{"identitytoken":"tok-sekrit-123"}}}`)

	_, err := (DefaultAcquirer{}).Resolve(t.Context(), reg.RepoURL("charts/auth"), "1.2.3", Options{CacheDir: t.TempDir(), Credentials: Credentials{RegistryConfig: config}})
	if err == nil {
		t.Fatal("Resolve() error = nil, want identity-token rejection")
	}
	for _, want := range []string{"--registry-config", reg.Host, "identity token", "--oci-username/--oci-password"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Resolve() error = %q, want %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "tok-sekrit-123") {
		t.Fatalf("Resolve() error leaks the token: %q", err)
	}
}

// A credential helper that cannot run fails closed with the helper named:
// oras returns the exec error (credentials/internal/executer/executer.go:65-77)
// and the %w wrap keeps "docker-credential-<helper>" in the text.
func TestRegistryConfigMissingHelperFailsClosed(t *testing.T) {
	reg := ocitest.StartRegistry(t)
	config := writeRegistryConfig(t, `{"credHelpers":{"`+reg.Host+`":"drydock-test-nonexistent"}}`)

	_, err := (DefaultAcquirer{}).Resolve(t.Context(), reg.RepoURL("charts/auth"), "1.2.3", Options{CacheDir: t.TempDir(), Credentials: Credentials{RegistryConfig: config}})
	if err == nil {
		t.Fatal("Resolve() error = nil, want helper exec failure")
	}
	for _, want := range []string{"--registry-config", "docker-credential-drydock-test-nonexistent"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Resolve() error = %q, want %q", err, want)
		}
	}
}

// TestForRepositoryValidatesFile pins the lazy existence check: oras's loader
// would treat a missing file as an empty store, turning a path typo into
// anonymous pulls.
func TestForRepositoryValidatesFile(t *testing.T) {
	const repoURL = "oci://registry.example.test/org/app"

	t.Run("missing", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "missing.json")
		_, err := (Credentials{RegistryConfig: missing}).ForRepository(t.Context(), repoURL)
		if err == nil || !strings.Contains(err.Error(), "does not exist") || !strings.Contains(err.Error(), missing) {
			t.Fatalf("ForRepository() error = %v, want %q naming %q", err, "does not exist", missing)
		}
	})

	t.Run("directory", func(t *testing.T) {
		dir := t.TempDir()
		_, err := (Credentials{RegistryConfig: dir}).ForRepository(t.Context(), repoURL)
		if err == nil || !strings.Contains(err.Error(), "must be a file") || !strings.Contains(err.Error(), dir) {
			t.Fatalf("ForRepository() error = %v, want %q naming %q", err, "must be a file", dir)
		}
	})
}

// A userinfo-bearing repoURL is rejected before its host segment can become a
// lookup key or appear in error text; the secret never reaches either.
func TestForRepositoryRejectsURLUserinfo(t *testing.T) {
	config := writeRegistryConfig(t, `{}`)
	_, err := (Credentials{RegistryConfig: config}).ForRepository(t.Context(), "oci://user:sekrit@registry.example.test/org/app")
	if err == nil {
		t.Fatal("ForRepository() error = nil, want userinfo rejection")
	}
	if strings.Contains(err.Error(), "sekrit") {
		t.Fatalf("ForRepository() error leaks the URL password: %q", err)
	}
}

// TestCredentialsValidateIgnoresRegistryConfig is the lazy contract: Validate
// runs on every build and diff, so a missing file must not fail there.
func TestCredentialsValidateIgnoresRegistryConfig(t *testing.T) {
	credentials := Credentials{RegistryConfig: filepath.Join(t.TempDir(), "nonexistent.json")}
	if err := credentials.Validate(); err != nil {
		t.Fatalf("Validate() error = %v, want nil (RegistryConfig is validated by ForRepository)", err)
	}
	if _, configured, err := credentials.HTTPClient(); err != nil || configured {
		t.Fatalf("HTTPClient() = (configured=%v, err=%v), want (false, nil)", configured, err)
	}
}

// registryConfigKey mirrors the Docker Hub alias folding oras applies on the
// wire (registry.Reference.Host) and in its store keys.
func TestRegistryConfigKeyFoldsDockerHubAlias(t *testing.T) {
	cases := []struct {
		repoURL    string
		wantHost   string
		wantServer string
	}{
		{"oci://docker.io/library/app", "registry-1.docker.io", "https://index.docker.io/v1/"},
		{"oci://registry-1.docker.io/library/app", "registry-1.docker.io", "https://index.docker.io/v1/"},
		{"oci://ghcr.io/org/app", "ghcr.io", "ghcr.io"},
		{"oci://127.0.0.1:5000/org/app", "127.0.0.1:5000", "127.0.0.1:5000"},
		{" OCI://registry.example.test/org/app/ ", "registry.example.test", "registry.example.test"},
	}
	for _, tc := range cases {
		host, server := registryConfigKey(tc.repoURL)
		if host != tc.wantHost || server != tc.wantServer {
			t.Errorf("registryConfigKey(%q) = (%q, %q), want (%q, %q)", tc.repoURL, host, server, tc.wantHost, tc.wantServer)
		}
	}
}
