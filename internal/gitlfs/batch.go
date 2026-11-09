package gitlfs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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

// batch asks the batch API at endpoint for the download actions of
// objects. It sends the HTTP credentials only when the scheme, host, and
// port of the batch URL match a --git-auth-host entry, and it removes them
// from a redirect that leaves that origin.
func (c *client) batch(ctx context.Context, endpoint string, objects []objectRef) (map[string]batchObject, error) {
	body, err := json.Marshal(batchRequest{Operation: "download", Transfers: []string{"basic"}, Objects: objects, HashAlgo: "sha256"})
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, c.limits.BatchTimeout)
	defer cancel()
	batchURL := BatchURL(endpoint)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, batchURL, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("the Git LFS endpoint is not a valid URL")
	}
	request.Header.Set("Accept", mediaType)
	request.Header.Set("Content-Type", mediaType)
	httpClient := &http.Client{Transport: c.transport}
	if c.credentials.AuthorizesHTTP(batchURL) {
		applyAuth(request, c.credentials)
		httpClient = credhost.ScopeRedirects(httpClient)
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("the batch request to %s failed: %s", hostOf(request.URL), transportError(err))
	}
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(response.Body, c.limits.MaxBatchResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read the batch response of %s: %s", hostOf(request.URL), transportError(err))
	}
	if int64(len(data)) > c.limits.MaxBatchResponseBytes {
		return nil, fmt.Errorf("the batch response of %s is larger than %d bytes", hostOf(request.URL), c.limits.MaxBatchResponseBytes)
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the Git LFS server %s answered the batch request with HTTP %d%s", hostOf(request.URL), response.StatusCode, serverMessage(data))
	}
	return parseBatchResponse(data, objects)
}

// parseBatchResponse reads the objects of a batch response. It keeps only
// objects that the request asked for, once each, with the requested size.
func parseBatchResponse(data []byte, requested []objectRef) (map[string]batchObject, error) {
	var parsed batchResponse
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, errors.New("the batch response is not valid JSON")
	}
	if parsed.Transfer != "" && parsed.Transfer != "basic" {
		return nil, fmt.Errorf("the batch response asks for the transfer adapter %q; drydock supports only basic", sanitize(parsed.Transfer))
	}
	if parsed.HashAlgo != "" && parsed.HashAlgo != "sha256" {
		return nil, fmt.Errorf("the batch response uses the hash algorithm %q; drydock supports only sha256", sanitize(parsed.HashAlgo))
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
