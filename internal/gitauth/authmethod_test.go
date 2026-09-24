package gitauth

import (
	"reflect"
	"testing"

	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
)

func TestAuthMethodHTTPCredentials(t *testing.T) {
	const repoURL = "https://github.com/example/private.git"

	t.Run("bearer token wins over basic auth", func(t *testing.T) {
		auth, hasAuth, res, err := AuthMethod(Credentials{Username: "user", Password: "pass", BearerToken: "token"}, repoURL, newTestEnv(t).env)
		if err != nil {
			t.Fatalf("AuthMethod() error = %v", err)
		}
		if !hasAuth {
			t.Fatal("AuthMethod() hasAuth = false, want true")
		}
		if !reflect.DeepEqual(res, Resolution{}) {
			t.Fatalf("AuthMethod() resolution = %+v, want zero value", res)
		}
		token, ok := auth.(*githttp.TokenAuth)
		if !ok {
			t.Fatalf("AuthMethod() auth = %T, want *http.TokenAuth", auth)
		}
		if token.Token != "token" {
			t.Fatalf("AuthMethod() token = %q, want %q", token.Token, "token")
		}
	})

	t.Run("username and password produce basic auth", func(t *testing.T) {
		auth, hasAuth, _, err := AuthMethod(Credentials{Username: "user", Password: "pass"}, repoURL, newTestEnv(t).env)
		if err != nil {
			t.Fatalf("AuthMethod() error = %v", err)
		}
		if !hasAuth {
			t.Fatal("AuthMethod() hasAuth = false, want true")
		}
		basic, ok := auth.(*githttp.BasicAuth)
		if !ok {
			t.Fatalf("AuthMethod() auth = %T, want *http.BasicAuth", auth)
		}
		if basic.Username != "user" || basic.Password != "pass" {
			t.Fatalf("AuthMethod() basic auth = %q/%q, want user/pass", basic.Username, basic.Password)
		}
	})

	t.Run("no credentials produce no auth method", func(t *testing.T) {
		auth, hasAuth, res, err := AuthMethod(Credentials{}, repoURL, newTestEnv(t).env)
		if err != nil {
			t.Fatalf("AuthMethod() error = %v", err)
		}
		if auth != nil || hasAuth {
			t.Fatalf("AuthMethod() = %v, %t, want nil, false", auth, hasAuth)
		}
		if !reflect.DeepEqual(res, Resolution{}) {
			t.Fatalf("AuthMethod() resolution = %+v, want zero value", res)
		}
	})
}
