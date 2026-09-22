package prcomment

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

type recordedRequest struct {
	Method string
	Path   string
	Query  string
	Header http.Header
	Body   string
}

type recorder struct {
	mu       sync.Mutex
	requests []recordedRequest
}

func (rec *recorder) record(t *testing.T, request *http.Request) {
	t.Helper()
	body, err := io.ReadAll(request.Body)
	if err != nil {
		t.Errorf("read request body: %v", err)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.requests = append(rec.requests, recordedRequest{
		Method: request.Method,
		Path:   request.URL.Path,
		Query:  request.URL.RawQuery,
		Header: request.Header.Clone(),
		Body:   string(body),
	})
}

func (rec *recorder) all() []recordedRequest {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return slices.Clone(rec.requests)
}

func (rec *recorder) count(method string) int {
	count := 0
	for _, request := range rec.all() {
		if request.Method == method {
			count++
		}
	}
	return count
}

func decodeCommentBody(t *testing.T, payload string) string {
	t.Helper()
	var decoded struct {
		Body string `json:"body"`
	}
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		t.Fatalf("decode request payload %q: %v", payload, err)
	}
	return decoded.Body
}

func TestMarkerMatchesLegacyActionLiteral(t *testing.T) {
	// Byte-identical to what mshick/add-pr-comment@ec328af66588ab8f77cdeb2c264f14aba45bbf59
	// writes (src/config.ts: messageId = 'add-pr-comment:' + input), so sticky comments
	// created by the previous action keep being updated in place.
	want := "<!-- add-pr-comment:12/drydock-diff -->"
	if got := Marker("12/drydock-diff"); got != want {
		t.Fatalf("Marker() = %q, want %q", got, want)
	}
}

func TestUpsertUpdatesLegacyActionComment(t *testing.T) {
	rec := &recorder{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		rec.record(t, request)
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/repos/o/r/issues/12/comments":
			_, _ = io.WriteString(writer, `[{"id": 4242, "body": "<!-- add-pr-comment:12/drydock-diff -->\n\nold body\n", "html_url": "https://github.com/o/r/pull/12#issuecomment-4242"}]`)
		case request.Method == http.MethodPatch && request.URL.Path == "/repos/o/r/issues/comments/4242":
			_, _ = io.WriteString(writer, `{"id": 4242, "html_url": "https://github.com/o/r/pull/12#issuecomment-4242"}`)
		default:
			t.Errorf("unexpected request %s %s", request.Method, request.URL.Path)
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	client := Client{BaseURL: server.URL, Token: "secret-token", UserAgent: "drydock/test"}
	result, err := client.Upsert(t.Context(), "o/r", 12, "12/drydock-diff", "new body")
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	if result.Action != "updated" || result.ID != 4242 {
		t.Fatalf("Upsert() = %+v, want updated comment 4242", result)
	}
	if result.URL != "https://github.com/o/r/pull/12#issuecomment-4242" {
		t.Fatalf("Upsert() URL = %q", result.URL)
	}
	requests := rec.all()
	if len(requests) != 2 {
		t.Fatalf("requests = %d, want 2: %+v", len(requests), requests)
	}
	if got := decodeCommentBody(t, requests[1].Body); got != "<!-- add-pr-comment:12/drydock-diff -->\n\nnew body" {
		t.Fatalf("PATCH body = %q", got)
	}
}

func TestUpsertFollowsGitHubPaginationAndPatchesMatch(t *testing.T) {
	rec := &recorder{}
	var baseURL string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		rec.record(t, request)
		if request.Method == http.MethodPatch {
			_, _ = io.WriteString(writer, `{"id": 33, "html_url": "https://example.test/c/33"}`)
			return
		}
		switch request.URL.Query().Get("page") {
		case "", "1":
			writer.Header().Set("Link", "<"+baseURL+"/repos/o/r/issues/12/comments?per_page=100&page=2>; rel=\"next\"")
			_, _ = io.WriteString(writer, `[{"id": 1, "body": "unrelated"}]`)
		case "2":
			writer.Header().Set("Link", "<"+baseURL+"/repos/o/r/issues/12/comments?per_page=100&page=3>; rel=\"next\", <"+baseURL+"/repos/o/r/issues/12/comments?per_page=100&page=1>; rel=\"prev\"")
			_, _ = io.WriteString(writer, `[{"id": 2, "body": "also unrelated"}]`)
		case "3":
			_, _ = io.WriteString(writer, `[{"id": 33, "body": "head\n<!-- add-pr-comment:12/drydock-diff -->\n\nbody", "html_url": "https://example.test/c/33"}]`)
		default:
			t.Errorf("unexpected page query %q", request.URL.RawQuery)
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	baseURL = server.URL

	client := Client{BaseURL: server.URL, Token: "t", UserAgent: "drydock/test"}
	result, err := client.Upsert(t.Context(), "o/r", 12, "12/drydock-diff", "updated")
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	if result.Action != "updated" || result.ID != 33 {
		t.Fatalf("Upsert() = %+v, want updated comment 33", result)
	}
	requests := rec.all()
	if got := rec.count(http.MethodGet); got != 3 {
		t.Fatalf("GET requests = %d, want 3", got)
	}
	if !strings.Contains(requests[0].Query, "per_page=100") {
		t.Fatalf("first GET query = %q, want per_page=100", requests[0].Query)
	}
	last := requests[len(requests)-1]
	if last.Method != http.MethodPatch || last.Path != "/repos/o/r/issues/comments/33" {
		t.Fatalf("last request = %s %s, want PATCH /repos/o/r/issues/comments/33", last.Method, last.Path)
	}
}

func TestUpsertCreatesCommentWithExactBody(t *testing.T) {
	rec := &recorder{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		rec.record(t, request)
		if request.Method == http.MethodPost {
			writer.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(writer, `{"id": 77, "html_url": "https://example.test/c/77"}`)
			return
		}
		_, _ = io.WriteString(writer, `[{"id": 1, "body": "<!-- add-pr-comment:12/other -->\n\nnope"}]`)
	}))
	defer server.Close()

	message := "line with \"quotes\", ünïcode ✅ and a trailing newline\n"
	client := Client{BaseURL: server.URL + "/", Token: "t", UserAgent: "drydock/test"}
	result, err := client.Upsert(t.Context(), "o/r", 12, "12/drydock-diff", message)
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	if result.Action != "created" || result.ID != 77 || result.URL != "https://example.test/c/77" {
		t.Fatalf("Upsert() = %+v, want created comment 77", result)
	}
	requests := rec.all()
	if len(requests) != 2 || requests[1].Method != http.MethodPost || requests[1].Path != "/repos/o/r/issues/12/comments" {
		t.Fatalf("requests = %+v", requests)
	}
	want := "<!-- add-pr-comment:12/drydock-diff -->\n\n" + message
	if got := decodeCommentBody(t, requests[1].Body); got != want {
		t.Fatalf("POST body = %q, want %q", got, want)
	}
}

func TestUpsertForgejoSingleListThenPost(t *testing.T) {
	rec := &recorder{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/repos/o/r/issues/12/comments", func(writer http.ResponseWriter, request *http.Request) {
		rec.record(t, request)
		if request.Method == http.MethodPost {
			writer.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(writer, `{"id": 9, "html_url": "http://forgejo.test/o/r/pulls/12#issuecomment-9"}`)
			return
		}
		writer.Header().Set("X-Total-Count", "1")
		_, _ = io.WriteString(writer, `[{"id": 5, "body": "plain review comment", "html_url": "http://forgejo.test/o/r/pulls/12#issuecomment-5"}]`)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client := Client{BaseURL: server.URL + "/api/v1/", Token: "t", UserAgent: "drydock/test"}
	result, err := client.Upsert(t.Context(), "o/r", 12, "12/drydock-diff", "hello")
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	if result.Action != "created" || result.ID != 9 {
		t.Fatalf("Upsert() = %+v, want created comment 9", result)
	}
	if got := rec.count(http.MethodGet); got != 1 {
		t.Fatalf("GET requests = %d, want 1", got)
	}
}

func TestUpsertIgnoresForeignMessageIDs(t *testing.T) {
	rec := &recorder{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		rec.record(t, request)
		if request.Method == http.MethodPatch {
			_, _ = io.WriteString(writer, `{"id": 200}`)
			return
		}
		_, _ = io.WriteString(writer, `[{"id": 100, "body": "<!-- add-pr-comment:7/drydock-images -->\n\nimages"},{"id": 200, "body": "<!-- add-pr-comment:7/drydock-diff -->\n\ndiff"}]`)
	}))
	defer server.Close()

	client := Client{BaseURL: server.URL, Token: "t", UserAgent: "drydock/test"}
	result, err := client.Upsert(t.Context(), "o/r", 7, "7/drydock-diff", "new diff")
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	if result.Action != "updated" || result.ID != 200 {
		t.Fatalf("Upsert() = %+v, want updated comment 200", result)
	}
	if path := rec.all()[1].Path; path != "/repos/o/r/issues/comments/200" {
		t.Fatalf("PATCH path = %q", path)
	}
}

func TestUpsertSendsExpectedHeaders(t *testing.T) {
	rec := &recorder{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		rec.record(t, request)
		if request.Method == http.MethodPost {
			writer.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(writer, `{"id": 1}`)
			return
		}
		_, _ = io.WriteString(writer, `[]`)
	}))
	defer server.Close()

	client := Client{BaseURL: server.URL, Token: "x", UserAgent: "drydock/1.2.3"}
	if _, err := client.Upsert(t.Context(), "o/r", 3, "3/drydock-diff", "hi"); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	for _, request := range rec.all() {
		if got := request.Header.Get("Authorization"); got != "token x" {
			t.Fatalf("%s Authorization = %q, want %q", request.Method, got, "token x")
		}
		if got := request.Header.Get("Accept"); got != "application/json" {
			t.Fatalf("%s Accept = %q", request.Method, got)
		}
		if got := request.Header.Get("User-Agent"); got != "drydock/1.2.3" {
			t.Fatalf("%s User-Agent = %q", request.Method, got)
		}
	}
	if got := rec.all()[1].Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("POST Content-Type = %q", got)
	}
}

func TestUpsertRedactsTokenFromErrors(t *testing.T) {
	const token = "ghs_supersecret"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(writer, `{"message": "bad credentials for `+request.Header.Get("Authorization")+`"}`)
	}))
	defer server.Close()

	client := Client{BaseURL: server.URL, Token: token, UserAgent: "drydock/test"}
	_, err := client.Upsert(t.Context(), "o/r", 4, "4/drydock-diff", "hi")
	if err == nil {
		t.Fatal("Upsert() error = nil, want unauthorized error")
	}
	message := err.Error()
	if strings.Contains(message, token) {
		t.Fatalf("error leaks the token: %q", message)
	}
	if !strings.Contains(message, "HTTP 401") || !strings.Contains(message, "[redacted]") {
		t.Fatalf("error = %q, want HTTP 401 and [redacted]", message)
	}
	if strings.Contains(message, "per_page") {
		t.Fatalf("error includes a query string: %q", message)
	}
}

func TestUpsertEmptyTokenKeepsResponseBodyVerbatim(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(writer, `{"message": "pull request not found"}`)
	}))
	defer server.Close()

	client := Client{BaseURL: server.URL, UserAgent: "drydock/test"}
	_, err := client.Upsert(t.Context(), "o/r", 4, "4/drydock-diff", "hi")
	if err == nil {
		t.Fatal("Upsert() error = nil, want not found error")
	}
	if !strings.Contains(err.Error(), `{"message": "pull request not found"}`) {
		t.Fatalf("error = %q, want the verbatim response body", err.Error())
	}
}

func TestUpsertRetriesListOnRateLimit(t *testing.T) {
	rec := &recorder{}
	attempts := 0
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		rec.record(t, request)
		if request.Method == http.MethodPost {
			writer.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(writer, `{"id": 1}`)
			return
		}
		mu.Lock()
		attempts++
		first := attempts == 1
		mu.Unlock()
		if first {
			writer.Header().Set("Retry-After", "0")
			writer.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = io.WriteString(writer, `[]`)
	}))
	defer server.Close()

	client := Client{BaseURL: server.URL, Token: "t", UserAgent: "drydock/test"}
	result, err := client.Upsert(t.Context(), "o/r", 4, "4/drydock-diff", "hi")
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	if result.Action != "created" {
		t.Fatalf("Upsert() = %+v, want created", result)
	}
	if got := rec.count(http.MethodGet); got != 2 {
		t.Fatalf("GET requests = %d, want 2", got)
	}
}

func TestUpsertGivesUpAfterThreeListFailures(t *testing.T) {
	rec := &recorder{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		rec.record(t, request)
		// Retry-After keeps the backoff at zero so the test does not sleep.
		writer.Header().Set("Retry-After", "0")
		writer.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(writer, "boom")
	}))
	defer server.Close()

	client := Client{BaseURL: server.URL, Token: "t", UserAgent: "drydock/test"}
	_, err := client.Upsert(t.Context(), "o/r", 4, "4/drydock-diff", "hi")
	if err == nil {
		t.Fatal("Upsert() error = nil, want server error")
	}
	if !strings.Contains(err.Error(), "HTTP 500") {
		t.Fatalf("error = %q, want HTTP 500", err.Error())
	}
	if got := rec.count(http.MethodGet); got != 3 {
		t.Fatalf("GET requests = %d, want 3", got)
	}
}

func TestUpsertRetriesPatchAndResendsBody(t *testing.T) {
	rec := &recorder{}
	patches := 0
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		rec.record(t, request)
		if request.Method != http.MethodPatch {
			_, _ = io.WriteString(writer, `[{"id": 8, "body": "<!-- add-pr-comment:4/drydock-diff -->\n\nold"}]`)
			return
		}
		mu.Lock()
		patches++
		first := patches == 1
		mu.Unlock()
		if first {
			writer.Header().Set("Retry-After", "0")
			writer.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = io.WriteString(writer, `{"id": 8, "html_url": "https://example.test/c/8"}`)
	}))
	defer server.Close()

	client := Client{BaseURL: server.URL, Token: "t", UserAgent: "drydock/test"}
	result, err := client.Upsert(t.Context(), "o/r", 4, "4/drydock-diff", "fresh")
	if err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	if result.Action != "updated" || result.ID != 8 {
		t.Fatalf("Upsert() = %+v, want updated comment 8", result)
	}
	requests := rec.all()
	if got := rec.count(http.MethodPatch); got != 2 {
		t.Fatalf("PATCH requests = %d, want 2", got)
	}
	want := "<!-- add-pr-comment:4/drydock-diff -->\n\nfresh"
	for _, request := range requests {
		if request.Method != http.MethodPatch {
			continue
		}
		if got := decodeCommentBody(t, request.Body); got != want {
			t.Fatalf("PATCH body = %q, want %q", got, want)
		}
	}
}

func TestUpsertDoesNotRetryPostOnServerError(t *testing.T) {
	rec := &recorder{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		rec.record(t, request)
		if request.Method == http.MethodPost {
			writer.Header().Set("Retry-After", "0")
			writer.WriteHeader(http.StatusBadGateway)
			_, _ = io.WriteString(writer, "upstream exploded")
			return
		}
		_, _ = io.WriteString(writer, `[]`)
	}))
	defer server.Close()

	client := Client{BaseURL: server.URL, Token: "t", UserAgent: "drydock/test"}
	_, err := client.Upsert(t.Context(), "o/r", 4, "4/drydock-diff", "hi")
	if err == nil {
		t.Fatal("Upsert() error = nil, want bad gateway error")
	}
	if !strings.Contains(err.Error(), "HTTP 502") {
		t.Fatalf("error = %q, want HTTP 502", err.Error())
	}
	if got := rec.count(http.MethodPost); got != 1 {
		t.Fatalf("POST requests = %d, want exactly 1", got)
	}
}

func TestUpsertRefusesRedirectsToAnotherHost(t *testing.T) {
	elsewhere := &recorder{}
	other := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		elsewhere.record(t, request)
		_, _ = io.WriteString(writer, `[]`)
	}))
	defer other.Close()

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, other.URL+"/repos/o/r/issues/4/comments", http.StatusFound)
	}))
	defer server.Close()

	caller := &http.Client{Timeout: time.Minute}
	for name, httpClient := range map[string]*http.Client{"default": nil, "caller-supplied": caller} {
		t.Run(name, func(t *testing.T) {
			client := Client{BaseURL: server.URL, Token: "t", HTTP: httpClient, UserAgent: "drydock/test"}
			_, err := client.Upsert(t.Context(), "o/r", 4, "4/drydock-diff", "hi")
			if err == nil {
				t.Fatal("Upsert() error = nil, want redirect error")
			}
			if !strings.Contains(err.Error(), "HTTP 302") {
				t.Fatalf("error = %q, want HTTP 302", err.Error())
			}
		})
	}
	if got := len(elsewhere.all()); got != 0 {
		t.Fatalf("redirect target received %d requests, want 0", got)
	}
	if caller.CheckRedirect != nil {
		t.Fatal("caller-supplied client was mutated")
	}
}

func TestUpsertIgnoresCrossHostLinkHeader(t *testing.T) {
	elsewhere := &recorder{}
	other := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		elsewhere.record(t, request)
		_, _ = io.WriteString(writer, `[]`)
	}))
	defer other.Close()

	rec := &recorder{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		rec.record(t, request)
		if request.Method == http.MethodPost {
			writer.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(writer, `{"id": 1}`)
			return
		}
		writer.Header().Set("Link", "<"+other.URL+"/repos/o/r/issues/4/comments?page=2>; rel=\"next\"")
		_, _ = io.WriteString(writer, `[]`)
	}))
	defer server.Close()

	client := Client{BaseURL: server.URL, Token: "t", UserAgent: "drydock/test"}
	if _, err := client.Upsert(t.Context(), "o/r", 4, "4/drydock-diff", "hi"); err != nil {
		t.Fatalf("Upsert() error = %v", err)
	}
	if got := rec.count(http.MethodGet); got != 1 {
		t.Fatalf("GET requests = %d, want 1", got)
	}
	if got := len(elsewhere.all()); got != 0 {
		t.Fatalf("cross-host Link target received %d requests, want 0", got)
	}
}

func TestUpsertCapsPagination(t *testing.T) {
	rec := &recorder{}
	var baseURL string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		rec.record(t, request)
		writer.Header().Set("Link", "<"+baseURL+"/repos/o/r/issues/4/comments?per_page=100&page=99>; rel=\"next\"")
		_, _ = io.WriteString(writer, `[]`)
	}))
	defer server.Close()
	baseURL = server.URL

	client := Client{BaseURL: server.URL, Token: "t", UserAgent: "drydock/test"}
	_, err := client.Upsert(t.Context(), "o/r", 4, "4/drydock-diff", "hi")
	if err == nil {
		t.Fatal("Upsert() error = nil, want pagination cap error")
	}
	if !strings.Contains(err.Error(), "50") {
		t.Fatalf("error = %q, want the page cap", err.Error())
	}
	if got := rec.count(http.MethodGet); got != 50 {
		t.Fatalf("GET requests = %d, want 50", got)
	}
}

func TestUpsertRejectsInvalidArguments(t *testing.T) {
	rec := &recorder{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		rec.record(t, request)
		_, _ = io.WriteString(writer, `[]`)
	}))
	defer server.Close()

	client := Client{BaseURL: server.URL, Token: "t", UserAgent: "drydock/test"}
	for name, test := range map[string]struct {
		repository string
		number     int
		messageID  string
	}{
		"no slash":       {repository: "owner", number: 1, messageID: "1/drydock-diff"},
		"empty owner":    {repository: "/r", number: 1, messageID: "1/drydock-diff"},
		"empty name":     {repository: "o/", number: 1, messageID: "1/drydock-diff"},
		"two slashes":    {repository: "o/r/x", number: 1, messageID: "1/drydock-diff"},
		"whitespace":     {repository: "o /r", number: 1, messageID: "1/drydock-diff"},
		"zero number":    {repository: "o/r", number: 0, messageID: "1/drydock-diff"},
		"empty messages": {repository: "o/r", number: 1, messageID: ""},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := client.Upsert(t.Context(), test.repository, test.number, test.messageID, "hi"); err == nil {
				t.Fatalf("Upsert(%q, %d, %q) error = nil, want validation error", test.repository, test.number, test.messageID)
			}
		})
	}
	if got := len(rec.all()); got != 0 {
		t.Fatalf("server received %d requests, want 0", got)
	}
}

func TestRetryDelayDefaultsToLinearBackoff(t *testing.T) {
	header := http.Header{}
	if got := retryDelay(response{header: header}, 1); got != time.Second {
		t.Fatalf("retryDelay(attempt 1) = %v, want 1s", got)
	}
	if got := retryDelay(response{header: header}, 2); got != 2*time.Second {
		t.Fatalf("retryDelay(attempt 2) = %v, want 2s", got)
	}
	header.Set("Retry-After", "7")
	if got := retryDelay(response{header: header}, 1); got != 7*time.Second {
		t.Fatalf("retryDelay(Retry-After: 7) = %v, want 7s", got)
	}
	header.Set("Retry-After", "Wed, 21 Oct 2026 07:28:00 GMT")
	if got := retryDelay(response{header: header}, 2); got != 2*time.Second {
		t.Fatalf("retryDelay(http-date Retry-After) = %v, want the default backoff", got)
	}
}
