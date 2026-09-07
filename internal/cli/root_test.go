package cli

import (
	"testing"

	"magicmarkets-cli/internal/config"
)

// --api-url alone must re-derive WSURL too, or REST and the stream silently
// split across two environments (e.g. staging REST, production WebSocket).
func TestApplyFlagOverridesRederivesWSURL(t *testing.T) {
	cfg := &config.Config{
		APIURL: config.DefaultAPIURL,
		WSURL:  config.DeriveWSURL(config.DefaultAPIURL),
	}

	applyFlagOverrides(cfg, "", "https://staging.magicmarkets.com/v2", "", false)

	wantAPI := "https://staging.magicmarkets.com/v2"
	wantWS := "wss://staging.magicmarkets.com/v2/stream"
	if cfg.APIURL != wantAPI {
		t.Errorf("APIURL = %q, want %q", cfg.APIURL, wantAPI)
	}
	if cfg.WSURL != wantWS {
		t.Errorf("WSURL = %q, want %q (still pointed at production)", cfg.WSURL, wantWS)
	}
}

// An explicit MAGICMARKETS_WS_URL must survive an --api-url override.
func TestApplyFlagOverridesKeepsExplicitWSURLEnv(t *testing.T) {
	cfg := &config.Config{
		APIURL: config.DefaultAPIURL,
		WSURL:  "wss://pinned.example/stream",
	}

	applyFlagOverrides(cfg, "", "https://staging.magicmarkets.com/v2", "", true)

	if want := "wss://pinned.example/stream"; cfg.WSURL != want {
		t.Errorf("WSURL = %q, want %q (explicit MAGICMARKETS_WS_URL should win)", cfg.WSURL, want)
	}
}

// --ws-url wins even over an --api-url derivation.
func TestApplyFlagOverridesWSURLFlagWins(t *testing.T) {
	cfg := &config.Config{APIURL: config.DefaultAPIURL, WSURL: config.DeriveWSURL(config.DefaultAPIURL)}

	applyFlagOverrides(cfg, "", "https://staging.magicmarkets.com/v2", "wss://custom.example/stream/", false)

	if want := "wss://custom.example/stream"; cfg.WSURL != want {
		t.Errorf("WSURL = %q, want %q", cfg.WSURL, want)
	}
}

func TestApplyFlagOverridesTrimsTrailingSlash(t *testing.T) {
	cfg := &config.Config{APIURL: config.DefaultAPIURL}

	applyFlagOverrides(cfg, "", "https://staging.magicmarkets.com/v2/", "", false)

	if want := "https://staging.magicmarkets.com/v2"; cfg.APIURL != want {
		t.Errorf("APIURL = %q, want %q", cfg.APIURL, want)
	}
}
