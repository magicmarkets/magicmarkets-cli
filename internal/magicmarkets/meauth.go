package magicmarkets

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	lru "github.com/hashicorp/golang-lru/v2/expirable"
)

// MeCacheTTL bounds how long a credential resolved from an OAuth access token is reused
// before a fresh exchange is made. A deliberate const rather than a config knob for now — see
// README "Authentication — known limitations".
const MeCacheTTL = time.Minute

// meCacheSize bounds the number of distinct access tokens held at once.
// Generous relative to how many concurrent OAuth sessions one replica sees.
const meCacheSize = 4096

// identityToolkitSignInURL is Google's Firebase Identity Toolkit endpoint that redeems a
// Firebase custom token for a real ID token — see [MeResolver.fetch].
const identityToolkitSignInURL = "https://identitytoolkit.googleapis.com/v1/accounts:signInWithCustomToken"

// MeCredential is what an OAuth access token resolves to: the values the v2 API and
// /v2/stream actually require on every subsequent call, in place of the access token itself.
type MeCredential struct {
	// JWT is sent as the magic-metadata-jwt header on REST, and as the jwt query parameter on
	// the stream. It's a Firebase ID token: the Authorization Server's exchange only mints a
	// Firebase custom token, which is not itself valid here, so this resolver redeems it for
	// an ID token via Firebase's own signInWithCustomToken first — see [MeResolver.fetch].
	JWT string
	// Session is sent as the session header on REST ("m-<group id>-<user
	// uuid>"), and as the token query parameter on the stream.
	Session string
}

// MeResolver turns an OAuth access token into a [MeCredential] via two hops against the
// Magic Markets Authorization Server and then Firebase: it exchanges the access token for a
// Firebase custom token carrying the player's real entitlements, then redeems that custom
// token for a Firebase ID token — caching the result for [MeCacheTTL] so a burst of tool calls
// on the same access token doesn't repeat either hop for each one.
//
// This resolves via POST {issuer}/oauth2/firebase-token, not GET {issuer}/me: /me requires a
// genuine Firebase ID token (it's guarded by Firebase-only auth on the Authorization Server),
// which the self-signed OAuth access token this resolver is given never satisfies. The type
// keeps the Me* naming for continuity with the credential shape it produces and the mechanism
// it replaced — see README "Authentication — known limitations".
//
// Only the OAuth Bearer scheme goes through this — an API key is sent to the v2 API and the
// stream unchanged, with no exchange at all.
//
// The cache is an in-process LRU (hashicorp/golang-lru's expirable variant), not shared across
// replicas of a multi-pod deployment — see README "Authentication — known limitations". That's
// fine for what this buys: worst case on a cache miss routed to the "wrong" replica is one
// redundant exchange call, not a failure, unlike the OAuth proxy's sealed state in
// mcpserver/oauth.go which must decode correctly on any replica.
type MeResolver struct {
	issuer            string
	groupID           string
	firebaseWebAPIKey string
	httpClient        *http.Client
	cache             *lru.LRU[string, MeCredential]

	// Trace, when non-nil, is called with a one-line summary of each step of
	// the access-token → MeCredential exchange. Used by the --verbose flag.
	// Secrets are redacted to their last 4 characters.
	Trace func(format string, args ...any)
}

// trace calls r.Trace if set, a no-op otherwise.
func (r *MeResolver) trace(format string, args ...any) {
	if r.Trace != nil {
		r.Trace(format, args...)
	}
}

// NewMeResolver builds a resolver against the given Magic Markets
// Authorization Server issuer (MAGICMARKETS_OAUTH_ISSUER). groupID is the
// environment-specific session group id Magic Markets assigns — see
// MAGICMARKETS_SESSION_GROUP_ID. firebaseWebAPIKey is the Magic Markets
// Firebase project's Web API key, used to redeem the Firebase custom token
// for an ID token — see MAGICMARKETS_FIREBASE_WEB_API_KEY. httpClient may be
// nil to use http.DefaultClient.
func NewMeResolver(issuer, groupID, firebaseWebAPIKey string, httpClient *http.Client) *MeResolver {
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	return &MeResolver{
		issuer:            strings.TrimRight(issuer, "/"),
		groupID:           groupID,
		firebaseWebAPIKey: firebaseWebAPIKey,
		httpClient:        httpClient,
		cache:             lru.NewLRU[string, MeCredential](meCacheSize, nil, MeCacheTTL),
	}
}

// Resolve returns the cached credential for accessToken, exchanging it on a cache miss or
// expiry.
func (r *MeResolver) Resolve(ctx context.Context, accessToken string) (MeCredential, error) {
	accessToken = strings.TrimSpace(accessToken)
	if accessToken == "" {
		return MeCredential{}, fmt.Errorf("resolve oauth credential: access token is empty")
	}
	if cred, ok := r.cache.Get(accessToken); ok {
		r.trace("meauth: cache hit for token ...%s -> session=%s jwt=...%s",
			redactTail(accessToken), cred.Session, redactTail(cred.JWT))
		return cred, nil
	}
	r.trace("meauth: cache miss for token ...%s, exchanging", redactTail(accessToken))
	cred, err := r.fetch(ctx, accessToken)
	if err != nil {
		r.trace("meauth: exchange failed: %v", err)
		return MeCredential{}, err
	}
	r.cache.Add(accessToken, cred)
	r.trace("meauth: resolved session=%s jwt=...%s", cred.Session, redactTail(cred.JWT))
	return cred, nil
}

// firebaseTokenResponse is the shape of POST {issuer}/oauth2/firebase-token.
type firebaseTokenResponse struct {
	FirebaseToken string `json:"firebase_token"`
}

func (r *MeResolver) fetch(ctx context.Context, accessToken string) (MeCredential, error) {
	// The session header needs the player's uuid. The Authorization Server's exchange
	// response doesn't carry it (it only returns firebase_token), so it's read directly off
	// the access token we already hold — unverified, since it's only used to build an opaque
	// session string, never for an authorization decision. The actual authorization check
	// happens server-side, against the token's verified signature, in the exchange call
	// below.
	uuid, err := subjectFromAccessToken(accessToken)
	if err != nil {
		return MeCredential{}, fmt.Errorf("read subject from access token: %w", err)
	}
	r.trace("meauth: access token sub=%s, group=%s", uuid, r.groupID)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.issuer+"/oauth2/firebase-token", nil)
	if err != nil {
		return MeCredential{}, fmt.Errorf("build firebase-token exchange request: %w", err)
	}
	req.Header.Set(AuthorizationHeader, "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	r.trace("meauth: POST %s/oauth2/firebase-token", r.issuer)
	resp, err := NoRedirectClient(r.httpClient).Do(req)
	if err != nil {
		r.trace("meauth: firebase-token exchange failed: %v", err)
		return MeCredential{}, fmt.Errorf("firebase-token exchange failed: %w", err)
	}
	defer resp.Body.Close()

	r.trace("meauth: firebase-token exchange -> HTTP %d", resp.StatusCode)
	if resp.StatusCode != http.StatusOK {
		return MeCredential{}, fmt.Errorf("firebase-token exchange returned HTTP %d", resp.StatusCode)
	}

	var body firebaseTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return MeCredential{}, fmt.Errorf("decode firebase-token exchange response: %w", err)
	}
	if body.FirebaseToken == "" {
		return MeCredential{}, fmt.Errorf("firebase-token exchange response missing firebase_token")
	}

	if strings.TrimSpace(r.firebaseWebAPIKey) == "" {
		return MeCredential{}, fmt.Errorf("resolve oauth credential: no Firebase Web API key configured " +
			"(missing MAGICMARKETS_FIREBASE_WEB_API_KEY?)")
	}
	r.trace("meauth: redeeming custom token ...%s via identitytoolkit (web api key ...%s)",
		redactTail(body.FirebaseToken), redactTail(r.firebaseWebAPIKey))
	endpoint := identityToolkitSignInURL + "?key=" + url.QueryEscape(r.firebaseWebAPIKey)
	idToken, err := redeemCustomToken(ctx, r.httpClient, endpoint, body.FirebaseToken)
	if err != nil {
		r.trace("meauth: redeem custom token failed: %v", err)
		return MeCredential{}, err
	}
	r.trace("meauth: redeemed ID token ...%s", redactTail(idToken))

	return MeCredential{
		JWT:     idToken,
		Session: fmt.Sprintf("m-%s-%s", r.groupID, uuid),
	}, nil
}

// signInWithCustomTokenResponse is the shape of Firebase's
// accounts:signInWithCustomToken response. Only the field this client needs is modeled.
type signInWithCustomTokenResponse struct {
	IDToken string `json:"idToken"`
}

// redeemCustomToken exchanges a Firebase custom token (minted by the Authorization Server's
// /oauth2/firebase-token) for a Firebase ID token, via Firebase's own Identity Toolkit REST
// API at endpoint (identityToolkitSignInURL in production, with the Firebase Web API key
// already appended as a query parameter by the caller). A custom token is not itself a valid
// magic-metadata-jwt — see [MeCredential.JWT]. A free function, not a method, so it's testable
// against an arbitrary endpoint without any override state on MeResolver.
func redeemCustomToken(ctx context.Context, httpClient *http.Client, endpoint, customToken string) (string, error) {
	reqBody, err := json.Marshal(map[string]any{
		"token":             customToken,
		"returnSecureToken": true,
	})
	if err != nil {
		return "", fmt.Errorf("build signInWithCustomToken request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(reqBody))
	if err != nil {
		return "", fmt.Errorf("build signInWithCustomToken request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := NoRedirectClient(httpClient).Do(req)
	if err != nil {
		return "", fmt.Errorf("signInWithCustomToken failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read signInWithCustomToken response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("signInWithCustomToken returned HTTP %d: %s", resp.StatusCode, respBody)
	}

	var out signInWithCustomTokenResponse
	if err := json.Unmarshal(respBody, &out); err != nil {
		return "", fmt.Errorf("decode signInWithCustomToken response: %w", err)
	}
	if out.IDToken == "" {
		return "", fmt.Errorf("signInWithCustomToken response missing idToken")
	}
	return out.IDToken, nil
}

// subjectFromAccessToken reads the "sub" claim from a JWT's payload without verifying its
// signature. See the caller in fetch for why that's safe here.
func subjectFromAccessToken(accessToken string) (string, error) {
	parts := strings.Split(accessToken, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("not a JWT: want 3 dot-separated parts, got %d", len(parts))
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", fmt.Errorf("decode payload: %w", err)
	}
	var claims struct {
		Sub string `json:"sub"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", fmt.Errorf("parse payload: %w", err)
	}
	if claims.Sub == "" {
		return "", fmt.Errorf("token has no sub claim")
	}
	return claims.Sub, nil
}
