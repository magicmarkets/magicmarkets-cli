package magicmarkets

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

// AuthorizationHeader is the HTTP header a caller uses to present an OAuth
// Bearer token to this process, and the header this process presents its own
// Bearer token on when exchanging it for a Firebase custom token. The
// caller's Bearer token itself is never forwarded downstream to the v2 API
// or the stream unchanged — see [Credential.ApplyTo]. The same header name
// is reused downstream for an unrelated purpose: an infra-level HTTP Basic
// Auth wall some non-production environments put in front of the v2 API —
// see [WithBasicAuth].
const AuthorizationHeader = "Authorization"

// MagicMetadataJWTHeader and SessionHeader are what an OAuth Bearer
// credential actually goes out as on the v2 API, once resolved via
// [MeResolver]. See [Credential.ApplyTo].
const (
	MagicMetadataJWTHeader = "magic-metadata-jwt"
	SessionHeader          = "session"
)

// Credential is one caller-presented Magic Markets credential: an API key in
// X-Api-Key, or an OAuth access token in Authorization: Bearer. An API key
// wins when both are set, matching the documented primary scheme.
//
// Neither is what actually goes out on the wire to the v2 API or the stream
// — see [Credential.ApplyTo].
type Credential struct {
	APIKey string
	Bearer string
}

// Empty reports whether neither scheme is present.
func (c Credential) Empty() bool {
	return strings.TrimSpace(c.APIKey) == "" && strings.TrimSpace(c.Bearer) == ""
}

// ApplyTo sets the outbound authentication header(s) for this credential on
// h. An API key is applied directly as X-Api-Key.
//
// A Bearer token is never forwarded as-is: the v2 API and the stream don't
// accept it. It must first be resolved, via resolver, to the
// magic-metadata-jwt/session pair the Authorization Server's token-exchange
// endpoint actually returns for it — see [MeResolver]. resolver must be
// non-nil whenever cred carries a Bearer token; it is not needed for an API
// key.
func (c Credential) ApplyTo(ctx context.Context, h http.Header, resolver *MeResolver) error {
	if key := strings.TrimSpace(c.APIKey); key != "" {
		h.Set(APIKeyHeader, key)
		return nil
	}
	token := strings.TrimSpace(c.Bearer)
	if token == "" {
		return nil
	}
	if resolver == nil {
		return fmt.Errorf("magicmarkets: OAuth credential set but no token resolver configured " +
			"(missing MAGICMARKETS_OAUTH_ISSUER or MAGICMARKETS_SESSION_GROUP_ID?)")
	}
	me, err := resolver.Resolve(ctx, token)
	if err != nil {
		return err
	}
	h.Set(MagicMetadataJWTHeader, me.JWT)
	h.Set(SessionHeader, me.Session)
	return nil
}

// redactTail masks all but the last 4 characters of a secret, for safe
// inclusion in trace/debug output.
func redactTail(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "(empty)"
	}
	if len(s) <= 4 {
		return strings.Repeat("*", len(s))
	}
	return strings.Repeat("*", len(s)-4) + s[len(s)-4:]
}

// BearerToken extracts the token from an Authorization header value.
func BearerToken(header string) (string, bool) {
	s := strings.TrimSpace(header)
	const prefix = "Bearer "
	if len(s) < len(prefix) || !strings.EqualFold(s[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(s[len(prefix):])
	if token == "" {
		return "", false
	}
	return token, true
}

// CredentialFromHeaders reads X-Api-Key, then Authorization: Bearer.
func CredentialFromHeaders(h http.Header) (Credential, bool) {
	if key := strings.TrimSpace(h.Get(APIKeyHeader)); key != "" {
		return Credential{APIKey: key}, true
	}
	if token, ok := BearerToken(h.Get(AuthorizationHeader)); ok {
		return Credential{Bearer: token}, true
	}
	return Credential{}, false
}
