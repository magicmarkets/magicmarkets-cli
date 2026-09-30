package mcpserver

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"time"
)

// The hosted MCP deployment runs multiple replicas with no session affinity
// (see streamableHTTPOptions), and the OAuth dance crosses several requests —
// the browser hits /authorize then /callback, and the downstream client hits
// /token from a different network path entirely. None of those are
// guaranteed to land on the same replica, so the proxy keeps no server-side
// session state at all: every value it hands out (a DCR client_id, the
// `state` it sends upstream, the one-time code it returns downstream) is a
// sealed, self-contained blob that any replica can verify on its own, as
// long as every replica shares MAGICMARKETS_OAUTH_PROXY_SECRET.
//
// Trade-off: because there is no shared store, a redeemed authorization code
// is not tracked as spent — it stays valid for its full TTL rather than being
// invalidated after first use. PKCE still protects it: the code alone (which
// may appear in a redirect URL, logs, or browser history) is useless without
// the verifier, which never leaves the original downstream client.
type sealer struct {
	aead cipher.AEAD
}

func newSealer(secret string) *sealer {
	key := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		panic(err) // unreachable: sha256 always yields a valid 32-byte AES-256 key
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		panic(err) // unreachable: NewGCM never fails for a valid block cipher
	}
	return &sealer{aead: gcm}
}

type sealedPayload struct {
	Kind string          `json:"k"`
	Exp  int64           `json:"e,omitempty"`
	Data json.RawMessage `json:"d"`
}

var errInvalidSealedToken = errors.New("invalid or expired token")

// seal encrypts v into a URL-safe token tagged with kind (so a token minted
// for one purpose, e.g. a DCR client, can't be replayed as another, e.g. an
// authorization code) and, when ttl is non-zero, an expiry.
func (s *sealer) seal(kind string, v any, ttl time.Duration) (string, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	payload := sealedPayload{Kind: kind, Data: data}
	if ttl > 0 {
		payload.Exp = time.Now().Add(ttl).Unix()
	}
	plain, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ct := s.aead.Seal(nonce, nonce, plain, nil)
	return base64.RawURLEncoding.EncodeToString(ct), nil
}

// open decrypts a token minted by seal, checking kind and expiry, and
// unmarshals its payload into v.
func (s *sealer) open(kind, token string, v any) error {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return errInvalidSealedToken
	}
	ns := s.aead.NonceSize()
	if len(raw) < ns {
		return errInvalidSealedToken
	}
	nonce, ct := raw[:ns], raw[ns:]
	plain, err := s.aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return errInvalidSealedToken
	}
	var payload sealedPayload
	if err := json.Unmarshal(plain, &payload); err != nil {
		return errInvalidSealedToken
	}
	if payload.Kind != kind {
		return errInvalidSealedToken
	}
	if payload.Exp != 0 && time.Now().Unix() > payload.Exp {
		return errInvalidSealedToken
	}
	return json.Unmarshal(payload.Data, v)
}

// generatePKCE creates a random verifier and its S256 challenge, for the
// proxy's own PKCE leg against the upstream Authorization Server.
func generatePKCE() (verifier, challenge string, err error) {
	b := make([]byte, 32)
	if _, err = rand.Read(b); err != nil {
		return "", "", err
	}
	verifier = base64.RawURLEncoding.EncodeToString(b)
	return verifier, pkceChallengeS256(verifier), nil
}

func pkceChallengeS256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// randomSecret generates a fallback sealer secret for when
// MAGICMARKETS_OAUTH_PROXY_SECRET is unset. It is per-process, so it only
// works for a single replica — see ServeHTTP's startup warning.
func randomSecret() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
