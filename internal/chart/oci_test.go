package chart

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeOCIPuller struct {
	archive []byte
	err     error
	pulls   int
	options []Options
}

func (puller *fakeOCIPuller) Pull(ctx context.Context, request Request, opts Options) ([]byte, error) {
	puller.pulls++
	puller.options = append(puller.options, opts)
	if puller.err != nil {
		return nil, puller.err
	}
	return puller.archive, nil
}

func TestDefaultAcquirerFetchesOCIChartAndCachesIt(t *testing.T) {
	archive := chartArchive(t, "demo", map[string]string{
		"Chart.yaml":  "apiVersion: v2\nname: demo\nversion: 1.2.3\n",
		"values.yaml": "replicaCount: 1\n",
	})
	puller := &fakeOCIPuller{archive: archive}
	request := Request{
		Repository: "oci://registry.example.test/charts",
		Name:       "demo",
		Version:    "1.2.3",
		Kind:       RepositoryOCI,
	}
	opts := Options{CacheDir: t.TempDir()}
	acquirer := DefaultAcquirer{OCIPuller: puller}

	first, err := acquirer.Acquire(context.Background(), request, opts)
	if err != nil {
		t.Fatalf("first Acquire() error = %v", err)
	}
	if first.FromCache {
		t.Fatal("first Acquire() FromCache = true, want false")
	}
	if got, want := first.ChartDir, filepath.Join(opts.CacheDir, string(request.Kind), mustCacheKey(t, request), request.Name); got != want {
		t.Fatalf("first ChartDir = %q, want %q", got, want)
	}
	if _, err := os.Stat(filepath.Join(first.ChartDir, "Chart.yaml")); err != nil {
		t.Fatalf("stat extracted Chart.yaml: %v", err)
	}

	second, err := acquirer.Acquire(context.Background(), request, opts)
	if err != nil {
		t.Fatalf("second Acquire() error = %v", err)
	}
	if !second.FromCache {
		t.Fatal("second Acquire() FromCache = false, want true")
	}
	if second.ChartDir != first.ChartDir {
		t.Fatalf("second ChartDir = %q, want %q", second.ChartDir, first.ChartDir)
	}
	if puller.pulls != 1 {
		t.Fatalf("pull count = %d, want 1", puller.pulls)
	}
}

func TestDefaultAcquirerOCIOfflineRequiresCacheHit(t *testing.T) {
	puller := &fakeOCIPuller{archive: chartArchive(t, "demo", map[string]string{
		"Chart.yaml": "apiVersion: v2\nname: demo\nversion: 1.2.3\n",
	})}
	acquirer := DefaultAcquirer{OCIPuller: puller}
	_, err := acquirer.Acquire(context.Background(), Request{
		Repository: "oci://registry.example.test/charts",
		Name:       "demo",
		Version:    "1.2.3",
		Kind:       RepositoryOCI,
	}, Options{CacheDir: t.TempDir(), Offline: true})
	if err == nil {
		t.Fatal("Acquire() error = nil, want offline cache miss")
	}
	if !strings.Contains(err.Error(), "offline cache miss") {
		t.Fatalf("Acquire() error = %q, want offline cache miss", err)
	}
	if puller.pulls != 0 {
		t.Fatalf("pull count = %d, want 0", puller.pulls)
	}
}

func TestDefaultAcquirerPassesRegistryConfigToOCIPuller(t *testing.T) {
	registryConfig := filepath.Join(t.TempDir(), "registry.json")
	if err := os.WriteFile(registryConfig, []byte(`{"auths":{}}`), 0o600); err != nil {
		t.Fatalf("write registry config: %v", err)
	}
	puller := &fakeOCIPuller{archive: chartArchive(t, "demo", map[string]string{
		"Chart.yaml": "apiVersion: v2\nname: demo\nversion: 1.2.3\n",
	})}

	_, err := (DefaultAcquirer{OCIPuller: puller}).Acquire(context.Background(), Request{
		Repository: "oci://registry.example.test/charts",
		Name:       "demo",
		Version:    "1.2.3",
		Kind:       RepositoryOCI,
	}, Options{
		CacheDir: t.TempDir(),
		Credentials: ChartCredentials{
			RegistryConfig: registryConfig,
		},
	})
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	if len(puller.options) != 1 {
		t.Fatalf("puller options = %d, want 1", len(puller.options))
	}
	if got := puller.options[0].Credentials.RegistryConfig; got != registryConfig {
		t.Fatalf("RegistryConfig = %q, want %q", got, registryConfig)
	}
}

func TestDefaultAcquirerRejectsMissingRegistryConfigBeforeNetwork(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing-registry.json")
	puller := &fakeOCIPuller{archive: chartArchive(t, "demo", map[string]string{
		"Chart.yaml": "apiVersion: v2\nname: demo\nversion: 1.2.3\n",
	})}

	_, err := (DefaultAcquirer{OCIPuller: puller}).Acquire(context.Background(), Request{
		Repository: "oci://registry.example.test/charts",
		Name:       "demo",
		Version:    "1.2.3",
		Kind:       RepositoryOCI,
	}, Options{
		CacheDir: t.TempDir(),
		Credentials: ChartCredentials{
			RegistryConfig: missing,
		},
	})
	if err == nil {
		t.Fatal("Acquire() error = nil, want missing registry config error")
	}
	if puller.pulls != 0 {
		t.Fatalf("pull count = %d, want 0", puller.pulls)
	}
	if !strings.Contains(err.Error(), "registry config") || !strings.Contains(err.Error(), missing) {
		t.Fatalf("Acquire() error = %q, want missing registry config path", err)
	}
}

func TestDefaultAcquirerMapsOCIAuthFailures(t *testing.T) {
	puller := &fakeOCIPuller{err: fmt.Errorf("401 unauthorized")}
	_, err := (DefaultAcquirer{OCIPuller: puller}).Acquire(context.Background(), Request{
		Repository: "oci://registry.example.test/charts",
		Name:       "demo",
		Version:    "1.2.3",
		Kind:       RepositoryOCI,
	}, Options{CacheDir: t.TempDir()})
	if err == nil {
		t.Fatal("Acquire() error = nil, want auth failure error")
	}
	if !strings.Contains(err.Error(), "authenticate OCI chart") || !strings.Contains(err.Error(), "401 unauthorized") {
		t.Fatalf("Acquire() error = %q, want OCI auth failure", err)
	}
}

func TestHelmOCIPullerDoesNotReadCallerDockerCredentials(t *testing.T) {
	const secret = "secret-password"
	callerDockerConfig := t.TempDir()
	auth := base64.StdEncoding.EncodeToString([]byte("user:" + secret))
	if err := os.WriteFile(filepath.Join(callerDockerConfig, "config.json"), []byte(fmt.Sprintf(`{
  "auths": {
    "registry.example.test": {
      "auth": %q
    }
  }
}
`, auth)), 0o600); err != nil {
		t.Fatalf("write caller Docker config: %v", err)
	}
	t.Setenv("DOCKER_CONFIG", callerDockerConfig)

	var authorizationHeaders []string
	client := &http.Client{
		Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			authorizationHeaders = append(authorizationHeaders, request.Header.Get("Authorization"))
			return &http.Response{
				StatusCode: http.StatusUnauthorized,
				Status:     "401 Unauthorized",
				Header: http.Header{
					"WWW-Authenticate": []string{`Basic realm="registry"`},
				},
				Body:    io.NopCloser(strings.NewReader("authentication required")),
				Request: request,
			}, nil
		}),
	}

	_, err := (DefaultAcquirer{Client: client}).Acquire(context.Background(), Request{
		Repository: "oci://registry.example.test/charts",
		Name:       "demo",
		Version:    "1.2.3",
		Kind:       RepositoryOCI,
	}, Options{CacheDir: t.TempDir()})
	if err == nil {
		t.Fatal("Acquire() error = nil, want unauthorized error")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), auth) {
		t.Fatalf("Acquire() error leaked caller Docker credentials: %q", err)
	}
	for _, header := range authorizationHeaders {
		if strings.Contains(header, secret) || strings.Contains(header, auth) || strings.HasPrefix(header, "Basic ") {
			t.Fatalf("request Authorization header used caller Docker credentials: %q", header)
		}
	}
}

func TestHelmOCIPullerRejectsUnsafeRepositoriesBeforeNetwork(t *testing.T) {
	for _, tt := range []struct {
		name       string
		repository string
		want       string
		notWant    []string
	}{
		{
			name:       "userinfo",
			repository: "oci://user:password@registry.example.test/charts",
			want:       "must not include credentials",
			notWant:    []string{"user", "password"},
		},
		{
			name:       "query",
			repository: "oci://registry.example.test/charts?token=secret",
			want:       "must not include query",
			notWant:    []string{"token=secret"},
		},
		{
			name:       "fragment",
			repository: "oci://registry.example.test/charts#secret",
			want:       "must not include fragment",
			notWant:    []string{"secret"},
		},
		{
			name:       "empty path segment",
			repository: "oci://registry.example.test/foo//bar",
			want:       "empty path segment",
		},
		{
			name:       "dot path segment",
			repository: "oci://registry.example.test/foo/./bar",
			want:       "unsafe path segment",
		},
		{
			name:       "parent path segment",
			repository: "oci://registry.example.test/foo/../bar",
			want:       "unsafe path segment",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := &http.Client{
				Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					t.Fatal("invalid OCI repository made a network request")
					return nil, errors.New("unexpected network request")
				}),
			}
			_, err := (DefaultAcquirer{Client: client}).Acquire(context.Background(), Request{
				Repository: tt.repository,
				Name:       "demo",
				Version:    "1.2.3",
				Kind:       RepositoryOCI,
			}, Options{CacheDir: t.TempDir()})
			if err == nil {
				t.Fatal("Acquire() error = nil, want repository validation error")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Acquire() error = %q, want %q", err, tt.want)
			}
			for _, notWant := range tt.notWant {
				if strings.Contains(err.Error(), notWant) {
					t.Fatalf("Acquire() error leaked %q: %q", notWant, err)
				}
			}
		})
	}
}

func TestDefaultAcquirerRejectsUnsafeOCIRepositoriesBeforeCacheHit(t *testing.T) {
	for _, tt := range []struct {
		name       string
		repository string
		want       string
		notWant    []string
	}{
		{
			name:       "userinfo",
			repository: "oci://user:password@registry.example.test/charts",
			want:       "must not include credentials",
			notWant:    []string{"user", "password"},
		},
		{
			name:       "query",
			repository: "oci://registry.example.test/charts?token=secret",
			want:       "must not include query",
			notWant:    []string{"token=secret"},
		},
		{
			name:       "fragment",
			repository: "oci://registry.example.test/charts#secret",
			want:       "must not include fragment",
			notWant:    []string{"secret"},
		},
		{
			name:       "empty path segment",
			repository: "oci://registry.example.test/foo//bar",
			want:       "empty path segment",
		},
		{
			name:       "dot path segment",
			repository: "oci://registry.example.test/foo/./bar",
			want:       "unsafe path segment",
		},
		{
			name:       "parent path segment",
			repository: "oci://registry.example.test/foo/../bar",
			want:       "unsafe path segment",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			request := Request{
				Repository: tt.repository,
				Name:       "demo",
				Version:    "1.2.3",
				Kind:       RepositoryOCI,
			}
			cacheDir := t.TempDir()
			chartDir := filepath.Join(cacheDir, string(request.Kind), mustCacheKey(t, request), request.Name)
			if err := os.MkdirAll(chartDir, 0o755); err != nil {
				t.Fatalf("create cached chart dir: %v", err)
			}
			if err := os.WriteFile(filepath.Join(chartDir, "Chart.yaml"), []byte("apiVersion: v2\nname: demo\nversion: 1.2.3\n"), 0o600); err != nil {
				t.Fatalf("write cached Chart.yaml: %v", err)
			}

			result, err := (DefaultAcquirer{}).Acquire(context.Background(), request, Options{CacheDir: cacheDir})
			if err == nil {
				t.Fatalf("Acquire() error = nil and FromCache = %v, want repository validation error", result.FromCache)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Acquire() error = %q, want %q", err, tt.want)
			}
			for _, notWant := range tt.notWant {
				if strings.Contains(err.Error(), notWant) {
					t.Fatalf("Acquire() error leaked %q: %q", notWant, err)
				}
			}
		})
	}
}

func TestDefaultAcquirerRedactsInvalidOCIRepositoryErrors(t *testing.T) {
	for _, tt := range []struct {
		name       string
		repository string
		secrets    []string
	}{
		{
			name:       "missing host with userinfo",
			repository: "oci://user:password@",
			secrets:    []string{"user", "password"},
		},
		{
			name:       "wrong scheme with userinfo",
			repository: "https://user:password@registry.example.test/charts",
			secrets:    []string{"user", "password"},
		},
		{
			name:       "wrong scheme with token query",
			repository: "https://registry.example.test/charts?token=secret-token",
			secrets:    []string{"secret-token", "token=secret-token"},
		},
		{
			name:       "parse error with userinfo",
			repository: "oci://user:password@%zz",
			secrets:    []string{"user", "password"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := (DefaultAcquirer{}).Acquire(context.Background(), Request{
				Repository: tt.repository,
				Name:       "demo",
				Version:    "1.2.3",
				Kind:       RepositoryOCI,
			}, Options{CacheDir: t.TempDir()})
			if err == nil {
				t.Fatal("Acquire() error = nil, want repository validation error")
			}
			for _, secret := range tt.secrets {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("Acquire() error leaked %q: %q", secret, err)
				}
			}
		})
	}
}

func TestHelmOCIPullerRestoresDockerConfigAfterFailedPull(t *testing.T) {
	originalDockerConfig := t.TempDir()
	t.Setenv("DOCKER_CONFIG", originalDockerConfig)
	client := &http.Client{
		Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusUnauthorized,
				Status:     "401 Unauthorized",
				Header: http.Header{
					"WWW-Authenticate": []string{`Basic realm="registry"`},
				},
				Body:    io.NopCloser(strings.NewReader("authentication required")),
				Request: request,
			}, nil
		}),
	}

	_, err := (DefaultAcquirer{Client: client}).Acquire(context.Background(), Request{
		Repository: "oci://registry.example.test/charts",
		Name:       "demo",
		Version:    "1.2.3",
		Kind:       RepositoryOCI,
	}, Options{CacheDir: t.TempDir()})
	if err == nil {
		t.Fatal("Acquire() error = nil, want unauthorized error")
	}
	if got := os.Getenv("DOCKER_CONFIG"); got != originalDockerConfig {
		t.Fatalf("DOCKER_CONFIG = %q, want restored value %q", got, originalDockerConfig)
	}
}
