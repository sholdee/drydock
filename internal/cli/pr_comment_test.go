package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

type prCommentRequest struct {
	Method string
	Path   string
	Query  string
	Header http.Header
	Body   string
}

type prCommentRecorder struct {
	mu       sync.Mutex
	requests []prCommentRequest
}

func (rec *prCommentRecorder) record(t *testing.T, request *http.Request) {
	t.Helper()
	body, err := io.ReadAll(request.Body)
	if err != nil {
		t.Errorf("read request body: %v", err)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.requests = append(rec.requests, prCommentRequest{
		Method: request.Method,
		Path:   request.URL.Path,
		Query:  request.URL.RawQuery,
		Header: request.Header.Clone(),
		Body:   string(body),
	})
}

func (rec *prCommentRecorder) all() []prCommentRequest {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return slices.Clone(rec.requests)
}

type prCommentResult struct {
	Stdout string
	Stderr string
	Err    error
}

// runPRCommentCLI drives the command through NewRootCommand so a missing
// registration fails these tests rather than passing silently.
func runPRCommentCLI(t *testing.T, stdin io.Reader, args ...string) prCommentResult {
	t.Helper()
	cmd := NewRootCommand(VersionInfo{Version: "test", Commit: "none"})
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.SetArgs(args)
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	if stdin != nil {
		cmd.SetIn(stdin)
	}
	err := cmd.Execute()
	return prCommentResult{Stdout: stdout.String(), Stderr: stderr.String(), Err: err}
}

func writePRCommentBodyFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "comment.md")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write body file: %v", err)
	}
	return path
}

func decodePRCommentBody(t *testing.T, payload string) string {
	t.Helper()
	var decoded struct {
		Body string `json:"body"`
	}
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		t.Fatalf("decode request payload %q: %v", payload, err)
	}
	return decoded.Body
}

// newPRCommentServer answers the issue-comment API with the given listing and
// records every request it saw.
func newPRCommentServer(t *testing.T, listing string) (*httptest.Server, *prCommentRecorder) {
	t.Helper()
	rec := &prCommentRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		rec.record(t, request)
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/repos/o/r/issues/12/comments":
			_, _ = io.WriteString(writer, listing)
		case request.Method == http.MethodPost && request.URL.Path == "/repos/o/r/issues/12/comments":
			writer.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(writer, `{"id": 77, "html_url": "https://example.test/o/r/pull/12#issuecomment-77"}`)
		case request.Method == http.MethodPatch && request.URL.Path == "/repos/o/r/issues/comments/4242":
			_, _ = io.WriteString(writer, `{"id": 4242, "html_url": "https://example.test/o/r/pull/12#issuecomment-4242"}`)
		default:
			t.Errorf("unexpected request %s %s", request.Method, request.URL.Path)
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server, rec
}

// newPRCommentRefusingServer stands in for an API that must never be reached:
// any request it sees fails the test, so a validation that stops
// short-circuiting surfaces as an unexpected call instead of being swallowed
// by a transport error.
func newPRCommentRefusingServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		t.Errorf("unexpected request %s %s", request.Method, request.URL.Path)
		writer.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)
	return server
}

func TestPRCommentCreatesComment(t *testing.T) {
	t.Setenv("DRYDOCK_GITHUB_TOKEN", "secret-token")
	server, rec := newPRCommentServer(t, `[]`)

	result := runPRCommentCLI(t, nil,
		"pr", "comment",
		"--api-url", server.URL,
		"--repository", "o/r",
		"--number", "12",
		"--message-id", "12/drydock-diff",
		"--body-file", writePRCommentBodyFile(t, "fresh body"),
	)

	if result.Err != nil {
		t.Fatalf("Execute() error = %v\nstderr:\n%s", result.Err, result.Stderr)
	}
	if want := "created https://example.test/o/r/pull/12#issuecomment-77\n"; result.Stdout != want {
		t.Fatalf("stdout = %q, want %q", result.Stdout, want)
	}
	requests := rec.all()
	if len(requests) != 2 || requests[1].Method != http.MethodPost {
		t.Fatalf("requests = %#v, want a GET then a POST", requests)
	}
	if got, want := decodePRCommentBody(t, requests[1].Body), "<!-- add-pr-comment:12/drydock-diff -->\n\nfresh body"; got != want {
		t.Fatalf("posted body = %q, want %q", got, want)
	}
	if got := requests[0].Header.Get("Authorization"); got != "token secret-token" {
		t.Fatalf("Authorization = %q, want %q", got, "token secret-token")
	}
	if got := requests[0].Header.Get("User-Agent"); !strings.HasPrefix(got, "drydock/") {
		t.Fatalf("User-Agent = %q, want a drydock/<version> value", got)
	}
}

func TestPRCommentUpdatesExistingComment(t *testing.T) {
	t.Setenv("DRYDOCK_GITHUB_TOKEN", "secret-token")
	server, rec := newPRCommentServer(t,
		`[{"id": 4242, "body": "<!-- add-pr-comment:12/drydock-diff -->\n\nold body\n", "html_url": "https://example.test/o/r/pull/12#issuecomment-4242"}]`)

	result := runPRCommentCLI(t, nil,
		"pr", "comment",
		"--api-url", server.URL,
		"--repository", "o/r",
		"--number", "12",
		"--message-id", "12/drydock-diff",
		"--body-file", writePRCommentBodyFile(t, "new body"),
	)

	if result.Err != nil {
		t.Fatalf("Execute() error = %v\nstderr:\n%s", result.Err, result.Stderr)
	}
	if want := "updated https://example.test/o/r/pull/12#issuecomment-4242\n"; result.Stdout != want {
		t.Fatalf("stdout = %q, want %q", result.Stdout, want)
	}
	requests := rec.all()
	if len(requests) != 2 || requests[1].Method != http.MethodPatch {
		t.Fatalf("requests = %#v, want a GET then a PATCH", requests)
	}
	if got, want := decodePRCommentBody(t, requests[1].Body), "<!-- add-pr-comment:12/drydock-diff -->\n\nnew body"; got != want {
		t.Fatalf("patched body = %q, want %q", got, want)
	}
}

func TestPRCommentDefaultsAPIURLAndRepositoryFromEnvironment(t *testing.T) {
	t.Setenv("DRYDOCK_GITHUB_TOKEN", "secret-token")
	server, rec := newPRCommentServer(t, `[]`)
	t.Setenv("GITHUB_API_URL", server.URL+"/")
	t.Setenv("GITHUB_REPOSITORY", "o/r")

	result := runPRCommentCLI(t, nil,
		"pr", "comment",
		"--number", "12",
		"--message-id", "12/drydock-diff",
		"--body-file", writePRCommentBodyFile(t, "body"),
	)

	if result.Err != nil {
		t.Fatalf("Execute() error = %v\nstderr:\n%s", result.Err, result.Stderr)
	}
	requests := rec.all()
	if len(requests) != 2 {
		t.Fatalf("requests = %#v, want a GET then a POST", requests)
	}
	if requests[0].Path != "/repos/o/r/issues/12/comments" {
		t.Fatalf("list path = %q", requests[0].Path)
	}
}

func TestPRCommentReadsBodyFromStdin(t *testing.T) {
	t.Setenv("DRYDOCK_GITHUB_TOKEN", "secret-token")
	server, rec := newPRCommentServer(t, `[]`)

	result := runPRCommentCLI(t, strings.NewReader("piped \"body\" ✅"),
		"pr", "comment",
		"--api-url", server.URL,
		"--repository", "o/r",
		"--number", "12",
		"--message-id", "12/drydock-diff",
		"--body-file", "-",
	)

	if result.Err != nil {
		t.Fatalf("Execute() error = %v\nstderr:\n%s", result.Err, result.Stderr)
	}
	requests := rec.all()
	if len(requests) != 2 {
		t.Fatalf("requests = %#v, want a GET then a POST", requests)
	}
	if got, want := decodePRCommentBody(t, requests[1].Body), "<!-- add-pr-comment:12/drydock-diff -->\n\npiped \"body\" ✅"; got != want {
		t.Fatalf("posted body = %q, want %q", got, want)
	}
}

func TestPRCommentFallsBackToGitHubToken(t *testing.T) {
	t.Setenv("DRYDOCK_GITHUB_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "runner-token")
	server, rec := newPRCommentServer(t, `[]`)

	result := runPRCommentCLI(t, nil,
		"pr", "comment",
		"--api-url", server.URL,
		"--repository", "o/r",
		"--number", "12",
		"--message-id", "12/drydock-diff",
		"--body-file", writePRCommentBodyFile(t, "body"),
	)

	if result.Err != nil {
		t.Fatalf("Execute() error = %v\nstderr:\n%s", result.Err, result.Stderr)
	}
	if got := rec.all()[0].Header.Get("Authorization"); got != "token runner-token" {
		t.Fatalf("Authorization = %q, want %q", got, "token runner-token")
	}
}

func TestPRCommentReadsCustomTokenEnvironmentVariable(t *testing.T) {
	t.Setenv("DRYDOCK_GITHUB_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("FORGEJO_TOKEN", "forgejo-token")
	server, rec := newPRCommentServer(t, `[]`)

	result := runPRCommentCLI(t, nil,
		"pr", "comment",
		"--api-url", server.URL,
		"--repository", "o/r",
		"--number", "12",
		"--message-id", "12/drydock-diff",
		"--token-env", "FORGEJO_TOKEN",
		"--body-file", writePRCommentBodyFile(t, "body"),
	)

	if result.Err != nil {
		t.Fatalf("Execute() error = %v\nstderr:\n%s", result.Err, result.Stderr)
	}
	if got := rec.all()[0].Header.Get("Authorization"); got != "token forgejo-token" {
		t.Fatalf("Authorization = %q, want %q", got, "token forgejo-token")
	}
}

func TestPRCommentExplicitTokenEnvDoesNotFallBack(t *testing.T) {
	t.Setenv("DRYDOCK_GITHUB_TOKEN", "")
	t.Setenv("FORGEJO_TOKEN", "")
	// A GitHub-issued token must not be sent to whatever --api-url names just
	// because the variable the caller chose is empty or misspelled.
	t.Setenv("GITHUB_TOKEN", "runner-token")
	server := newPRCommentRefusingServer(t)

	result := runPRCommentCLI(t, nil,
		"pr", "comment",
		"--api-url", server.URL,
		"--repository", "o/r",
		"--number", "12",
		"--message-id", "12/drydock-diff",
		"--token-env", "FORGEJO_TOKEN",
		"--body-file", writePRCommentBodyFile(t, "body"),
	)

	if result.Err == nil {
		t.Fatal("Execute() error = nil, want a missing token error")
	}
	if want := "no token: set $FORGEJO_TOKEN"; result.Err.Error() != want {
		t.Fatalf("error = %q, want %q", result.Err.Error(), want)
	}
	for _, field := range []string{result.Err.Error(), result.Stdout, result.Stderr} {
		if strings.Contains(field, "runner-token") {
			t.Fatalf("output leaked the fallback token: %q", field)
		}
	}
}

func TestPRCommentRequiresToken(t *testing.T) {
	t.Setenv("DRYDOCK_GITHUB_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	server, rec := newPRCommentServer(t, `[]`)

	result := runPRCommentCLI(t, nil,
		"pr", "comment",
		"--api-url", server.URL,
		"--repository", "o/r",
		"--number", "12",
		"--message-id", "12/drydock-diff",
		"--body-file", writePRCommentBodyFile(t, "body"),
	)

	if result.Err == nil {
		t.Fatal("Execute() error = nil, want a missing token error")
	}
	if want := "no token: set $DRYDOCK_GITHUB_TOKEN or $GITHUB_TOKEN"; result.Err.Error() != want {
		t.Fatalf("error = %q, want %q", result.Err.Error(), want)
	}
	if requests := rec.all(); len(requests) != 0 {
		t.Fatalf("requests = %#v, want none", requests)
	}
}

func TestPRCommentRejectsIncompleteInvocations(t *testing.T) {
	bodyFile := writePRCommentBodyFile(t, "body")
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{
			name:    "missing number",
			args:    []string{"--message-id", "12/drydock-diff", "--body-file", bodyFile},
			wantErr: `required flag(s) "number" not set`,
		},
		{
			name:    "missing message id",
			args:    []string{"--number", "12", "--body-file", bodyFile},
			wantErr: `required flag(s) "message-id" not set`,
		},
		{
			name:    "missing body file",
			args:    []string{"--number", "12", "--message-id", "12/drydock-diff"},
			wantErr: `required flag(s) "body-file" not set`,
		},
		{
			name:    "non-positive number",
			args:    []string{"--number", "0", "--message-id", "12/drydock-diff", "--body-file", bodyFile},
			wantErr: "invalid --number 0",
		},
		{
			name:    "message id with whitespace",
			args:    []string{"--number", "12", "--message-id", "12 drydock-diff", "--body-file", bodyFile},
			wantErr: `invalid --message-id "12 drydock-diff"`,
		},
		{
			name:    "missing repository",
			args:    []string{"--number", "12", "--message-id", "12/drydock-diff", "--body-file", bodyFile, "--repository", ""},
			wantErr: "no repository: pass --repository or set $GITHUB_REPOSITORY",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("DRYDOCK_GITHUB_TOKEN", "secret-token")
			t.Setenv("GITHUB_REPOSITORY", "")
			server := newPRCommentRefusingServer(t)
			args := append([]string{"pr", "comment", "--api-url", server.URL}, tt.args...)
			if !slices.Contains(tt.args, "--repository") {
				args = append(args, "--repository", "o/r")
			}
			result := runPRCommentCLI(t, nil, args...)
			if result.Err == nil {
				t.Fatalf("Execute() error = nil, want a usage error\nstdout:\n%s", result.Stdout)
			}
			if !strings.Contains(result.Err.Error(), tt.wantErr) {
				t.Fatalf("error = %q, want it to contain %q", result.Err.Error(), tt.wantErr)
			}
		})
	}
}

func TestPRCommentHelpListsFlags(t *testing.T) {
	result := runCLI(t, "pr", "comment", "--help")
	assertStdoutContainsAll(t, result,
		"--api-url",
		"--repository",
		"--number",
		"--message-id",
		"--body-file",
		"--token-env",
		"--timeout",
		"DRYDOCK_GITHUB_TOKEN",
	)
}

func TestPRCommentFailureRedactsTokenAndExitsTwo(t *testing.T) {
	const token = "super-secret-token"
	t.Setenv("DRYDOCK_GITHUB_TOKEN", token)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(writer, `{"message": "bad credentials for `+request.Header.Get("Authorization")+`"}`)
	}))
	t.Cleanup(server.Close)

	result := runPRCommentCLI(t, nil,
		"pr", "comment",
		"--api-url", server.URL,
		"--repository", "o/r",
		"--number", "12",
		"--message-id", "12/drydock-diff",
		"--body-file", writePRCommentBodyFile(t, "body"),
	)

	if result.Err == nil {
		t.Fatal("Execute() error = nil, want an HTTP 401 error")
	}
	// main.go exits 2 for every error that is not an ExitError.
	if exitErr, ok := errors.AsType[ExitError](result.Err); ok {
		t.Fatalf("error is ExitError{Code: %d}, want a plain error so the binary exits 2", exitErr.Code)
	}
	for _, field := range []string{result.Err.Error(), result.Stdout, result.Stderr} {
		if strings.Contains(field, token) {
			t.Fatalf("output leaked the token: %q", field)
		}
	}
	for _, want := range []string{"HTTP 401", "[redacted]"} {
		if !strings.Contains(result.Err.Error(), want) {
			t.Fatalf("error = %q, want it to contain %q", result.Err.Error(), want)
		}
	}
}

func TestPRCommentFallsBackToNumericIDWhenHTMLURLIsAbsent(t *testing.T) {
	t.Setenv("DRYDOCK_GITHUB_TOKEN", "secret-token")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPost {
			writer.WriteHeader(http.StatusCreated)
			// Gitea/Forgejo deployments behind a proxy can omit html_url.
			_, _ = io.WriteString(writer, `{"id": 77}`)
			return
		}
		_, _ = io.WriteString(writer, `[]`)
	}))
	t.Cleanup(server.Close)

	result := runPRCommentCLI(t, nil,
		"pr", "comment",
		"--api-url", server.URL,
		"--repository", "o/r",
		"--number", "12",
		"--message-id", "12/drydock-diff",
		"--body-file", writePRCommentBodyFile(t, "body"),
	)

	if result.Err != nil {
		t.Fatalf("Execute() error = %v\nstderr:\n%s", result.Err, result.Stderr)
	}
	if want := "created 77\n"; result.Stdout != want {
		t.Fatalf("stdout = %q, want %q", result.Stdout, want)
	}
}

func TestPRCommentTimeoutBoundsTheAPICalls(t *testing.T) {
	t.Setenv("DRYDOCK_GITHUB_TOKEN", "secret-token")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_, _ = io.WriteString(writer, `[]`)
	}))
	t.Cleanup(server.Close)

	result := runPRCommentCLI(t, nil,
		"pr", "comment",
		"--api-url", server.URL,
		"--repository", "o/r",
		"--number", "12",
		"--message-id", "12/drydock-diff",
		"--timeout", "1ms",
		"--body-file", writePRCommentBodyFile(t, "body"),
	)

	if result.Err == nil {
		t.Fatal("Execute() error = nil, want a deadline error")
	}
	if !strings.Contains(result.Err.Error(), "context deadline exceeded") {
		t.Fatalf("error = %q, want it to report the deadline", result.Err.Error())
	}
}

func TestPRCommentRejectsNonPositiveTimeout(t *testing.T) {
	t.Setenv("DRYDOCK_GITHUB_TOKEN", "secret-token")
	server := newPRCommentRefusingServer(t)

	result := runPRCommentCLI(t, nil,
		"pr", "comment",
		"--api-url", server.URL,
		"--repository", "o/r",
		"--number", "12",
		"--message-id", "12/drydock-diff",
		"--timeout", "0",
		"--body-file", writePRCommentBodyFile(t, "body"),
	)

	if result.Err == nil {
		t.Fatal("Execute() error = nil, want a usage error")
	}
	if want := "invalid --timeout 0s"; !strings.Contains(result.Err.Error(), want) {
		t.Fatalf("error = %q, want it to contain %q", result.Err.Error(), want)
	}
}
