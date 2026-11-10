package gitlfs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/sholdee/drydock/internal/credhost"
	"github.com/sholdee/drydock/internal/gitauth"
)

// mediaType is the media type of the Git LFS API.
const mediaType = "application/vnd.git-lfs+json"

// batchRequest is the body of a batch API request.
type batchRequest struct {
	Operation string      `json:"operation"`
	Transfers []string    `json:"transfers"`
	Objects   []objectRef `json:"objects"`
	HashAlgo  string      `json:"hash_algo"`
}

type objectRef struct {
	OID  string `json:"oid"`
	Size int64  `json:"size"`
}

// batchResponse is the body of a batch API response.
type batchResponse struct {
	Transfer string        `json:"transfer"`
	Objects  []batchObject `json:"objects"`
	HashAlgo string        `json:"hash_algo"`
}

type batchObject struct {
	OID     string                 `json:"oid"`
	Size    int64                  `json:"size"`
	Actions map[string]batchAction `json:"actions"`
	Error   *batchError            `json:"error"`
}

// batchAction is one action of an object. Header holds the headers that
// the server asks the client to send to Href, and nowhere else.
type batchAction struct {
	Href   string            `json:"href"`
	Header map[string]string `json:"header"`
}

type batchError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// errorBody is the body of a batch API error response.
type errorBody struct {
	Message string `json:"message"`
}

// batch asks the batch API for the download actions of objects. It sends
// the --git-* HTTP credentials only when the scheme, host, and port of the
// batch URL match a --git-auth-host entry, and never for an SSH remote,
// whose batch request gets the headers of git-lfs-authenticate instead. A
// redirect that leaves the origin of the request loses the credentials and
// those headers.
func (c *client) batch(ctx context.Context, objects []objectRef) (map[string]batchObject, *url.URL, error) {
	body, err := json.Marshal(batchRequest{Operation: "download", Transfers: []string{"basic"}, Objects: objects, HashAlgo: "sha256"})
	if err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, c.limits.BatchTimeout)
	defer cancel()
	endpoint, header, err := c.batchEndpoint(ctx)
	if err != nil {
		return nil, nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, BatchURL(endpoint), bytes.NewReader(body))
	if err != nil {
		return nil, nil, errors.New("the Git LFS endpoint is not a valid URL")
	}
	names := make([]string, 0, len(header))
	for name, value := range header {
		request.Header.Set(name, value)
		names = append(names, name)
	}
	request.Header.Set("Accept", mediaType)
	request.Header.Set("Content-Type", mediaType)
	httpClient := &http.Client{Transport: c.transport, CheckRedirect: dropHeadersOffOrigin(names)}
	if c.endpoint.SSH == nil && c.credentials.AuthorizesHTTP(request.URL.String()) {
		applyAuth(request, c.credentials)
	}
	httpClient = credhost.ScopeRedirects(httpClient)
	response, err := httpClient.Do(request)
	if err != nil {
		return nil, nil, classifyTransport(fmt.Errorf("the batch request to %s failed: %s", hostOf(request.URL), transportError(err)), err)
	}
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(response.Body, c.limits.MaxBatchResponseBytes+1))
	if err != nil {
		return nil, nil, fmt.Errorf("read the batch response of %s: %s", hostOf(request.URL), transportError(err))
	}
	if int64(len(data)) > c.limits.MaxBatchResponseBytes {
		return nil, nil, fmt.Errorf("the batch response of %s is larger than %d bytes", hostOf(request.URL), c.limits.MaxBatchResponseBytes)
	}
	if response.StatusCode != http.StatusOK {
		message := serverMessage(data)
		for _, value := range header {
			if value = strings.TrimSpace(value); value != "" {
				message = strings.ReplaceAll(message, value, "[redacted]")
			}
		}
		return nil, nil, fmt.Errorf("the Git LFS server %s answered the batch request with HTTP %d%s", hostOf(request.URL), response.StatusCode, message)
	}
	parsed, err := parseBatchResponse(data, objects)
	return parsed, request.URL, err
}

// batchEndpoint returns the endpoint of the batch request and its headers:
// for an SSH remote, those of git-lfs-authenticate.
func (c *client) batchEndpoint(ctx context.Context) (string, map[string]string, error) {
	if c.endpoint.SSH == nil {
		return c.endpoint.URL, nil, nil
	}
	auth, err := authenticateSSH(ctx, *c.endpoint.SSH, c.endpoint.URL, c.ssh, c.guard)
	if err != nil {
		return "", nil, err
	}
	return auth.href, auth.header, nil
}

// dropHeadersOffOrigin removes the named headers from a redirected request
// that leaves the scheme, host, or port of the requests before it.
func dropHeadersOffOrigin(names []string) func(*http.Request, []*http.Request) error {
	return func(request *http.Request, via []*http.Request) error {
		if !credhost.SameOrigin(request, via) {
			for _, name := range names {
				request.Header.Del(name)
			}
		}
		if len(via) >= credhost.MaxRedirects {
			return fmt.Errorf("stopped after %d redirects", credhost.MaxRedirects)
		}
		return nil
	}
}

// parseBatchResponse reads the objects of a batch response. It keeps only
// objects that the request asked for, once each, with the requested size.
func parseBatchResponse(data []byte, requested []objectRef) (map[string]batchObject, error) {
	var parsed batchResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, errors.New("the batch response is not valid JSON")
	}
	if parsed.Transfer != "" && parsed.Transfer != "basic" {
		return nil, unsupportedf("the batch response asks for the transfer adapter %q; drydock supports only basic", sanitize(parsed.Transfer))
	}
	if parsed.HashAlgo != "" && parsed.HashAlgo != "sha256" {
		return nil, unsupportedf("the batch response uses the hash algorithm %q; drydock supports only sha256", sanitize(parsed.HashAlgo))
	}
	want := make(map[string]int64, len(requested))
	for _, object := range requested {
		want[object.OID] = object.Size
	}
	out := make(map[string]batchObject, len(parsed.Objects))
	for _, object := range parsed.Objects {
		size, ok := want[object.OID]
		if !ok || size != object.Size {
			continue
		}
		if _, seen := out[object.OID]; seen {
			continue
		}
		out[object.OID] = object
	}
	return out, nil
}

// applyAuth sets the Authorization header of the --git-* HTTP
// credentials: the bearer token when one is set, else basic auth.
func applyAuth(request *http.Request, credentials gitauth.Credentials) {
	if strings.TrimSpace(credentials.BearerToken) != "" {
		request.Header.Set("Authorization", "Bearer "+credentials.BearerToken)
		return
	}
	request.SetBasicAuth(credentials.Username, credentials.Password)
}

// serverMessage returns ": <message>" for the message of an error body,
// cut and cleaned, or "".
func serverMessage(data []byte) string {
	var parsed errorBody
	if json.Unmarshal(data, &parsed) != nil || strings.TrimSpace(parsed.Message) == "" {
		return ""
	}
	return ": " + sanitize(parsed.Message)
}

// maxMessageLength bounds the server text that a warning quotes.
const maxMessageLength = 200

// sanitize removes control characters from server text and cuts it, so a
// server cannot fill or forge the output.
func sanitize(text string) string {
	cleaned := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, strings.TrimSpace(text))
	if runes := []rune(cleaned); len(runes) > maxMessageLength {
		cleaned = string(runes[:maxMessageLength]) + "..."
	}
	return cleaned
}
