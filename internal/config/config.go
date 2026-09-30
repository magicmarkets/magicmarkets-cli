// Package config loads magicmarkets-cli configuration from .env files and the
// environment.
//
// The Magic Markets v2 API authenticates with an X-Api-Key header or an OAuth
// Bearer access token. There is no request signing. The WebSocket stream
// accepts the same credentials as REST.
package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Default endpoints for the public Magic Markets API.
const (
	DefaultAPIURL      = "https://magicmarkets.com/v2"
	DefaultLang        = "en"
	DefaultOAuthIssuer = "https://magicmarkets.com/api/auth"
)

// Config holds everything the client needs to talk to the API.
type Config struct {
	// APIKey is the X-Api-Key value. Created at magicmarkets.com under
	// Settings → API and shown only once.
	APIKey string

	// AccessToken is an OAuth Bearer token (MAGICMARKETS_ACCESS_TOKEN). Used
	// when APIKey is empty, for CLI and stdio MCP.
	AccessToken string

	// OAuthIssuer is the Magic Markets Authorization Server, advertised by
	// `magicmarkets mcp --http` so MCP clients can obtain a Bearer token.
	OAuthIssuer string

	// MCPPublicURL is the externally-reachable base of the HTTP MCP server
	// (no trailing /mcp). Used in OAuth protected-resource metadata.
	MCPPublicURL string

	// OAuthClientID is a pre-registered public client on the Magic Markets
	// Authorization Server. HTTP MCP's /authorize and /token substitute this
	// id when proxying Claude's Authorization Code flow upstream.
	OAuthClientID string

	// OAuthProxySecret seals the HTTP MCP OAuth proxy's DCR client_ids,
	// in-flight login state, and one-time codes so no server-side session
	// storage is needed. Every replica of a multi-replica deployment must
	// share the same value, or a request that lands on a different replica
	// than the one that minted a token can't decode it. A single-process
	// deployment can leave this unset — a random per-process key is used.
	OAuthProxySecret string

	// SessionGroupID (MAGICMARKETS_SESSION_GROUP_ID) is the environment-
	// specific id Magic Markets assigns to build the `session` value
	// ("m-<group id>-<user uuid>") an OAuth Bearer credential resolves to
	// via POST {OAuthIssuer}/oauth2/firebase-token — see
	// magicmarkets.MeResolver. It differs per environment and has no safe
	// default, so it must be set explicitly wherever an OAuth-authenticated
	// caller is expected.
	SessionGroupID string

	// FirebaseWebAPIKey (MAGICMARKETS_FIREBASE_WEB_API_KEY) is the Magic
	// Markets Firebase project's Web API key, used to redeem the Firebase
	// custom token from POST {OAuthIssuer}/oauth2/firebase-token for a real
	// Firebase ID token via Google's signInWithCustomToken — see
	// magicmarkets.MeResolver. It differs per environment (STG and prod are
	// different Firebase projects) and has no safe default, so it must be
	// set explicitly wherever an OAuth-authenticated caller is expected.
	FirebaseWebAPIKey string

	// BasicAuth (MAGICMARKETS_BASIC_AUTH) is the base64 "user:pass" token sent
	// as an additional Authorization: Basic header, on top of whatever
	// credential headers the request already carries. Some non-production
	// environments sit behind an infra-level HTTP Basic Auth wall in front
	// of the v2 API; production has none, so this is empty by default — see
	// magicmarkets.WithBasicAuth.
	BasicAuth string

	// APIURL is the REST base, including the /v2 suffix.
	APIURL string

	// WSURL is the WebSocket stream endpoint. Derived from APIURL when unset.
	WSURL string

	// Lang selects the language of event and competition names on the stream.
	Lang string

	// Timeout bounds a single REST request.
	Timeout time.Duration

	// AllowTrading enables the MCP tools that spend money, set via
	// MAGICMARKETS_ALLOW_TRADING. It affects `magicmarkets mcp` only — the CLI's
	// own trading commands are always available.
	AllowTrading bool

	// Loaded lists the .env files that were read, for `magicmarkets status`.
	Loaded []string
}

// envFiles returns the candidate .env paths in priority order. Earlier files
// win: values already present in the environment are never overwritten.
//
// This mirrors kalshi-cli's search order.
func envFiles() []string {
	paths := []string{".env"}
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths,
			filepath.Join(home, ".magicmarkets", ".env"),
			filepath.Join(home, ".env"),
		)
	}
	return paths
}

// Load reads .env files then resolves configuration from the environment.
//
// Precedence (highest first): real environment variables, ./.env,
// ~/.magicmarkets/.env, ~/.env, built-in defaults.
//
// A missing API key is not an error here — offline commands such as
// `magicmarkets api endpoints` work without one. Commands that need it call
// RequireKey.
func Load() (*Config, error) {
	cfg := &Config{}

	for _, path := range envFiles() {
		ok, err := loadDotenv(path)
		if err != nil {
			return nil, err
		}
		if ok {
			cfg.Loaded = append(cfg.Loaded, path)
		}
	}

	cfg.APIKey = firstEnv("MAGICMARKETS_API_KEY", "MAGICMARKETS_APIKEY", "MAGICMARKETS_API_KEY")
	cfg.AccessToken = firstEnv("MAGICMARKETS_ACCESS_TOKEN", "MAGICMARKETS_BEARER_TOKEN")
	cfg.OAuthIssuer = strings.TrimRight(firstEnv("MAGICMARKETS_OAUTH_ISSUER"), "/")
	cfg.OAuthClientID = firstEnv("MAGICMARKETS_OAUTH_CLIENT_ID")
	cfg.OAuthProxySecret = firstEnv("MAGICMARKETS_OAUTH_PROXY_SECRET")
	cfg.SessionGroupID = firstEnv("MAGICMARKETS_SESSION_GROUP_ID")
	cfg.FirebaseWebAPIKey = firstEnv("MAGICMARKETS_FIREBASE_WEB_API_KEY")
	cfg.BasicAuth = firstEnv("MAGICMARKETS_BASIC_AUTH")
	cfg.MCPPublicURL = strings.TrimRight(firstEnv("MAGICMARKETS_MCP_PUBLIC_URL"), "/")
	cfg.APIURL = strings.TrimRight(firstEnv("MAGICMARKETS_API_URL", "MAGICMARKETS_BASE_URL"), "/")
	cfg.WSURL = firstEnv("MAGICMARKETS_WS_URL")
	cfg.Lang = firstEnv("MAGICMARKETS_LANG")

	if cfg.APIURL == "" {
		cfg.APIURL = DefaultAPIURL
	}
	if cfg.Lang == "" {
		cfg.Lang = DefaultLang
	}
	if cfg.OAuthIssuer == "" {
		cfg.OAuthIssuer = DefaultOAuthIssuer
	}
	if cfg.WSURL == "" {
		cfg.WSURL = DeriveWSURL(cfg.APIURL)
	}

	cfg.Timeout = 30 * time.Second
	if v := firstEnv("MAGICMARKETS_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return nil, fmt.Errorf("MAGICMARKETS_TIMEOUT: %w", err)
		}
		cfg.Timeout = d
	}

	if v := firstEnv("MAGICMARKETS_ALLOW_TRADING"); v != "" {
		allow, err := parseBool(v)
		if err != nil {
			return nil, fmt.Errorf("MAGICMARKETS_ALLOW_TRADING: %w", err)
		}
		cfg.AllowTrading = allow
	}

	return cfg, nil
}

// parseBool reads a permissive boolean, so 1/true/yes/on all work.
//
// It rejects anything it does not recognise rather than defaulting to false: a
// typo here would silently leave trading disabled, which is exactly the
// confusion this setting is meant to remove.
func parseBool(v string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "y", "on":
		return true, nil
	case "0", "false", "no", "n", "off":
		return false, nil
	default:
		return false, fmt.Errorf("%q is not a boolean (use 1/true/yes or 0/false/no)", v)
	}
}

// RequireAuth returns an actionable error when neither an API key nor an
// OAuth access token is configured.
func (c *Config) RequireAuth() error {
	if c.APIKey != "" || c.AccessToken != "" {
		return nil
	}
	return fmt.Errorf("no credentials configured\n\n" +
		"Set MAGICMARKETS_API_KEY or MAGICMARKETS_ACCESS_TOKEN in the environment or in one of:\n" +
		"  ./.env\n  ~/.magicmarkets/.env\n  ~/.env\n\n" +
		"Create a key at magicmarkets.com under Settings → API " +
		"(it is shown only once), or obtain an OAuth access token from " +
		DefaultOAuthIssuer + ".")
}

// RequireKey is [Config.RequireAuth].
func (c *Config) RequireKey() error { return c.RequireAuth() }

func redactSecret(v string) string {
	if v == "" {
		return "(unset)"
	}
	if len(v) <= 4 {
		return strings.Repeat("*", len(v))
	}
	return strings.Repeat("*", len(v)-4) + v[len(v)-4:]
}

// RedactedKey returns the API key with all but the last 4 characters masked.
func (c *Config) RedactedKey() string { return redactSecret(c.APIKey) }

// RedactedAccessToken returns the OAuth token with all but the last 4 characters masked.
func (c *Config) RedactedAccessToken() string { return redactSecret(c.AccessToken) }

// DeriveWSURL turns an https REST base into its wss stream endpoint.
//
// Exported so the CLI can re-derive it after applying a --api-url override —
// PersistentPreRunE runs after Load, so the flag isn't visible to Load itself.
func DeriveWSURL(apiURL string) string {
	ws := apiURL
	switch {
	case strings.HasPrefix(ws, "https://"):
		ws = "wss://" + strings.TrimPrefix(ws, "https://")
	case strings.HasPrefix(ws, "http://"):
		ws = "ws://" + strings.TrimPrefix(ws, "http://")
	}
	return strings.TrimRight(ws, "/") + "/stream"
}

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := strings.TrimSpace(os.Getenv(k)); v != "" {
			return v
		}
	}
	return ""
}

// loadDotenv reads KEY=VALUE lines from path into the process environment
// without overwriting variables that are already set. It reports whether the
// file existed.
func loadDotenv(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		key, val, ok := parseDotenvLine(sc.Text())
		if !ok {
			continue
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		if err := os.Setenv(key, val); err != nil {
			return false, fmt.Errorf("set %s: %w", key, err)
		}
	}
	if err := sc.Err(); err != nil {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	return true, nil
}

// parseDotenvLine parses a single .env line, handling `export` prefixes,
// comments, and quoted values. It reports whether the line held an assignment.
func parseDotenvLine(line string) (key, value string, ok bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false
	}
	line = strings.TrimPrefix(line, "export ")

	key, value, found := strings.Cut(line, "=")
	if !found {
		return "", "", false
	}
	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)
	if key == "" {
		return "", "", false
	}

	// Quoted values are taken verbatim; unquoted values stop at a trailing
	// inline comment.
	switch {
	case len(value) >= 2 && strings.HasPrefix(value, `"`) && strings.HasSuffix(value, `"`):
		value = value[1 : len(value)-1]
	case len(value) >= 2 && strings.HasPrefix(value, `'`) && strings.HasSuffix(value, `'`):
		value = value[1 : len(value)-1]
	default:
		if i := strings.Index(value, " #"); i >= 0 {
			value = strings.TrimSpace(value[:i])
		}
	}
	return key, value, true
}
