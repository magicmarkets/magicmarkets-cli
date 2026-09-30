package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"magicmarkets-cli/internal/magicmarkets"
)

const (
	protectedResourcePath    = "/.well-known/oauth-protected-resource"
	protectedResourceMCPPath = "/.well-known/oauth-protected-resource/mcp"
	authorizationServerPath  = "/.well-known/oauth-authorization-server"
	openidConfigurationPath  = "/.well-known/openid-configuration"
	registerPath             = "/register"
	authorizePath            = "/authorize"
	tokenPath                = "/token"
	callbackPath             = "/callback"
)

// Sealed-token kinds, so a token minted for one purpose can't be replayed
// as another — see sealer in oauthcrypto.go.
const (
	sealKindClient = "client"
	sealKindState  = "state"
	sealKindCode   = "code"
)

// stateTTL bounds how long a downstream client has to complete the upstream
// login after /authorize redirects it. codeTTL bounds the one-time
// authorization code /callback hands back — see oauthcrypto.go's sealer
// doc comment for why "one-time" isn't enforced server-side.
const (
	stateTTL = 10 * time.Minute
	codeTTL  = 60 * time.Second
)

// registeredClient is what /register hands back as an opaque client_id: the
// redirect_uris a downstream client (Claude, Cursor, ...) registered. Sealing
// it into the id itself — rather than an in-memory map — is what lets
// /authorize validate a registration on whichever hosted-deployment replica
// receives the request, and lets it survive a restart.
type registeredClient struct {
	Name         string   `json:"n,omitempty"`
	RedirectURIs []string `json:"r"`
}

// pendingAuth is what /authorize seals into the `state` it sends upstream:
// everything /callback needs to complete the code exchange and redirect back
// to the downstream client, without server-side session storage.
type pendingAuth struct {
	DownstreamRedirectURI   string `json:"rd"`
	DownstreamState         string `json:"st"`
	DownstreamCodeChallenge string `json:"cc"`
	UpstreamCodeVerifier    string `json:"uv"`
}

// proxyCode is what /callback seals into the one-time code it redirects back
// to the downstream client with: the real Magic Markets tokens obtained from
// the upstream exchange, bound to the authorization request that must redeem
// them (redirect_uri + PKCE).
type proxyCode struct {
	AccessToken             string `json:"at"`
	RefreshToken            string `json:"rt,omitempty"`
	TokenType               string `json:"tt"`
	ExpiresIn               int    `json:"ei,omitempty"`
	Scope                   string `json:"sc,omitempty"`
	DownstreamRedirectURI   string `json:"rd"`
	DownstreamCodeChallenge string `json:"cc"`
}

// upstreamTokens is the token response from the real Magic Markets AS.
type upstreamTokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in,omitempty"`
	Scope        string `json:"scope,omitempty"`
}

// tokenResponse is what /token hands back to the downstream client for an
// authorization_code grant: the real upstream tokens, so the client can call
// /mcp with Authorization: Bearer directly — this process never sees a
// separate "local" access token to keep track of.
type tokenResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in,omitempty"`
	Scope        string `json:"scope,omitempty"`
}

func writeCORS(w http.ResponseWriter) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, MCP-Protocol-Version")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	writeCORS(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeOAuthError(w http.ResponseWriter, status int, code, desc string) {
	writeJSON(w, status, map[string]string{"error": code, "error_description": desc})
}

func (s *Server) mountOAuth(mux *http.ServeMux) {
	prm := s.serveProtectedResourceMetadata
	as := s.serveAuthorizationServerMetadata
	for _, p := range []string{protectedResourcePath, protectedResourceMCPPath, HTTPPath + protectedResourcePath} {
		mux.HandleFunc("GET "+p, prm)
	}
	for _, p := range []string{authorizationServerPath, openidConfigurationPath, HTTPPath + authorizationServerPath} {
		mux.HandleFunc("GET "+p, as)
	}
	for _, p := range []string{registerPath, HTTPPath + registerPath} {
		mux.HandleFunc(p, s.serveRegister)
	}
	for _, p := range []string{authorizePath, HTTPPath + authorizePath} {
		mux.HandleFunc(p, s.serveAuthorize)
	}
	for _, p := range []string{tokenPath, HTTPPath + tokenPath} {
		mux.HandleFunc(p, s.serveToken)
	}
	for _, p := range []string{callbackPath, HTTPPath + callbackPath} {
		mux.HandleFunc("GET "+p, s.serveCallback)
	}
}

func (s *Server) asIssuer(r *http.Request) string {
	return strings.TrimRight(s.publicBase(r), "/")
}

// callbackURL is the one fixed redirect_uri this process ever presents to
// the upstream Magic Markets AS — the value that must be allowlisted for
// MAGICMARKETS_OAUTH_CLIENT_ID there. Downstream clients' own redirect_uris
// never reach upstream; see serveAuthorize.
func (s *Server) callbackURL(r *http.Request) string {
	return s.asIssuer(r) + HTTPPath + callbackPath
}

func (s *Server) upstreamClientID() string {
	if s.cfg != nil {
		return strings.TrimSpace(s.cfg.OAuthClientID)
	}
	return ""
}

func (s *Server) serveProtectedResourceMetadata(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		writeCORS(w)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	issuer := s.asIssuer(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"resource":                 issuer + HTTPPath,
		"authorization_servers":    []string{issuer},
		"bearer_methods_supported": []string{"header"},
		"scopes_supported":         []string{"mcp"},
	})
}

func (s *Server) serveAuthorizationServerMetadata(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		writeCORS(w)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	issuer := s.asIssuer(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                issuer,
		"authorization_endpoint":                issuer + authorizePath,
		"token_endpoint":                        issuer + tokenPath,
		"registration_endpoint":                 issuer + registerPath,
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none"},
		"scopes_supported":                      []string{"mcp"},
	})
}

// serveRegister answers RFC 7591 Dynamic Client Registration. The returned
// client_id is a sealed token encoding the caller's redirect_uris — see
// registeredClient — so there is nothing to look up later, on this replica
// or any other.
func (s *Server) serveRegister(w http.ResponseWriter, r *http.Request) {
	writeCORS(w)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var body struct {
		ClientName   string   `json:"client_name"`
		RedirectURIs []string `json:"redirect_uris"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", "request body must be valid JSON")
		return
	}
	if len(body.RedirectURIs) == 0 {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client_metadata", "redirect_uris is required and must be non-empty")
		return
	}
	for _, uri := range body.RedirectURIs {
		if !isAbsoluteURI(uri) {
			writeOAuthError(w, http.StatusBadRequest, "invalid_redirect_uri",
				fmt.Sprintf("redirect_uri %q is not an absolute URI", uri))
			return
		}
	}

	name := strings.TrimSpace(body.ClientName)
	if name == "" {
		name = "unnamed MCP client"
	}
	clientID, err := s.seal.seal(sealKindClient, registeredClient{Name: name, RedirectURIs: body.RedirectURIs}, 0)
	if err != nil {
		writeOAuthError(w, http.StatusInternalServerError, "server_error", "failed to issue client_id")
		return
	}

	writeJSON(w, http.StatusCreated, map[string]any{
		"client_id":                  clientID,
		"client_name":                name,
		"redirect_uris":              body.RedirectURIs,
		"grant_types":                []string{"authorization_code", "refresh_token"},
		"response_types":             []string{"code"},
		"token_endpoint_auth_method": "none",
	})
}

// serveAuthorize validates the downstream client's request, then starts this
// process's own PKCE login against the upstream Magic Markets AS: it never
// forwards the downstream client's redirect_uri upstream (the real AS only
// allowlists this process's fixed callback — see callbackURL), and it
// generates its own code_challenge for that upstream leg. Everything needed
// to resume the downstream request travels in the sealed `state`.
func (s *Server) serveAuthorize(w http.ResponseWriter, r *http.Request) {
	writeCORS(w)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "invalid form body", http.StatusBadRequest)
			return
		}
		q = r.Form
	}

	if rt := q.Get("response_type"); rt != "" && rt != "code" {
		http.Error(w, "response_type must be code", http.StatusBadRequest)
		return
	}

	var client registeredClient
	if err := s.seal.open(sealKindClient, q.Get("client_id"), &client); err != nil {
		http.Error(w, "unknown client_id", http.StatusBadRequest)
		return
	}
	redirectURI := q.Get("redirect_uri")
	if !slices.Contains(client.RedirectURIs, redirectURI) {
		http.Error(w, "redirect_uri does not match registration", http.StatusBadRequest)
		return
	}
	challenge := q.Get("code_challenge")
	method := q.Get("code_challenge_method")
	if challenge == "" || (method != "" && method != "S256") {
		http.Error(w, "code_challenge with code_challenge_method=S256 is required", http.StatusBadRequest)
		return
	}

	upstreamVerifier, upstreamChallenge, err := generatePKCE()
	if err != nil {
		http.Error(w, "failed to start upstream login", http.StatusInternalServerError)
		return
	}
	state, err := s.seal.seal(sealKindState, pendingAuth{
		DownstreamRedirectURI:   redirectURI,
		DownstreamState:         q.Get("state"),
		DownstreamCodeChallenge: challenge,
		UpstreamCodeVerifier:    upstreamVerifier,
	}, stateTTL)
	if err != nil {
		http.Error(w, "failed to start upstream login", http.StatusInternalServerError)
		return
	}

	upstream := strings.TrimRight(s.opts.OAuthIssuer, "/") + "/oauth2/authorize"
	u, err := url.Parse(upstream)
	if err != nil {
		http.Error(w, "upstream issuer is not a URL", http.StatusInternalServerError)
		return
	}
	scope := q.Get("scope")
	if scope == "" {
		scope = "mcp"
	}
	out := url.Values{
		"response_type":         {"code"},
		"client_id":             {s.upstreamClientID()},
		"redirect_uri":          {s.callbackURL(r)},
		"state":                 {state},
		"code_challenge":        {upstreamChallenge},
		"code_challenge_method": {"S256"},
		"scope":                 {scope},
	}
	u.RawQuery = out.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

// serveCallback is where the upstream Magic Markets AS redirects the user's
// browser back to after login — this process's one fixed callback (see
// callbackURL). It completes the upstream code exchange and hands the
// resulting tokens back to the downstream client as a one-time proxy code.
func (s *Server) serveCallback(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	var pending pendingAuth
	if err := s.seal.open(sealKindState, q.Get("state"), &pending); err != nil {
		http.Error(w, "invalid or expired login session — restart the connection from your MCP client", http.StatusBadRequest)
		return
	}

	if errParam := q.Get("error"); errParam != "" {
		s.redirectDownstreamError(w, r, pending, errParam, q.Get("error_description"))
		return
	}
	code := q.Get("code")
	if code == "" {
		s.redirectDownstreamError(w, r, pending, "server_error", "upstream did not return a code")
		return
	}

	tokens, err := s.exchangeUpstreamCode(r.Context(), code, pending.UpstreamCodeVerifier, s.callbackURL(r))
	if err != nil {
		s.redirectDownstreamError(w, r, pending, "server_error", err.Error())
		return
	}

	proxyCodeToken, err := s.seal.seal(sealKindCode, proxyCode{
		AccessToken:             tokens.AccessToken,
		RefreshToken:            tokens.RefreshToken,
		TokenType:               orDefault(tokens.TokenType, "Bearer"),
		ExpiresIn:               tokens.ExpiresIn,
		Scope:                   tokens.Scope,
		DownstreamRedirectURI:   pending.DownstreamRedirectURI,
		DownstreamCodeChallenge: pending.DownstreamCodeChallenge,
	}, codeTTL)
	if err != nil {
		s.redirectDownstreamError(w, r, pending, "server_error", "failed to issue code")
		return
	}

	dest, err := url.Parse(pending.DownstreamRedirectURI)
	if err != nil {
		http.Error(w, "downstream redirect_uri is not a URL", http.StatusInternalServerError)
		return
	}
	out := dest.Query()
	out.Set("code", proxyCodeToken)
	if pending.DownstreamState != "" {
		out.Set("state", pending.DownstreamState)
	}
	dest.RawQuery = out.Encode()
	http.Redirect(w, r, dest.String(), http.StatusFound)
}

// redirectDownstreamError reports an upstream or proxy failure to the
// downstream client the same way a real Authorization Server would: as a
// redirect to its own redirect_uri with error/error_description/state, not a
// bare HTTP error page the client's browser would get stuck on.
func (s *Server) redirectDownstreamError(w http.ResponseWriter, r *http.Request, pending pendingAuth, code, desc string) {
	dest, err := url.Parse(pending.DownstreamRedirectURI)
	if err != nil {
		http.Error(w, desc, http.StatusBadGateway)
		return
	}
	out := dest.Query()
	out.Set("error", code)
	if desc != "" {
		out.Set("error_description", desc)
	}
	if pending.DownstreamState != "" {
		out.Set("state", pending.DownstreamState)
	}
	dest.RawQuery = out.Encode()
	http.Redirect(w, r, dest.String(), http.StatusFound)
}

// exchangeUpstreamCode redeems an upstream authorization code for tokens at
// the real Magic Markets AS, using this process's own PKCE verifier and
// fixed callback redirect_uri.
func (s *Server) exchangeUpstreamCode(ctx context.Context, code, verifier, redirectURI string) (*upstreamTokens, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {redirectURI},
		"client_id":     {s.upstreamClientID()},
		"code_verifier": {verifier},
	}
	body, status, err := s.postUpstreamToken(ctx, form)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("upstream token exchange failed: HTTP %d: %s", status, body)
	}
	var tok upstreamTokens
	if err := json.Unmarshal(body, &tok); err != nil {
		return nil, fmt.Errorf("decode upstream token response: %w", err)
	}
	return &tok, nil
}

func (s *Server) postUpstreamToken(ctx context.Context, form url.Values) (body []byte, status int, err error) {
	endpoint := strings.TrimRight(s.opts.OAuthIssuer, "/") + "/oauth2/token"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := magicmarkets.NoRedirectClient(s.httpClient).Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("upstream token endpoint failed: %w", err)
	}
	defer resp.Body.Close()
	body, err = io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, fmt.Errorf("read upstream token response: %w", err)
	}
	return body, resp.StatusCode, nil
}

// serveToken answers the downstream client's token requests. For
// authorization_code it redeems the proxy code minted by serveCallback —
// entirely locally, no upstream call — and hands back the real Magic Markets
// tokens already embedded in it. For refresh_token it proxies straight
// through to the upstream AS, substituting this process's client_id: the
// downstream client already holds the real upstream refresh_token (returned
// from the authorization_code exchange above), so no local state is needed.
func (s *Server) serveToken(w http.ResponseWriter, r *http.Request) {
	writeCORS(w)
	if r.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "malformed form body")
		return
	}

	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		s.serveTokenAuthorizationCode(w, r)
	case "refresh_token":
		s.serveTokenRefresh(w, r)
	default:
		writeOAuthError(w, http.StatusBadRequest, "unsupported_grant_type",
			"grant_type must be authorization_code or refresh_token")
	}
}

func (s *Server) serveTokenAuthorizationCode(w http.ResponseWriter, r *http.Request) {
	form := r.PostForm

	var pc proxyCode
	if err := s.seal.open(sealKindCode, form.Get("code"), &pc); err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "unknown or expired code")
		return
	}
	if form.Get("redirect_uri") != pc.DownstreamRedirectURI {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "redirect_uri does not match the authorization request")
		return
	}
	verifier := form.Get("code_verifier")
	if verifier == "" || pkceChallengeS256(verifier) != pc.DownstreamCodeChallenge {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "code_verifier does not match code_challenge")
		return
	}

	writeJSON(w, http.StatusOK, tokenResponse{
		AccessToken:  pc.AccessToken,
		RefreshToken: pc.RefreshToken,
		TokenType:    pc.TokenType,
		ExpiresIn:    pc.ExpiresIn,
		Scope:        pc.Scope,
	})
}

func (s *Server) serveTokenRefresh(w http.ResponseWriter, r *http.Request) {
	form := url.Values{}
	for k, vs := range r.PostForm {
		form[k] = append([]string(nil), vs...)
	}
	form.Set("client_id", s.upstreamClientID())

	body, status, err := s.postUpstreamToken(r.Context(), form)
	if err != nil {
		writeOAuthError(w, http.StatusBadGateway, "server_error", err.Error())
		return
	}
	writeCORS(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func isAbsoluteURI(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || (u.Host == "" && u.Opaque == "" && u.Path == "") {
		return false
	}
	return true
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
