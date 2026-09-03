// Package config loads magicmarkets-cli configuration from .env files and the
// environment.
//
// Only an API key is needed — the Magic Markets v2 API authenticates with a
// single X-Api-Key header. There is no request signing and no private key.
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
	DefaultAPIURL = "https://magicmarkets.com/v2"
	DefaultLang   = "en"
)

// Config holds everything the client needs to talk to the API.
type Config struct {
	// APIKey is the X-Api-Key value. Created at magicmarkets.com under
	// Settings → API and shown only once.
	APIKey string

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
	cfg.APIURL = strings.TrimRight(firstEnv("MAGICMARKETS_API_URL", "MAGICMARKETS_BASE_URL"), "/")
	cfg.WSURL = firstEnv("MAGICMARKETS_WS_URL")
	cfg.Lang = firstEnv("MAGICMARKETS_LANG")

	if cfg.APIURL == "" {
		cfg.APIURL = DefaultAPIURL
	}
	if cfg.Lang == "" {
		cfg.Lang = DefaultLang
	}
	if cfg.WSURL == "" {
		cfg.WSURL = deriveWSURL(cfg.APIURL)
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

// RequireKey returns an actionable error when no API key is configured.
func (c *Config) RequireKey() error {
	if c.APIKey != "" {
		return nil
	}
	return fmt.Errorf("no API key configured\n\n" +
		"Set MAGICMARKETS_API_KEY in the environment or in one of:\n" +
		"  ./.env\n  ~/.magicmarkets/.env\n  ~/.env\n\n" +
		"Create a key at magicmarkets.com under Settings → API " +
		"(it is shown only once).")
}

// RedactedKey returns the key with all but the last 4 characters masked.
func (c *Config) RedactedKey() string {
	if c.APIKey == "" {
		return "(unset)"
	}
	if len(c.APIKey) <= 4 {
		return strings.Repeat("*", len(c.APIKey))
	}
	return strings.Repeat("*", len(c.APIKey)-4) + c.APIKey[len(c.APIKey)-4:]
}

// deriveWSURL turns an https REST base into its wss stream endpoint.
func deriveWSURL(apiURL string) string {
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
