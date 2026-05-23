package chart

import (
	"context"
	"encoding/base64"
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
}

func (puller *fakeOCIPuller) Pull(ctx context.Context, request Request) ([]byte, error) {
	puller.pulls++
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

func TestDefaultAcquirerMapsOCIAuthFailures(t *testing.T) {
	puller := &fakeOCIPuller{err: fmt.Errorf("401 unauthorized")}
	_, err := (DefaultAcquirer{OCIPuller: puller}).Acquire(context.Background(), Request{
		Repository: "oci://user:pass@registry.example.test/charts",
		Name:       "demo",
		Version:    "1.2.3",
		Kind:       RepositoryOCI,
	}, Options{CacheDir: t.TempDir()})
	if err == nil {
		t.Fatal("Acquire() error = nil, want auth unsupported error")
	}
	if !strings.Contains(err.Error(), "authenticated chart repositories are not supported yet") {
		t.Fatalf("Acquire() error = %q, want auth unsupported error", err)
	}
	if strings.Contains(err.Error(), "user:") || strings.Contains(err.Error(), "pass") {
		t.Fatalf("Acquire() error leaked repository credentials: %q", err)
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
			want:       "authenticated chart repositories are not supported yet",
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
			name:       "dot dot path segment",
			repository: "oci://registry.example.test/foo/../bar",
			want:       "unsafe path segment",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := &http.Client{
				Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					t.Fatal("invalid OCI repository made a network request")
					return nil, nil
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
