package cache

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	metadataSchemaVersion = 1
	metadataDirName       = ".drydock-cache"
	metadataFileName      = "metadata.json"
)

type Metadata struct {
	SchemaVersion int       `json:"schemaVersion" yaml:"schemaVersion"`
	Source        Source    `json:"source" yaml:"source"`
	Kind          string    `json:"kind" yaml:"kind"`
	Key           string    `json:"key" yaml:"key"`
	Target        string    `json:"target,omitempty" yaml:"target,omitempty"`
	Name          string    `json:"name,omitempty" yaml:"name,omitempty"`
	Version       string    `json:"version,omitempty" yaml:"version,omitempty"`
	Revision      string    `json:"revision,omitempty" yaml:"revision,omitempty"`
	Path          string    `json:"path,omitempty" yaml:"path,omitempty"`
	CreatedAt     time.Time `json:"createdAt" yaml:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt" yaml:"updatedAt"`
}

func MetadataPath(entryPath string) string {
	return filepath.Join(entryPath, metadataDirName, metadataFileName)
}

func ReadMetadata(entryPath string, expected Source, expectedKind, expectedKey string) (*Metadata, error) {
	path := MetadataPath(entryPath)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			//nolint:nilnil // Missing metadata identifies a legacy cache entry.
			return nil, nil
		}
		return nil, err
	}
	var metadata Metadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return nil, err
	}
	if metadata.SchemaVersion != metadataSchemaVersion {
		return nil, fmt.Errorf("unsupported cache metadata schema version %d", metadata.SchemaVersion)
	}
	if metadata.Source != expected {
		return nil, fmt.Errorf("cache metadata source %q does not match %q", metadata.Source, expected)
	}
	if expectedKind != "" && metadata.Kind != expectedKind {
		return nil, fmt.Errorf("cache metadata kind %q does not match %q", metadata.Kind, expectedKind)
	}
	if metadata.Key != expectedKey {
		return nil, fmt.Errorf("cache metadata key %q does not match %q", metadata.Key, expectedKey)
	}
	return &metadata, nil
}

func WriteMetadata(entryPath string, metadata Metadata) error {
	now := time.Now().UTC()
	if metadata.CreatedAt.IsZero() {
		existing, err := ReadMetadata(entryPath, metadata.Source, metadata.Kind, metadata.Key)
		if err == nil && existing != nil && !existing.CreatedAt.IsZero() {
			metadata.CreatedAt = existing.CreatedAt
		}
	}
	if metadata.SchemaVersion == 0 {
		metadata.SchemaVersion = metadataSchemaVersion
	}
	if metadata.CreatedAt.IsZero() {
		metadata.CreatedAt = now
	}
	if metadata.UpdatedAt.IsZero() {
		metadata.UpdatedAt = now
	}
	metadata.Target = RedactedTarget(metadata.Target)
	metadataPath := MetadataPath(entryPath)
	if err := os.MkdirAll(filepath.Dir(metadataPath), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writeFileAtomic(metadataPath, data, 0o600)
}

func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	parent := filepath.Dir(path)
	tmp, err := os.CreateTemp(parent, "."+filepath.Base(path)+".tmp-")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

func RedactedTarget(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if after, ok := strings.CutPrefix(raw, "git::"); ok {
		return "git::" + RedactedTarget(after)
	}
	if isSCPStyleTarget(raw) {
		if before, _, ok := strings.Cut(raw, "#"); ok {
			raw = before
		}
		if before, _, ok := strings.Cut(raw, "?"); ok {
			raw = before
		}
		userHost, repoPath, ok := strings.Cut(raw, ":")
		if !ok {
			return raw
		}
		if _, host, ok := strings.Cut(userHost, "@"); ok && host != "" {
			return host + ":" + repoPath
		}
		return raw
	}
	if parsed, err := url.Parse(raw); err == nil && parsed.Scheme != "" {
		parsed.User = nil
		parsed.RawQuery = ""
		parsed.ForceQuery = false
		parsed.Fragment = ""
		return parsed.String()
	}
	if before, _, ok := strings.Cut(raw, "#"); ok {
		raw = before
	}
	if before, _, ok := strings.Cut(raw, "?"); ok {
		raw = before
	}
	return raw
}

func isSCPStyleTarget(raw string) bool {
	if strings.Contains(raw, "://") || strings.HasPrefix(raw, "/") {
		return false
	}
	colon := strings.Index(raw, ":")
	if colon <= 0 {
		return false
	}
	if slash := strings.IndexAny(raw, `/\`); slash >= 0 && slash < colon {
		return false
	}
	return strings.Contains(raw[:colon], "@")
}
