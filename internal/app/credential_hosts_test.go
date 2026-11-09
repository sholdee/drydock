package app

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sholdee/drydock/internal/remote"
	sourcepkg "github.com/sholdee/drydock/internal/source"
)

// TestBuildEntryPointsCheckCredentialHostsFirst checks that Build,
// BuildApp, and BuildSelection check the credential hosts before any other
// work. The request also has a render cache directory inside the
// repository, which the render cache check refuses. That check runs before
// ListApplications, so an entry point that left out its own early
// credential check fails with the render cache error instead.
func TestBuildEntryPointsCheckCredentialHostsFirst(t *testing.T) {
	root := t.TempDir()
	credentials := map[string]AcquisitionOptions{
		"git": {GitCredentials: sourcepkg.GitCredentials{BearerToken: "early-secret-token"}},
		"remote git": {
			RemoteResourceGitCredentials: remote.GitCredentials{BearerToken: "early-secret-token"},
		},
		"remote": {RemoteResourceCredentials: remote.Credentials{BearerToken: "early-secret-token"}},
	}
	for credentialName, acquisition := range credentials {
		request := BuildRequest{
			Path:               root,
			AcquisitionOptions: acquisition,
			RenderCacheEnabled: true,
			RenderCacheDir:     filepath.Join(root, "render-cache"),
		}
		entryPoints := map[string]func() error{
			"Build": func() error {
				_, err := Orchestrator{}.Build(context.Background(), request)
				return err
			},
			"BuildApp": func() error {
				_, err := Orchestrator{}.BuildApp(context.Background(), BuildAppRequest{BuildRequest: request, Name: "any"})
				return err
			},
			"BuildSelection": func() error {
				_, err := Orchestrator{}.BuildSelection(context.Background(), request, nil)
				return err
			},
		}
		for name, run := range entryPoints {
			t.Run(credentialName+"/"+name, func(t *testing.T) {
				err := run()
				if err == nil || !strings.Contains(err.Error(), "auth-host") {
					t.Fatalf("%s() error = %v, want the missing auth host error first", name, err)
				}
				if strings.Contains(err.Error(), "early-secret-token") {
					t.Fatalf("%s() error %q leaks the token", name, err)
				}
			})
		}
	}
}
