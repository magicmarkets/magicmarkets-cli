package mcpserver

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"magicmarkets-cli/internal/config"
	"magicmarkets-cli/internal/magicmarkets"
)

// tradingTools are the tools that can move money. They must never be reachable
// unless trading was explicitly enabled.
var tradingTools = []string{
	"create_betslip",
	"place_order",
	"close_order",
	"close_all_orders",
	"create_heartbeat",
	"refresh_heartbeat",
	"cancel_heartbeat",
	"list_heartbeats",
}

// readOnlyTools must always be available.
var readOnlyTools = []string{
	"get_balance",
	"get_exchange_rates",
	"get_position",
	"validate_bet_type",
	"snap_price",
	"list_events",
	"list_event_offers",
	"list_orders",
	"get_order",
	"list_betslips",
	"get_betslip",
}

// newTestServer registers the tool set onto a throwaway MCP server and lists
// what a client would see.
func newTestServer(t *testing.T, allowTrading bool) map[string]*mcp.Tool {
	t.Helper()

	cfg := &config.Config{
		APIKey:  "test-key",
		APIURL:  "https://example.invalid/v2",
		WSURL:   "wss://example.invalid/v2/stream",
		Lang:    "en",
		Timeout: time.Second,
	}
	client := magicmarkets.New(cfg.APIURL, cfg.APIKey, cfg.Timeout)

	s := New(client, cfg, Options{AllowTrading: allowTrading, Version: "test"})
	listed, err := listRegisteredTools(s.newMCP())
	if err != nil {
		t.Fatalf("list registered tools: %v", err)
	}
	tools := make(map[string]*mcp.Tool, len(listed))
	for _, tool := range listed {
		tools[tool.Name] = tool
	}
	return tools
}

func TestReadOnlyByDefault(t *testing.T) {
	tools := newTestServer(t, false)

	for _, name := range readOnlyTools {
		if _, ok := tools[name]; !ok {
			t.Errorf("read-only tool %q should always be registered", name)
		}
	}

	for _, name := range tradingTools {
		if _, ok := tools[name]; ok {
			t.Errorf("trading tool %q must NOT be registered without AllowTrading", name)
		}
	}
}

func TestTradingToolsRequireOptIn(t *testing.T) {
	tools := newTestServer(t, true)

	for _, name := range append(append([]string{}, readOnlyTools...), tradingTools...) {
		if _, ok := tools[name]; !ok {
			t.Errorf("tool %q should be registered with AllowTrading", name)
		}
	}
}

func TestEveryToolHasObjectOutputSchema(t *testing.T) {
	// Typed Out values give clients a stable schema. A missing or non-object
	// output schema is how a top-level array slipped through for xrates.
	tools := newTestServer(t, true)
	for _, name := range append(append([]string{}, readOnlyTools...), tradingTools...) {
		tool, ok := tools[name]
		if !ok {
			t.Fatalf("tool %q not registered", name)
		}
		if tool.OutputSchema == nil {
			t.Errorf("tool %q has no output schema", name)
			continue
		}
		raw, err := json.Marshal(tool.OutputSchema)
		if err != nil {
			t.Errorf("tool %q: marshal output schema: %v", name, err)
			continue
		}
		var schema map[string]any
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Errorf("tool %q output schema = %s, want a JSON object", name, raw)
			continue
		}
		if schema["type"] != "object" {
			t.Errorf("tool %q output schema type = %v, want object (%s)", name, schema["type"], raw)
		}
	}
}

func TestEveryToolHasADescription(t *testing.T) {
	// A tool with no description is unusable by an agent.
	tools := newTestServer(t, true)
	if len(tools) == 0 {
		t.Fatal("no tools registered")
	}

	names := make([]string, 0, len(tools))
	for name := range tools {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		if tools[name].Description == "" {
			t.Errorf("tool %q has no description", name)
		}
	}
}

func TestMoneySpendingToolsAreMarkedDestructive(t *testing.T) {
	// The annotations are what an MCP client uses to decide whether to prompt,
	// so the ones that cancel or place bets must not claim to be read-only.
	tools := newTestServer(t, true)

	for _, name := range []string{"place_order", "close_order", "close_all_orders"} {
		tool, ok := tools[name]
		if !ok {
			t.Fatalf("tool %q not registered", name)
		}
		if tool.Annotations == nil {
			t.Fatalf("tool %q has no annotations", name)
		}
		if tool.Annotations.ReadOnlyHint {
			t.Errorf("tool %q is annotated read-only but it changes state", name)
		}
		if tool.Annotations.DestructiveHint == nil || !*tool.Annotations.DestructiveHint {
			t.Errorf("tool %q should carry a destructive hint", name)
		}
	}
}

// newTestHTTPServer wires a throwaway Server the same way newTestServer does,
// for exercising the HTTP transport rather than the tool registration.
func newTestHTTPServer(t *testing.T) *Server {
	t.Helper()
	cfg := &config.Config{
		APIURL:  "https://example.invalid/v2",
		WSURL:   "wss://example.invalid/v2/stream",
		Lang:    "en",
		Timeout: time.Second,
	}
	return New(nil, cfg, Options{Version: "test"})
}

func TestHTTPProbesDoNotRequireAPIKey(t *testing.T) {
	s := newTestHTTPServer(t)
	ts := httptest.NewServer(s.httpHandler())
	t.Cleanup(ts.Close)

	for _, path := range []string{"/health", "/live"} {
		t.Run(path, func(t *testing.T) {
			resp, err := http.Get(ts.URL + path)
			if err != nil {
				t.Fatalf("GET %s: %v", path, err)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("read body: %v", err)
			}
			if resp.StatusCode != http.StatusOK || string(body) != "ok\n" {
				t.Errorf("GET %s = %d %q, want 200 ok\\n", path, resp.StatusCode, body)
			}
		})
	}
}

func TestHTTPRequiresCredential(t *testing.T) {
	s := newTestHTTPServer(t)
	ts := httptest.NewServer(s.httpHandler())
	defer ts.Close()

	body := `{"jsonrpc": "2.0", "id": 1, "method":"initialize", "params": {"protocolVersion":"2025-06-18"}}`

	cases := []struct {
		name   string
		header http.Header
		want   int
	}{
		{"no credential", nil, http.StatusUnauthorized},
		{"api key", http.Header{magicmarkets.APIKeyHeader: []string{"caller-key"}}, http.StatusOK},
		{"bearer", http.Header{"Authorization": []string{"Bearer oauth-tok"}}, http.StatusOK},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, ts.URL+HTTPPath, strings.NewReader(body))
			if err != nil {
				t.Fatalf("build request: %v", err)
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Accept", "application/json, text/event-stream")
			for k, vals := range c.header {
				for _, v := range vals {
					req.Header.Set(k, v)
				}
			}

			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatalf("do request: %v", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode != c.want {
				t.Errorf("status = %d, want %d", resp.StatusCode, c.want)
			}
			if c.want == http.StatusUnauthorized {
				if wa := resp.Header.Get("WWW-Authenticate"); !strings.Contains(wa, "resource_metadata=") {
					t.Errorf("WWW-Authenticate = %q, want resource_metadata for OAuth discovery", wa)
				}
			}
			if c.want == http.StatusOK {
				ct := resp.Header.Get("Content-Type")
				if !strings.HasPrefix(ct, "application/json") {
					t.Errorf("Content-Type = %q, want application/json", ct)
				}
			}
		})
	}
}

func TestHTTPRejectsGETWithoutASession(t *testing.T) {
	// Stateless streamable HTTP has no GET SSE stream. Clients must treat
	// 405 as "no standalone stream", not a hard failure — the SDK client does.
	s := newTestHTTPServer(t)
	ts := httptest.NewServer(s.httpHandler())
	t.Cleanup(ts.Close)

	req, err := http.NewRequest(http.MethodGet, ts.URL+HTTPPath, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set(magicmarkets.APIKeyHeader, "caller-key")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET /mcp = %d, want 405", resp.StatusCode)
	}
}

// apiKeyTransport injects X-Api-Key on every request, the same way a client's
// MCP config headers map does.
type apiKeyTransport struct {
	base http.RoundTripper
	key  string
}

func (t apiKeyTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set(magicmarkets.APIKeyHeader, t.key)
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}

func TestHTTPToolCallDoesNotNeedStickySessions(t *testing.T) {
	// Hosted MCP runs two replicas with no session affinity. A transport
	// session created on replica A is invisible on replica B. Stateless mode
	// must let initialize land on one handler and tools/call on the other.
	s := newTestHTTPServer(t)
	a := s.httpHandler()
	b := s.httpHandler()
	var n atomic.Int32
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1)%2 == 1 {
			a.ServeHTTP(w, r)
			return
		}
		b.ServeHTTP(w, r)
	})
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)

	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "mcpserver-test", Version: "test"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   ts.URL + HTTPPath,
		HTTPClient: &http.Client{Transport: apiKeyTransport{key: "caller-key"}},
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "snap_price",
		Arguments: map[string]any{"price": 2.11, "direction": "for"},
	})
	if err != nil {
		t.Fatalf("snap_price: %v", err)
	}
	if res.IsError {
		t.Fatalf("snap_price returned an error result: %+v", res)
	}

	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode structured content: %v", err)
	}
	snapped, ok := got["snapped"].(float64)
	if !ok {
		t.Fatalf("structured content = %s, missing snapped", raw)
	}
	if snapped != 2.10 {
		t.Errorf("snapped = %v, want 2.10", snapped)
	}
	if n.Load() < 2 {
		t.Fatalf("handler was hit %d time(s); need at least two replicas involved", n.Load())
	}
}

// connectHTTPSession starts an MCP HTTP server in front of a stub Magic
// Markets API and returns an SDK client session.
func connectHTTPSession(t *testing.T, api http.HandlerFunc) *mcp.ClientSession {
	t.Helper()
	return connectHTTPSessionWithTransport(t, api, apiKeyTransport{key: "caller-key"})
}

func connectHTTPSessionWithTransport(t *testing.T, api http.HandlerFunc, rt http.RoundTripper) *mcp.ClientSession {
	t.Helper()
	upstream := httptest.NewServer(api)
	t.Cleanup(upstream.Close)

	cfg := &config.Config{
		APIURL:  upstream.URL,
		WSURL:   "wss://example.invalid/v2/stream",
		Lang:    "en",
		Timeout: time.Second,
	}
	s := New(nil, cfg, Options{Version: "test"})
	ts := httptest.NewServer(s.httpHandler())
	t.Cleanup(ts.Close)

	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "mcpserver-test", Version: "test"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   ts.URL + HTTPPath,
		HTTPClient: &http.Client{Transport: rt},
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func toolText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	var b strings.Builder
	for _, c := range res.Content {
		tc, ok := c.(*mcp.TextContent)
		if !ok {
			t.Fatalf("content item type %T, want TextContent", c)
		}
		b.WriteString(tc.Text)
	}
	return b.String()
}

func structuredObject(t *testing.T, res *mcp.CallToolResult) map[string]any {
	t.Helper()
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatalf("marshal structured content: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("structured content = %s, want a JSON object so MCP clients can display it", raw)
	}
	return got
}

func TestHTTPProtectedResourceMetadata(t *testing.T) {
	s := New(nil, &config.Config{
		APIURL:      "https://example.invalid/v2",
		OAuthIssuer: "https://magicmarkets.com/api/auth",
	}, Options{Version: "test", PublicURL: "https://mcp.example.test"})
	ts := httptest.NewServer(s.httpHandler())
	t.Cleanup(ts.Close)

	resp, err := http.Get(ts.URL + "/.well-known/oauth-protected-resource")
	if err != nil {
		t.Fatalf("GET metadata: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	if got["resource"] != "https://mcp.example.test/mcp" {
		t.Errorf("resource = %v, want https://mcp.example.test/mcp", got["resource"])
	}
	servers, _ := got["authorization_servers"].([]any)
	if len(servers) != 1 || servers[0] != "https://mcp.example.test" {
		t.Errorf("authorization_servers = %v, want this MCP host", got["authorization_servers"])
	}
}

type bearerTransport struct {
	base  http.RoundTripper
	token string
}

func (t bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Set(magicmarkets.AuthorizationHeader, "Bearer "+t.token)
	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(req)
}

// fakeAccessToken builds a JWT-shaped string carrying only a "sub" claim in its (unsigned)
// payload — enough for magicmarkets.MeResolver to read a subject from it locally. The
// resolver never verifies the signature client-side; that happens server-side, in the
// firebase-token exchange call it makes.
func fakeAccessToken(sub string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"` + sub + `"}`))
	return header + "." + payload + ".sig"
}

type hostRedirectTransport struct {
	host   string
	target *url.URL
}

// redirectHost builds a RoundTripper that sends requests to host to target instead, leaving
// every other request untouched. Google's Identity Toolkit endpoint
// (identitytoolkit.googleapis.com) is a hardcoded const in internal/magicmarkets/meauth.go, not
// a config knob — this is how a test stands in for it without any override hook in production
// code.
func redirectHost(t *testing.T, host, target string) http.RoundTripper {
	t.Helper()
	u, err := url.Parse(target)
	if err != nil {
		t.Fatalf("redirectHost: parse target %q: %v", target, err)
	}
	return hostRedirectTransport{host: host, target: u}
}

func (t hostRedirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host == t.host {
		req = req.Clone(req.Context())
		req.URL.Scheme = t.target.Scheme
		req.URL.Host = t.target.Host
	}
	return http.DefaultTransport.RoundTrip(req)
}

// TestHTTPGetExchangeRatesResolvesBearerViaFirebaseTokenExchange checks the
// OAuth leg end to end: a caller's Bearer token must never reach the v2 API
// as-is — it's exchanged via POST {OAuthIssuer}/oauth2/firebase-token first,
// and the magic-metadata-jwt/session pair that comes back is what actually
// goes out.
func TestHTTPGetExchangeRatesResolvesBearerViaFirebaseTokenExchange(t *testing.T) {
	accessToken := fakeAccessToken("user-uuid")
	var meCalls int
	var gotJWT, gotSession, gotKey string
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth2/firebase-token", func(w http.ResponseWriter, r *http.Request) {
		meCalls++
		if got := r.Header.Get(magicmarkets.AuthorizationHeader); got != "Bearer "+accessToken {
			t.Errorf("firebase-token exchange Authorization = %q, want Bearer %s", got, accessToken)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"firebase_token":"me-custom-token"}`))
	})
	mux.HandleFunc("/v1/accounts:signInWithCustomToken", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"idToken":"me-jwt"}`))
	})
	mux.HandleFunc("/xrates/", func(w http.ResponseWriter, r *http.Request) {
		gotJWT = r.Header.Get(magicmarkets.MagicMetadataJWTHeader)
		gotSession = r.Header.Get(magicmarkets.SessionHeader)
		gotKey = r.Header.Get(magicmarkets.APIKeyHeader)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","data":[{"ccy":"EUR","rate":1.08}]}`))
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	cfg := &config.Config{
		APIURL:            ts.URL,
		WSURL:             "wss://example.invalid/v2/stream",
		Lang:              "en",
		Timeout:           time.Second,
		OAuthIssuer:       ts.URL,
		SessionGroupID:    "7",
		FirebaseWebAPIKey: "web-api-key",
	}
	s := New(nil, cfg, Options{Version: "test"})
	// identityToolkitSignInURL (internal/magicmarkets/meauth.go) is a hardcoded const, not a
	// config knob — redirect it at the transport level instead, same package, test-only.
	s.httpClient = &http.Client{Transport: redirectHost(t, "identitytoolkit.googleapis.com", ts.URL)}
	mcpTS := httptest.NewServer(s.httpHandler())
	t.Cleanup(mcpTS.Close)

	ctx := context.Background()
	client := mcp.NewClient(&mcp.Implementation{Name: "mcpserver-test", Version: "test"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{
		Endpoint:   mcpTS.URL + HTTPPath,
		HTTPClient: &http.Client{Transport: bearerTransport{token: accessToken}},
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })

	res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "get_exchange_rates"})
	if err != nil {
		t.Fatalf("get_exchange_rates protocol error: %v", err)
	}
	if res.IsError {
		t.Fatalf("get_exchange_rates isError: %s", toolText(t, res))
	}
	if gotJWT != "me-jwt" {
		t.Errorf("upstream magic-metadata-jwt = %q, want me-jwt", gotJWT)
	}
	if gotSession != "m-7-user-uuid" {
		t.Errorf("upstream session = %q, want m-7-user-uuid", gotSession)
	}
	if gotKey != "" {
		t.Errorf("upstream X-Api-Key = %q, want empty", gotKey)
	}
	if meCalls != 1 {
		t.Errorf("firebase-token exchange was called %d times, want 1", meCalls)
	}

	// A second call on the same access token within MeCacheTTL must reuse
	// the cached credential rather than repeating the exchange.
	res, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "get_exchange_rates"})
	if err != nil {
		t.Fatalf("get_exchange_rates (2nd) protocol error: %v", err)
	}
	if res.IsError {
		t.Fatalf("get_exchange_rates (2nd) isError: %s", toolText(t, res))
	}
	if meCalls != 1 {
		t.Errorf("firebase-token exchange was called %d times across two tool calls, want 1 (cache should have hit)", meCalls)
	}
}

func TestHTTPGetExchangeRatesReturnsObjectForClients(t *testing.T) {
	// Wrap rates in an object like every other list tool so structured
	// content stays object-rooted.
	session := connectHTTPSession(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/xrates/" {
			t.Errorf("path = %q, want /xrates/", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","data":[{"ccy":"EUR","rate":1.08}]}`))
	})

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_exchange_rates"})
	if err != nil {
		t.Fatalf("get_exchange_rates protocol error: %v", err)
	}
	if res.IsError {
		t.Fatalf("get_exchange_rates isError: %s", toolText(t, res))
	}

	got := structuredObject(t, res)
	rates, ok := got["rates"].([]any)
	if !ok {
		t.Fatalf("structured content = %#v, missing rates array", got)
	}
	if len(rates) != 1 {
		t.Fatalf("rates count = %d, want 1", len(rates))
	}
}

func TestHTTPGetExchangeRatesSurfacesAPIErrorToClient(t *testing.T) {
	// Tool failures must arrive as a CallToolResult with isError and the
	// message in content, not as a protocol-level JSON-RPC error.
	session := connectHTTPSession(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"status":"error","code":"auth_error","data":"invalid API key"}`))
	})

	res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "get_exchange_rates"})
	if err != nil {
		t.Fatalf("get_exchange_rates protocol error %v; want an isError tool result the client can read", err)
	}
	if !res.IsError {
		t.Fatal("get_exchange_rates succeeded; want isError so the client sees the API failure")
	}
	text := toolText(t, res)
	if !strings.Contains(text, "auth_error") {
		t.Errorf("error text = %q, want it to name the API error code", text)
	}
}

func TestHTTPInitializeDoesNotIssueSessionID(t *testing.T) {
	s := newTestHTTPServer(t)
	ts := httptest.NewServer(s.httpHandler())
	t.Cleanup(ts.Close)

	body := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"test","version":"0"}}}`
	req, err := http.NewRequest(http.MethodPost, ts.URL+HTTPPath, bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set(magicmarkets.APIKeyHeader, "caller-key")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Mcp-Session-Id"); got != "" {
		t.Errorf("Mcp-Session-Id = %q, want empty in stateless mode", got)
	}
}

func TestToolNamesMatchesRegistration(t *testing.T) {
	cfg := &config.Config{
		APIKey:  "test-key",
		APIURL:  "https://example.invalid/v2",
		WSURL:   "wss://example.invalid/v2/stream",
		Lang:    "en",
		Timeout: time.Second,
	}
	s := New(magicmarkets.New(cfg.APIURL, cfg.APIKey, cfg.Timeout), cfg, Options{Version: "test"})
	got := s.ToolNames()
	if len(got) != len(readOnlyTools) {
		t.Fatalf("ToolNames() = %v, want %d read-only tools", got, len(readOnlyTools))
	}
}
