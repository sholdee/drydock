package prcomment

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	// ActionCreated reports that Upsert posted a new comment.
	ActionCreated = "created"
	// ActionUpdated reports that Upsert edited an existing comment.
	ActionUpdated = "updated"

	markerPrefix = "<!-- add-pr-comment:"
	markerSuffix = " -->"

	maxListPages     = 50
	maxAttempts      = 3
	errorBodyLimit   = 512
	maxResponseBytes = 8 << 20
)

// Client talks to a GitHub-compatible issue-comment API. The zero HTTP client
// means http.DefaultClient; Upsert never mutates the client it is given.
type Client struct {
	BaseURL   string
	Token     string
	HTTP      *http.Client
	UserAgent string
}

// Result describes what Upsert did with the sticky comment.
type Result struct {
	Action string
	ID     int64
	URL    string
}

// Marker is the hidden HTML comment that identifies a sticky comment. It is
// byte-identical to what mshick/add-pr-comment@ec328af writes so comments
// created by the previous action are updated in place.
func Marker(messageID string) string {
	return markerPrefix + messageID + markerSuffix
}

type comment struct {
	ID      int64  `json:"id"`
	Body    string `json:"body"`
	HTMLURL string `json:"html_url"`
}

type commentPayload struct {
	Body string `json:"body"`
}

type response struct {
	status int
	header http.Header
	body   []byte
}

// Upsert posts or updates the sticky comment identified by messageID on the
// given pull request and reports what it did.
func (c Client) Upsert(ctx context.Context, repository string, number int, messageID, message string) (Result, error) {
	owner, name, err := splitRepository(repository)
	if err != nil {
		return Result{}, err
	}
	if number <= 0 {
		return Result{}, fmt.Errorf("invalid pull request number %d: want a positive number", number)
	}
	if messageID == "" || strings.ContainsFunc(messageID, unicode.IsSpace) {
		return Result{}, fmt.Errorf("invalid message id %q: want a non-empty value without whitespace", messageID)
	}
	if strings.TrimSpace(c.BaseURL) == "" {
		return Result{}, errors.New("empty API base URL")
	}

	marker := Marker(messageID)
	body := marker + "\n\n" + message
	existing, found, err := c.find(ctx, owner, name, number, marker)
	if err != nil {
		return Result{}, err
	}
	if found {
		return c.update(ctx, owner, name, existing, body)
	}
	return c.create(ctx, owner, name, number, body)
}

func (c Client) find(ctx context.Context, owner, name string, number int, marker string) (comment, bool, error) {
	next := c.commentsURL(owner, name, number) + "?per_page=100"
	for page := 1; ; page++ {
		if page > maxListPages {
			return comment{}, false, fmt.Errorf("issue comment listing exceeded %d pages", maxListPages)
		}
		resp, err := c.request(ctx, http.MethodGet, next, nil)
		if err != nil {
			return comment{}, false, err
		}
		var comments []comment
		if err := json.Unmarshal(resp.body, &comments); err != nil {
			return comment{}, false, fmt.Errorf("%s %s: decode response: %w", http.MethodGet, requestPath(next), err)
		}
		for _, item := range comments {
			if strings.Contains(item.Body, marker) {
				return item, true, nil
			}
		}
		next = c.nextPageURL(resp.header.Get("Link"))
		if next == "" {
			return comment{}, false, nil
		}
	}
}

func (c Client) create(ctx context.Context, owner, name string, number int, body string) (Result, error) {
	payload, err := json.Marshal(commentPayload{Body: body})
	if err != nil {
		return Result{}, fmt.Errorf("encode comment body: %w", err)
	}
	resp, err := c.request(ctx, http.MethodPost, c.commentsURL(owner, name, number), payload)
	if err != nil {
		return Result{}, err
	}
	var created comment
	// The comment already exists at this point, so an unreadable response body
	// only costs the reported id and URL, never the outcome.
	_ = json.Unmarshal(resp.body, &created)
	return Result{Action: ActionCreated, ID: created.ID, URL: created.HTMLURL}, nil
}

func (c Client) update(ctx context.Context, owner, name string, existing comment, body string) (Result, error) {
	payload, err := json.Marshal(commentPayload{Body: body})
	if err != nil {
		return Result{}, fmt.Errorf("encode comment body: %w", err)
	}
	resp, err := c.request(ctx, http.MethodPatch, c.commentURL(owner, name, existing.ID), payload)
	if err != nil {
		return Result{}, err
	}
	var updated comment
	// As in create: the edit landed, so an undecodable response is not fatal.
	_ = json.Unmarshal(resp.body, &updated)
	htmlURL := updated.HTMLURL
	if htmlURL == "" {
		htmlURL = existing.HTMLURL
	}
	return Result{Action: ActionUpdated, ID: existing.ID, URL: htmlURL}, nil
}

func (c Client) request(ctx context.Context, method, rawURL string, payload []byte) (response, error) {
	client := c.httpClient()
	for attempt := 1; ; attempt++ {
		resp, err := c.attempt(ctx, client, method, rawURL, payload)
		if err != nil {
			return response{}, err
		}
		if resp.status >= http.StatusOK && resp.status < http.StatusMultipleChoices {
			return resp, nil
		}
		if attempt >= maxAttempts || !retryable(method, resp) {
			return response{}, c.statusError(method, rawURL, resp)
		}
		if err := wait(ctx, retryDelay(resp, attempt)); err != nil {
			return response{}, err
		}
	}
}

func (c Client) attempt(ctx context.Context, client *http.Client, method, rawURL string, payload []byte) (response, error) {
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	request, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return response{}, fmt.Errorf("%s %s: %s", method, requestPath(rawURL), c.redact(err.Error()))
	}
	c.applyHeaders(request, payload != nil)
	resp, err := client.Do(request)
	if err != nil {
		return response{}, fmt.Errorf("%s %s: %s", method, requestPath(rawURL), c.redact(err.Error()))
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return response{}, fmt.Errorf("%s %s: %s", method, requestPath(rawURL), c.redact(err.Error()))
	}
	if len(data) > maxResponseBytes {
		return response{}, fmt.Errorf("%s %s: response exceeds %d bytes", method, requestPath(rawURL), maxResponseBytes)
	}
	return response{status: resp.StatusCode, header: resp.Header, body: data}, nil
}

func (c Client) httpClient() *http.Client {
	base := c.HTTP
	if base == nil {
		base = http.DefaultClient
	}
	// Copy so the caller's client keeps its own redirect policy; http.Client
	// holds no lock. Refusing redirects keeps the token on the requested host.
	client := *base
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &client
}

func (c Client) applyHeaders(request *http.Request, hasBody bool) {
	if c.Token != "" {
		request.Header.Set("Authorization", "token "+c.Token)
	}
	request.Header.Set("Accept", "application/json")
	if c.UserAgent != "" {
		request.Header.Set("User-Agent", c.UserAgent)
	}
	if hasBody {
		request.Header.Set("Content-Type", "application/json")
	}
}

func (c Client) statusError(method, rawURL string, resp response) error {
	message := fmt.Sprintf("%s %s: HTTP %d", method, requestPath(rawURL), resp.status)
	if snippet := errorSnippet(resp.body); snippet != "" {
		message += ": " + snippet
	}
	return errors.New(c.redact(message))
}

func (c Client) redact(message string) string {
	if c.Token == "" {
		return message
	}
	return strings.ReplaceAll(message, c.Token, "[redacted]")
}

func (c Client) commentsURL(owner, name string, number int) string {
	return c.endpoint(fmt.Sprintf("/repos/%s/%s/issues/%d/comments", url.PathEscape(owner), url.PathEscape(name), number))
}

func (c Client) commentURL(owner, name string, id int64) string {
	return c.endpoint(fmt.Sprintf("/repos/%s/%s/issues/comments/%d", url.PathEscape(owner), url.PathEscape(name), id))
}

func (c Client) endpoint(path string) string {
	return strings.TrimRight(strings.TrimSpace(c.BaseURL), "/") + path
}

func (c Client) nextPageURL(link string) string {
	base, err := url.Parse(strings.TrimSpace(c.BaseURL))
	if err != nil {
		return ""
	}
	for part := range strings.SplitSeq(link, ",") {
		segments := strings.Split(strings.TrimSpace(part), ";")
		if len(segments) < 2 || !hasRelNext(segments[1:]) {
			continue
		}
		raw := strings.TrimSpace(segments[0])
		if !strings.HasPrefix(raw, "<") || !strings.HasSuffix(raw, ">") {
			continue
		}
		candidate, err := url.Parse(raw[1 : len(raw)-1])
		if err != nil || candidate.Scheme != base.Scheme || candidate.Host != base.Host {
			continue
		}
		return candidate.String()
	}
	return ""
}

func hasRelNext(segments []string) bool {
	for _, segment := range segments {
		key, value, ok := strings.Cut(strings.TrimSpace(segment), "=")
		if !ok || strings.TrimSpace(key) != "rel" {
			continue
		}
		if strings.Trim(strings.TrimSpace(value), `"`) == "next" {
			return true
		}
	}
	return false
}

// retryable reports whether the response is worth another attempt. A POST that
// answers 5xx is never retried: the server may already have created the
// comment, and a second POST would leave two comments carrying the marker.
func retryable(method string, resp response) bool {
	switch {
	case resp.status == http.StatusTooManyRequests:
		return true
	case resp.status == http.StatusForbidden:
		return resp.header.Get("Retry-After") != ""
	case resp.status >= http.StatusInternalServerError:
		return method != http.MethodPost
	default:
		return false
	}
}

func retryDelay(resp response, attempt int) time.Duration {
	if raw := strings.TrimSpace(resp.header.Get("Retry-After")); raw != "" {
		if seconds, err := strconv.Atoi(raw); err == nil && seconds >= 0 {
			return time.Duration(seconds) * time.Second
		}
	}
	return time.Duration(attempt) * time.Second
}

func wait(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func errorSnippet(body []byte) string {
	if len(body) > errorBodyLimit {
		body = body[:errorBodyLimit]
	}
	return strings.TrimSpace(string(body))
}

func requestPath(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return parsed.EscapedPath()
}

func splitRepository(repository string) (string, string, error) {
	owner, name, ok := strings.Cut(repository, "/")
	invalid := !ok || owner == "" || name == "" ||
		strings.Contains(name, "/") || strings.ContainsFunc(repository, unicode.IsSpace)
	if invalid {
		return "", "", fmt.Errorf("invalid repository %q: want owner/name", repository)
	}
	return owner, name, nil
}
