package magicmarkets

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
)

// Client is a Magic Markets v2 REST client.
//
// Every request carries either an API key in [APIKeyHeader], applied
// unchanged, or a magic-metadata-jwt/session pair resolved from an OAuth
// Bearer credential via [MeResolver] — see [Credential.ApplyTo]. There is no
// request signing. The WebSocket stream uses the same credential — see
// [Client.DialStream].
type Client struct {
	baseURL    string
	cred       Credential
	meResolver *MeResolver
	httpClient *http.Client
	userAgent  string
	basicAuth  string

	// maxRetries bounds automatic retries of throttled (429) requests.
	maxRetries int

	// Trace, when non-nil, is called with a one-line summary of each request.
	// Used by the --verbose flag.
	Trace func(format string, args ...any)
}

// APIKeyHeader is the HTTP header the public v2 API uses for authentication.
const APIKeyHeader = "X-Api-Key"

// Option customises a Client.
type Option func(*Client)

// WithHTTPClient replaces the underlying HTTP client.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) { c.httpClient = hc }
}

// WithUserAgent sets the User-Agent header.
func WithUserAgent(ua string) Option {
	return func(c *Client) { c.userAgent = ua }
}

// WithMaxRetries sets how many times a 429 is retried. Zero disables retrying.
func WithMaxRetries(n int) Option {
	return func(c *Client) { c.maxRetries = n }
}

// WithTrace installs a request tracer.
func WithTrace(fn func(format string, args ...any)) Option {
	return func(c *Client) { c.Trace = fn }
}

// WithBasicAuth sets an additional Authorization: Basic header, sent
// alongside whatever [Credential] header(s) the request already carries.
// Some non-production environments sit behind an infra-level HTTP Basic Auth
// wall in front of the v2 API; production has no such wall. value is the
// already base64-encoded "user:pass" token (i.e. what follows "Basic " on
// the wire) — pass it empty (the default) to send nothing.
func WithBasicAuth(value string) Option {
	return func(c *Client) { c.basicAuth = strings.TrimSpace(value) }
}

// WithMeResolver installs the resolver used to turn a Bearer credential into
// the magic-metadata-jwt/session pair the v2 API and the stream require —
// see [MeResolver]. Required whenever the client may be given a Bearer
// credential; unused for an API key credential.
func WithMeResolver(resolver *MeResolver) Option {
	return func(c *Client) { c.meResolver = resolver }
}

// New creates a client for the given base URL (including the /v2 suffix),
// authenticated with a static API key.
func New(baseURL, apiKey string, timeout time.Duration, opts ...Option) *Client {
	return NewWithCredential(baseURL, Credential{APIKey: apiKey}, timeout, opts...)
}

// NewWithCredential creates a client authenticated with either an API key or
// an OAuth Bearer token. The same credential is sent on REST and on the stream.
func NewWithCredential(baseURL string, cred Credential, timeout time.Duration, opts ...Option) *Client {
	c := &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		cred:       cred,
		httpClient: &http.Client{Timeout: timeout},
		userAgent:  "magicmarkets-cli",
		maxRetries: 2,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// BaseURL returns the configured REST base.
func (c *Client) BaseURL() string { return c.baseURL }

// envelope is the response wrapper shared by every JSON endpoint:
// {"status": "ok", "data": ...}.
type envelope struct {
	Status string          `json:"status"`
	Data   json.RawMessage `json:"data"`
}

// do performs a request and decodes the envelope's data field into out.
//
// A throttled (429) response is retried up to maxRetries times, honouring the
// server's requested wait. out may be nil to discard the body.
func (c *Client) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	var payload []byte
	if body != nil {
		var err error
		payload, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode request body: %w", err)
		}
	}

	endpoint := c.baseURL + path
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}

	for attempt := 0; ; attempt++ {
		if c.Trace != nil {
			c.Trace("%s %s", method, endpoint)
		}

		data, err := c.attempt(ctx, method, endpoint, payload)
		if err == nil {
			if out == nil {
				return nil
			}
			if err := json.Unmarshal(data, out); err != nil {
				return fmt.Errorf("decode %s %s response: %w", method, path, err)
			}
			return nil
		}
		// Only throttling is safely retryable: it means the request was
		// rejected outright, so no order or betslip was created.
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.Code != CodeThrottled || attempt >= c.maxRetries {
			return err
		}

		wait := time.Duration(apiErr.RetryAfter) * time.Second
		if wait <= 0 {
			wait = time.Duration(attempt+1) * time.Second
		}
		if c.Trace != nil {
			c.Trace("throttled, retrying in %s (attempt %d/%d)", wait, attempt+1, c.maxRetries)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
	}
}

// attempt performs a single HTTP round trip and returns the envelope's data.
func (c *Client) attempt(ctx context.Context, method, endpoint string, payload []byte) (json.RawMessage, error) {
	var reader io.Reader
	if payload != nil {
		reader = bytes.NewReader(payload)
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	if err := c.cred.ApplyTo(ctx, req.Header, c.meResolver); err != nil {
		return nil, err
	}
	if c.basicAuth != "" {
		req.Header.Set(AuthorizationHeader, "Basic "+c.basicAuth)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := NoRedirectClient(c.httpClient).Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, endpoint, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		retryAfter, _ := strconv.Atoi(resp.Header.Get("Retry-After"))
		return nil, parseAPIError(resp.StatusCode, retryAfter, raw)
	}

	// 204 and other empty successes have no envelope.
	if len(bytes.TrimSpace(raw)) == 0 {
		return json.RawMessage("null"), nil
	}

	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("decode response envelope: %w (body: %s)", err, truncate(string(raw), 200))
	}
	if env.Status != "ok" {
		// A non-ok envelope with a 2xx status shouldn't happen, but surface it
		// rather than silently decoding a nil data field.
		return nil, parseAPIError(resp.StatusCode, 0, raw)
	}
	if len(env.Data) == 0 {
		return json.RawMessage("null"), nil
	}
	return env.Data, nil
}

// get issues a GET and decodes data into out.
func (c *Client) get(ctx context.Context, path string, query url.Values, out any) error {
	return c.do(ctx, http.MethodGet, path, query, nil, out)
}

// post issues a POST and decodes data into out.
func (c *Client) post(ctx context.Context, path string, body, out any) error {
	return c.do(ctx, http.MethodPost, path, nil, body, out)
}

// delete issues a DELETE and decodes data into out.
func (c *Client) delete(ctx context.Context, path string, out any) error {
	return c.do(ctx, http.MethodDelete, path, nil, nil, out)
}
