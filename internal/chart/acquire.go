package chart

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

type RepositoryKind string

const (
	RepositoryHTTP RepositoryKind = "http"
	RepositoryOCI  RepositoryKind = "oci"
)

type Request struct {
	Repository string
	Name       string
	Version    string
	Kind       RepositoryKind
}

type Result struct {
	ChartDir   string
	Repository string
	Name       string
	Version    string
	Kind       RepositoryKind
	FromCache  bool
}

type Options struct {
	CacheDir string
	Offline  bool
	Refresh  bool
}

type Acquirer interface {
	Acquire(ctx context.Context, request Request, opts Options) (Result, error)
}

func DefaultCacheDir() (string, error) {
	root, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "argocd-local", "charts"), nil
}

func NewCacheKey(request Request) (string, error) {
	normalized, err := NormalizeRepository(request.Repository, request.Kind)
	if err != nil {
		return "", err
	}
	name := strings.TrimSpace(request.Name)
	version := strings.TrimSpace(request.Version)
	if name == "" {
		return "", fmt.Errorf("chart name is required")
	}
	if version == "" {
		return "", fmt.Errorf("chart version is required")
	}
	if request.Kind != RepositoryHTTP && request.Kind != RepositoryOCI {
		return "", fmt.Errorf("unsupported chart repository kind %q", request.Kind)
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{
		string(request.Kind),
		normalized,
		name,
		version,
	}, "\x00")))
	return hex.EncodeToString(sum[:]), nil
}

func NormalizeRepository(repository string, kind RepositoryKind) (string, error) {
	repository = strings.TrimSpace(repository)
	if repository == "" {
		return "", fmt.Errorf("chart repository is required")
	}
	if kind == RepositoryOCI {
		repository = strings.TrimSuffix(repository, "/")
		if !strings.HasPrefix(repository, "oci://") {
			return "", fmt.Errorf("OCI chart repository %q must start with oci://", repository)
		}
		return repository, nil
	}
	parsed, err := url.Parse(repository)
	if err != nil {
		return "", fmt.Errorf("parse chart repository %q: %w", repository, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", fmt.Errorf("HTTP chart repository %q must use http or https", repository)
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	return parsed.String(), nil
}
