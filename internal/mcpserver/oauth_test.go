package mcpserver

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"magicmarkets-cli/internal/config"
)

func TestHTTPProtectedResourceNamesThisServerAsAS(t *testing.T) {
	s := New(nil, &config.Config{
		APIURL:      "https://example.invalid/v2",
		OAuthIssuer: "https://magicmarkets.com/api/auth",
	}, Options{Version: "test", PublicURL: "https://magicmarkets-mcp.dev-eu.kubershmuber.com"})
	ts := httptest.NewServer(s.httpHandler())
	t.Cleanup(ts.Close)

	for _, path := range []string{
		"/.well-known/oauth-protected-resource",
		"/.well-known/oauth-protected-resource/mcp",
	} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s = %d %s", path, resp.StatusCode, body)
		}
		var got map[string]any
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("GET %s decode: %v", path, err)
		}
		if got["resource"] != "https://magicmarkets-mcp.dev-eu.kubershmuber.com/mcp" {
			t.Errorf("%s resource = %v", path, got["resource"])
		}
		servers, _ := got["authorization_servers"].([]any)
		if len(servers) != 1 || servers[0] != "https://magicmarkets-mcp.dev-eu.kubershmuber.com" {
			t.Errorf("%s authorization_servers = %v, want this MCP host so Claude DCR hits /register here, not magicmarkets.com",
				path, got["authorization_servers"])
		}
	}
}

func TestHTTPAuthorizationServerMetadataAdvertisesDCR(t *testing.T) {
	s := New(nil, &config.Config{
		APIURL:      "https://example.invalid/v2",
		OAuthIssuer: "https://magicmarkets.com/api/auth",
	}, Options{Version: "test", PublicURL: "https://mcp.example.test"})
	ts := httptest.NewServer(s.httpHandler())
	t.Cleanup(ts.Close)

	for _, path := range []string{
		"/.well-known/oauth-authorization-server",
		"/.well-known/openid-configuration",
		"/mcp/.well-known/oauth-authorization-server",
	} {
		resp, err := http.Get(ts.URL + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s = %d %s", path, resp.StatusCode, body)
		}
		var got map[string]any
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("GET %s decode: %v", path, err)
		}
		if got["issuer"] != "https://mcp.example.test" {
			t.Errorf("%s issuer = %v", path, got["issuer"])
		}
		if got["registration_endpoint"] != "https://mcp.example.test/register" {
			t.Errorf("%s registration_endpoint = %v", path, got["registration_endpoint"])
		}
		if got["authorization_endpoint"] != "https://mcp.example.test/authorize" {
			t.Errorf("%s authorization_endpoint = %v", path, got["authorization_endpoint"])
		}
		if got["token_endpoint"] != "https://mcp.example.test/token" {
			t.Errorf("%s token_endpoint = %v", path, got["token_endpoint"])
		}
	}
}

func TestHTTPRegisterJSONCreatesPublicClient(t *testing.T) {
	s := newTestHTTPServer(t)
	ts := httptest.NewServer(s.httpHandler())
	t.Cleanup(ts.Close)

	for _, path := range []string{"/register", "/mcp/register"} {
		resp, err := http.Post(ts.URL+path, "application/json",
			strings.NewReader(`{"client_name":"claude","redirect_uris":["http://127.0.0.1:54321/callback"]}`))
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("POST %s = %d %s, want 201 JSON (not HTML 404)", path, resp.StatusCode, body)
		}
		if strings.Contains(string(body), "<html") {
			t.Fatalf("POST %s returned HTML, want RFC 7591 JSON", path)
		}
		var got map[string]any
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatalf("POST %s decode: %v", path, err)
		}
		id, _ := got["client_id"].(string)
		if id == "" {
			t.Errorf("POST %s missing client_id: %s", path, body)
		}
		if got["token_endpoint_auth_method"] != "none" {
			t.Errorf("POST %s token_endpoint_auth_method = %v", path, got["token_endpoint_auth_method"])
		}
	}
}

func TestHTTPRegisterRejectsMissingRedirectURIs(t *testing.T) {
	s := newTestHTTPServer(t)
	ts := httptest.NewServer(s.httpHandler())
	t.Cleanup(ts.Close)

	resp, err := http.Post(ts.URL+"/register", "application/json", strings.NewReader(`{"client_name":"claude"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

// registerClient is a small test helper that performs DCR and returns the
// client_id.
func registerClient(t *testing.T, baseURL string, redirectURIs ...string) string {
	t.Helper()
	body, err := json.Marshal(map[string]any{"redirect_uris": redirectURIs})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.Post(baseURL+"/register", "application/json", strings.NewReader(string(body)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var created map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	id, _ := created["client_id"].(string)
	if id == "" {
		t.Fatalf("register: no client_id in %v", created)
	}
	return id
}

func noRedirectClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func TestHTTPAuthorizeNeverForwardsDownstreamRedirectURIUpstream(t *testing.T) {
	s := New(nil, &config.Config{
		APIURL:        "https://example.invalid/v2",
		OAuthIssuer:   "https://magicmarkets.com/api/auth",
		OAuthClientID: "mm-public",
	}, Options{Version: "test", PublicURL: "https://mcp.example.test"})
	ts := httptest.NewServer(s.httpHandler())
	t.Cleanup(ts.Close)

	// A downstream redirect_uri the real Magic Markets AS would reject
	// outright (it only allowlists this process's own /mcp/callback).
	clientID := registerClient(t, ts.URL, "https://claude.ai/api/mcp/auth_callback")

	resp, err := noRedirectClient().Get(ts.URL + "/authorize?" + url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {"https://claude.ai/api/mcp/auth_callback"},
		"state":                 {"downstream-state"},
		"code_challenge":        {"downstream-challenge"},
		"code_challenge_method": {"S256"},
		"scope":                 {"mcp"},
	}.Encode())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d %s, want 302 to Magic Markets authorize", resp.StatusCode, body)
	}
	loc, err := resp.Location()
	if err != nil {
		t.Fatal(err)
	}
	if loc.Scheme+"://"+loc.Host+loc.Path != "https://magicmarkets.com/api/auth/oauth2/authorize" {
		t.Errorf("Location = %s", loc)
	}
	q := loc.Query()
	if q.Get("client_id") != "mm-public" {
		t.Errorf("upstream client_id = %q, want MAGICMARKETS_OAUTH_CLIENT_ID", q.Get("client_id"))
	}
	if got := q.Get("redirect_uri"); got != "https://mcp.example.test/mcp/callback" {
		t.Errorf("upstream redirect_uri = %q, want this process's own fixed callback, never the downstream client's", got)
	}
	if q.Get("code_challenge") == "downstream-challenge" {
		t.Errorf("upstream code_challenge must be this process's own PKCE pair, not the downstream client's")
	}
	if q.Get("state") == "downstream-state" {
		t.Errorf("upstream state must be a sealed token, not the downstream client's opaque state")
	}
}

func TestHTTPAuthorizeRejectsUnknownClient(t *testing.T) {
	s := newTestHTTPServer(t)
	ts := httptest.NewServer(s.httpHandler())
	t.Cleanup(ts.Close)

	resp, err := noRedirectClient().Get(ts.URL + "/authorize?" + url.Values{
		"client_id":             {"not-a-real-client-id"},
		"redirect_uri":          {"http://127.0.0.1:9/cb"},
		"code_challenge":        {"c"},
		"code_challenge_method": {"S256"},
	}.Encode())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestHTTPAuthorizeRejectsUnregisteredRedirectURI(t *testing.T) {
	s := newTestHTTPServer(t)
	ts := httptest.NewServer(s.httpHandler())
	t.Cleanup(ts.Close)

	clientID := registerClient(t, ts.URL, "http://127.0.0.1:9/cb")

	resp, err := noRedirectClient().Get(ts.URL + "/authorize?" + url.Values{
		"client_id":             {clientID},
		"redirect_uri":          {"http://127.0.0.1:9/some-other-path"},
		"code_challenge":        {"c"},
		"code_challenge_method": {"S256"},
	}.Encode())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestHTTPAuthorizeRequiresPKCE(t *testing.T) {
	s := newTestHTTPServer(t)
	ts := httptest.NewServer(s.httpHandler())
	t.Cleanup(ts.Close)

	clientID := registerClient(t, ts.URL, "http://127.0.0.1:9/cb")

	resp, err := noRedirectClient().Get(ts.URL + "/authorize?" + url.Values{
		"client_id":    {clientID},
		"redirect_uri": {"http://127.0.0.1:9/cb"},
	}.Encode())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (code_challenge required)", resp.StatusCode)
	}
}

// upstreamAS is a fake Magic Markets Authorization Server for exercising the
// full authorize -> callback -> token round trip.
type upstreamAS struct {
	*httptest.Server
	gotAuthorizeRedirectURI string
	gotTokenForm            url.Values
}

func newUpstreamAS(t *testing.T, code string, tokens map[string]any) *upstreamAS {
	t.Helper()
	up := &upstreamAS{}
	mux := http.NewServeMux()
	mux.HandleFunc("/oauth2/authorize", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		up.gotAuthorizeRedirectURI = q.Get("redirect_uri")
		dest, _ := url.Parse(q.Get("redirect_uri"))
		out := dest.Query()
		out.Set("code", code)
		out.Set("state", q.Get("state"))
		dest.RawQuery = out.Encode()
		http.Redirect(w, r, dest.String(), http.StatusFound)
	})
	mux.HandleFunc("/oauth2/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		up.gotTokenForm = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(tokens)
	})
	up.Server = httptest.NewServer(mux)
	t.Cleanup(up.Close)
	return up
}

func TestHTTPFullAuthorizeCallbackTokenRoundTrip(t *testing.T) {
	up := newUpstreamAS(t, "upstream-code", map[string]any{
		"access_token":  "mm-access",
		"refresh_token": "mm-refresh",
		"token_type":    "Bearer",
		"expires_in":    3600,
		"scope":         "mcp",
	})

	s := New(nil, &config.Config{
		APIURL:        "https://example.invalid/v2",
		OAuthIssuer:   up.URL,
		OAuthClientID: "mm-public",
	}, Options{Version: "test", PublicURL: "https://mcp.example.test"})
	ts := httptest.NewServer(s.httpHandler())
	t.Cleanup(ts.Close)

	downstreamRedirect := "https://claude.ai/api/mcp/auth_callback"
	clientID := registerClient(t, ts.URL, downstreamRedirect)

	// 1. Downstream client (e.g. Claude) starts the flow.
	authResp, err := noRedirectClient().Get(ts.URL + "/authorize?" + url.Values{
		"response_type":         {"code"},
		"client_id":             {clientID},
		"redirect_uri":          {downstreamRedirect},
		"state":                 {"downstream-state"},
		"code_challenge":        {pkceChallengeS256("downstream-verifier")},
		"code_challenge_method": {"S256"},
	}.Encode())
	if err != nil {
		t.Fatal(err)
	}
	authLoc, err := authResp.Location()
	authResp.Body.Close()
	if err != nil {
		t.Fatalf("authorize did not redirect: %v", err)
	}

	// 2. Browser follows the redirect to the (fake) upstream AS, which logs
	// the user in and redirects back to this process's own callback.
	callbackResp, err := noRedirectClient().Get(authLoc.String())
	if err != nil {
		t.Fatal(err)
	}
	upstreamLoc, err := callbackResp.Location()
	callbackResp.Body.Close()
	if err != nil {
		t.Fatalf("upstream authorize did not redirect: %v", err)
	}
	if up.gotAuthorizeRedirectURI != "https://mcp.example.test/mcp/callback" {
		t.Errorf("upstream saw redirect_uri = %q, want this process's fixed callback", up.gotAuthorizeRedirectURI)
	}

	// 3. Browser follows that redirect to /mcp/callback (rewritten onto the
	// test server's actual address).
	cbURL, err := url.Parse(ts.URL + upstreamLoc.Path + "?" + upstreamLoc.RawQuery)
	if err != nil {
		t.Fatal(err)
	}
	finalResp, err := noRedirectClient().Get(cbURL.String())
	if err != nil {
		t.Fatal(err)
	}
	finalLoc, err := finalResp.Location()
	finalResp.Body.Close()
	if err != nil {
		t.Fatalf("callback did not redirect to downstream: %v", err)
	}
	if got := finalLoc.Scheme + "://" + finalLoc.Host + finalLoc.Path; got != downstreamRedirect {
		t.Errorf("callback redirected to %q, want %q", got, downstreamRedirect)
	}
	if finalLoc.Query().Get("state") != "downstream-state" {
		t.Errorf("downstream state = %q, want the original downstream-state", finalLoc.Query().Get("state"))
	}
	proxyCodeVal := finalLoc.Query().Get("code")
	if proxyCodeVal == "" {
		t.Fatal("callback redirect carries no code")
	}

	// 4. The downstream client redeems the code at /token with its own PKCE
	// verifier and redirect_uri.
	tokResp, err := http.Post(ts.URL+"/token", "application/x-www-form-urlencoded", strings.NewReader(url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {proxyCodeVal},
		"redirect_uri":  {downstreamRedirect},
		"client_id":     {clientID},
		"code_verifier": {"downstream-verifier"},
	}.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	defer tokResp.Body.Close()
	body, _ := io.ReadAll(tokResp.Body)
	if tokResp.StatusCode != http.StatusOK {
		t.Fatalf("token status = %d %s", tokResp.StatusCode, body)
	}
	var tok map[string]any
	if err := json.Unmarshal(body, &tok); err != nil {
		t.Fatal(err)
	}
	if tok["access_token"] != "mm-access" {
		t.Errorf("access_token = %v, want the real upstream token", tok["access_token"])
	}
	if tok["refresh_token"] != "mm-refresh" {
		t.Errorf("refresh_token = %v, want the real upstream token", tok["refresh_token"])
	}
	if up.gotTokenForm.Get("client_id") != "mm-public" {
		t.Errorf("upstream saw client_id = %q, want MAGICMARKETS_OAUTH_CLIENT_ID", up.gotTokenForm.Get("client_id"))
	}
	if up.gotTokenForm.Get("redirect_uri") != "https://mcp.example.test/mcp/callback" {
		t.Errorf("upstream saw redirect_uri = %q, want this process's fixed callback", up.gotTokenForm.Get("redirect_uri"))
	}
}

func TestHTTPTokenRejectsWrongPKCEVerifier(t *testing.T) {
	up := newUpstreamAS(t, "upstream-code", map[string]any{
		"access_token": "mm-access", "token_type": "Bearer", "expires_in": 3600,
	})
	s := New(nil, &config.Config{
		APIURL:        "https://example.invalid/v2",
		OAuthIssuer:   up.URL,
		OAuthClientID: "mm-public",
	}, Options{Version: "test", PublicURL: "https://mcp.example.test"})
	ts := httptest.NewServer(s.httpHandler())
	t.Cleanup(ts.Close)

	downstreamRedirect := "http://127.0.0.1:9/cb"
	clientID := registerClient(t, ts.URL, downstreamRedirect)
	proxyCodeVal := mintProxyCodeForTest(t, ts, clientID, downstreamRedirect, "correct-verifier")

	resp, err := http.Post(ts.URL+"/token", "application/x-www-form-urlencoded", strings.NewReader(url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {proxyCodeVal},
		"redirect_uri":  {downstreamRedirect},
		"client_id":     {clientID},
		"code_verifier": {"wrong-verifier"},
	}.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 invalid_grant", resp.StatusCode)
	}
}

// mintProxyCodeForTest drives a real authorize -> callback round trip and
// returns the resulting proxy code, for tests that only care about /token.
func mintProxyCodeForTest(t *testing.T, ts *httptest.Server, clientID, redirectURI, verifier string) string {
	t.Helper()
	authResp, err := noRedirectClient().Get(ts.URL + "/authorize?" + url.Values{
		"client_id":             {clientID},
		"redirect_uri":          {redirectURI},
		"state":                 {"st"},
		"code_challenge":        {pkceChallengeS256(verifier)},
		"code_challenge_method": {"S256"},
	}.Encode())
	if err != nil {
		t.Fatal(err)
	}
	authLoc, err := authResp.Location()
	authResp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	upResp, err := noRedirectClient().Get(authLoc.String())
	if err != nil {
		t.Fatal(err)
	}
	cbLoc, err := upResp.Location()
	upResp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	cbURL, err := url.Parse(ts.URL + cbLoc.Path + "?" + cbLoc.RawQuery)
	if err != nil {
		t.Fatal(err)
	}
	cbResp, err := noRedirectClient().Get(cbURL.String())
	if err != nil {
		t.Fatal(err)
	}
	finalLoc, err := cbResp.Location()
	cbResp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	return finalLoc.Query().Get("code")
}

func TestHTTPTokenRefreshProxiesToUpstreamAS(t *testing.T) {
	var gotForm url.Values
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth2/token" {
			http.NotFound(w, r)
			return
		}
		_ = r.ParseForm()
		gotForm = r.PostForm
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"mm-access-2","token_type":"Bearer","expires_in":3600}`)
	}))
	t.Cleanup(upstream.Close)

	s := New(nil, &config.Config{
		APIURL:        "https://example.invalid/v2",
		OAuthIssuer:   upstream.URL,
		OAuthClientID: "mm-public",
	}, Options{Version: "test", PublicURL: "https://mcp.example.test"})
	ts := httptest.NewServer(s.httpHandler())
	t.Cleanup(ts.Close)

	resp, err := http.Post(ts.URL+"/token", "application/x-www-form-urlencoded", strings.NewReader(url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {"mm-refresh"},
		"client_id":     {"whatever-the-downstream-client-sends"},
	}.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d %s", resp.StatusCode, body)
	}
	if gotForm.Get("client_id") != "mm-public" {
		t.Errorf("proxied client_id = %q, want MAGICMARKETS_OAUTH_CLIENT_ID substituted in", gotForm.Get("client_id"))
	}
	if gotForm.Get("refresh_token") != "mm-refresh" {
		t.Errorf("proxied refresh_token = %q", gotForm.Get("refresh_token"))
	}
	var tok map[string]any
	if err := json.Unmarshal(body, &tok); err != nil {
		t.Fatal(err)
	}
	if tok["access_token"] != "mm-access-2" {
		t.Errorf("access_token = %v", tok["access_token"])
	}
}
