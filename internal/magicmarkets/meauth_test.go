package magicmarkets

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// fakeAccessToken builds a JWT-shaped string carrying only a "sub" claim in its (unsigned,
// unverified-by-this-package) payload — enough for subjectFromAccessToken to read it. The
// resolver never verifies the signature client-side; that happens server-side, in the
// firebase-token exchange call this resolver makes.
func fakeAccessToken(sub string) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"` + sub + `"}`))
	return header + "." + payload + ".sig"
}

type hostRedirectTransport struct {
	host   string
	target *url.URL
}

// identityToolkitClient builds an *http.Client that redirects requests to
// identitytoolkit.googleapis.com — the hardcoded endpoint [redeemCustomToken] calls in
// production — to target instead, leaving every other request untouched. Lets a full
// Resolve() round trip be tested without any override state on MeResolver: the const stays a
// genuine const.
func identityToolkitClient(t *testing.T, target string) *http.Client {
	t.Helper()
	u, err := url.Parse(target)
	if err != nil {
		t.Fatalf("identityToolkitClient: parse target %q: %v", target, err)
	}
	return &http.Client{Transport: hostRedirectTransport{host: "identitytoolkit.googleapis.com", target: u}}
}

func (t hostRedirectTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Host == t.host {
		req = req.Clone(req.Context())
		req.URL.Scheme = t.target.Scheme
		req.URL.Host = t.target.Host
	}
	return http.DefaultTransport.RoundTrip(req)
}

// stubIdentityToolkit stands in for Google's signInWithCustomToken endpoint, always redeeming
// whatever custom token it's given for idToken.
func stubIdentityToolkit(t *testing.T, idToken string) *httptest.Server {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"idToken":"` + idToken + `"}`))
	}))
	t.Cleanup(ts.Close)
	return ts
}

func TestMeResolverCachesByAccessToken(t *testing.T) {
	var calls int
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"firebase_token":"jwt-` + r.Header.Get(AuthorizationHeader) + `"}`))
	}))
	t.Cleanup(ts.Close)
	identity := stubIdentityToolkit(t, "redeemed-id-token")

	resolver := NewMeResolver(ts.URL, "5", "web-api-key", identityToolkitClient(t, identity.URL))
	ctx := context.Background()

	tokA := fakeAccessToken("uuid-1")
	first, err := resolver.Resolve(ctx, tokA)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	second, err := resolver.Resolve(ctx, tokA)
	if err != nil {
		t.Fatalf("Resolve (cached): %v", err)
	}
	if first != second {
		t.Errorf("cached resolve = %+v, want %+v", second, first)
	}
	if calls != 1 {
		t.Errorf("firebase-token exchange was called %d times for the same token, want 1", calls)
	}

	if _, err := resolver.Resolve(ctx, fakeAccessToken("uuid-2")); err != nil {
		t.Fatalf("Resolve (different token): %v", err)
	}
	if calls != 2 {
		t.Errorf("firebase-token exchange was called %d times across two distinct tokens, want 2", calls)
	}
}

func TestMeResolverSessionUsesGroupIDAndAccessTokenSubject(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if r.URL.Path != "/oauth2/firebase-token" {
			t.Errorf("path = %q, want /oauth2/firebase-token", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"firebase_token":"the-jwt"}`))
	}))
	t.Cleanup(ts.Close)
	identity := stubIdentityToolkit(t, "redeemed-id-token")

	resolver := NewMeResolver(ts.URL, "133", "web-api-key", identityToolkitClient(t, identity.URL))
	cred, err := resolver.Resolve(context.Background(), fakeAccessToken("abcd-1234"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if cred.JWT != "redeemed-id-token" {
		t.Errorf("JWT = %q, want redeemed-id-token", cred.JWT)
	}
	if cred.Session != "m-133-abcd-1234" {
		t.Errorf("Session = %q, want m-133-abcd-1234", cred.Session)
	}
}

func TestMeResolverRejectsNonJWTAccessToken(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"firebase_token":"the-jwt"}`))
	}))
	t.Cleanup(ts.Close)

	resolver := NewMeResolver(ts.URL, "5", "web-api-key", nil)
	if _, err := resolver.Resolve(context.Background(), "not-a-jwt"); err == nil {
		t.Fatal("expected an error for an access token with no readable sub claim")
	}
}

func TestMeResolverRejectsIncompleteResponse(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(ts.Close)

	resolver := NewMeResolver(ts.URL, "5", "web-api-key", nil)
	if _, err := resolver.Resolve(context.Background(), fakeAccessToken("uuid-1")); err == nil {
		t.Fatal("expected an error for a firebase-token response missing firebase_token")
	}
}

func TestMeResolverPropagatesUpstreamError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(ts.Close)

	resolver := NewMeResolver(ts.URL, "5", "web-api-key", nil)
	if _, err := resolver.Resolve(context.Background(), fakeAccessToken("uuid-1")); err == nil {
		t.Fatal("expected an error on a non-200 firebase-token exchange response")
	}
}

// TestMeResolverFailsLoudlyOnRedirect reproduces the production incident: an issuer that
// 301-redirects the firebase-token POST (e.g. a stray "www." prefix redirecting to the
// canonical domain) must not have that POST silently downgraded to a GET and re-sent — it must
// fail with a clear redirect error instead of the confusing "405 Method Not Allowed" a GET
// against a POST-only endpoint produces.
func TestMeResolverFailsLoudlyOnRedirect(t *testing.T) {
	var canonicalGETs int
	canonical := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			canonicalGETs++
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"firebase_token":"the-jwt"}`))
	}))
	t.Cleanup(canonical.Close)

	wwwIssuer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, canonical.URL+r.URL.Path, http.StatusMovedPermanently)
	}))
	t.Cleanup(wwwIssuer.Close)

	resolver := NewMeResolver(wwwIssuer.URL, "5", "web-api-key", nil)
	_, err := resolver.Resolve(context.Background(), fakeAccessToken("uuid-1"))
	if err == nil {
		t.Fatal("expected an error when the issuer redirects the exchange request")
	}
	if strings.Contains(err.Error(), "405") {
		t.Errorf("error = %q, must not surface as a bare 405 — that hides the real cause (the request was redirected and downgraded to GET)", err.Error())
	}
	if !strings.Contains(err.Error(), "redirect") {
		t.Errorf("error = %q, want it to mention the redirect", err.Error())
	}
	if canonicalGETs != 0 {
		t.Errorf("canonical endpoint saw %d GET(s), want 0 — the redirect must never be followed", canonicalGETs)
	}
}

func TestMeResolverRequiresFirebaseWebAPIKey(t *testing.T) {
	as := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"firebase_token":"custom-tok"}`))
	}))
	t.Cleanup(as.Close)

	resolver := NewMeResolver(as.URL, "5", "", nil)
	if _, err := resolver.Resolve(context.Background(), fakeAccessToken("uuid-1")); err == nil {
		t.Fatal("expected an error when no Firebase Web API key is configured")
	}
}

// TestRedeemCustomTokenSuccess is the regression test for the bug where the raw Firebase
// custom token from /oauth2/firebase-token was sent to the v2 API as magic-metadata-jwt
// unchanged. A custom token is not a valid ID token — it must be redeemed via Firebase's own
// signInWithCustomToken first (see README "Authentication"). redeemCustomToken is the free
// function fetch calls to do that; tested directly against an arbitrary endpoint, the same way
// magic-mcp's own signInWithCustomTokenURL is tested (no MeResolver override state involved).
func TestRedeemCustomTokenSuccess(t *testing.T) {
	var gotKey, gotBody string
	identity := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.URL.Query().Get("key")
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"idToken":"real-id-token"}`))
	}))
	t.Cleanup(identity.Close)

	endpoint := identity.URL + "?key=web-api-key"
	idToken, err := redeemCustomToken(context.Background(), http.DefaultClient, endpoint, "custom-tok")
	if err != nil {
		t.Fatalf("redeemCustomToken: %v", err)
	}
	if idToken != "real-id-token" {
		t.Errorf("idToken = %q, want real-id-token", idToken)
	}
	if gotKey != "web-api-key" {
		t.Errorf("signInWithCustomToken key param = %q, want web-api-key", gotKey)
	}
	if !strings.Contains(gotBody, "custom-tok") {
		t.Errorf("signInWithCustomToken body = %q, want it to carry the custom token custom-tok", gotBody)
	}
}

func TestRedeemCustomTokenPropagatesHTTPError(t *testing.T) {
	identity := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"INVALID_CUSTOM_TOKEN"}}`))
	}))
	t.Cleanup(identity.Close)

	_, err := redeemCustomToken(context.Background(), http.DefaultClient, identity.URL, "custom-tok")
	if err == nil {
		t.Fatal("expected an error on a non-200 signInWithCustomToken response")
	}
}

func TestRedeemCustomTokenRejectsMissingIDToken(t *testing.T) {
	identity := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(identity.Close)

	_, err := redeemCustomToken(context.Background(), http.DefaultClient, identity.URL, "custom-tok")
	if err == nil {
		t.Fatal("expected an error for a signInWithCustomToken response missing idToken")
	}
}
