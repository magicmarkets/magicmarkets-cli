package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// magicmarketsVars are the environment variables Load reads. Tests clear them so a
// developer's real key cannot leak into an assertion.
var magicmarketsVars = []string{
	"MAGICMARKETS_API_KEY", "MAGICMARKETS_APIKEY", "MAGICMARKETS_API_KEY",
	"MAGICMARKETS_API_URL", "MAGICMARKETS_BASE_URL", "MAGICMARKETS_WS_URL",
	"MAGICMARKETS_LANG", "MAGICMARKETS_TIMEOUT", "MAGICMARKETS_ALLOW_TRADING",
}

// isolate clears the MAGICMARKETS_* environment and points HOME and the working
// directory at fresh temp dirs, restoring everything afterwards.
//
// loadDotenv writes to the process environment, so without this the .env cases
// would leak into one another.
func isolate(t *testing.T) (workDir, homeDir string) {
	t.Helper()

	saved := make(map[string]string, len(magicmarketsVars))
	for _, k := range magicmarketsVars {
		if v, ok := os.LookupEnv(k); ok {
			saved[k] = v
		}
		os.Unsetenv(k)
	}
	t.Cleanup(func() {
		for _, k := range magicmarketsVars {
			if v, ok := saved[k]; ok {
				os.Setenv(k, v)
			} else {
				os.Unsetenv(k)
			}
		}
	})

	workDir = t.TempDir()
	homeDir = t.TempDir()
	t.Setenv("HOME", homeDir)

	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(workDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { os.Chdir(prev) })

	return workDir, homeDir
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestLoadDefaults(t *testing.T) {
	isolate(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.APIURL != DefaultAPIURL {
		t.Errorf("APIURL = %q, want %q", cfg.APIURL, DefaultAPIURL)
	}
	if want := "wss://magicmarkets.com/v2/stream"; cfg.WSURL != want {
		t.Errorf("WSURL = %q, want %q", cfg.WSURL, want)
	}
	if cfg.Lang != DefaultLang {
		t.Errorf("Lang = %q, want %q", cfg.Lang, DefaultLang)
	}
	// A missing key is not a load error; only RequireKey complains.
	if cfg.APIKey != "" {
		t.Errorf("APIKey = %q, want empty", cfg.APIKey)
	}
	if err := cfg.RequireKey(); err == nil {
		t.Error("RequireKey should fail when no key is configured")
	}
}

func TestLoadFromLocalDotenv(t *testing.T) {
	work, _ := isolate(t)
	writeFile(t, filepath.Join(work, ".env"), "MAGICMARKETS_API_KEY=from-local\n")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.APIKey != "from-local" {
		t.Errorf("APIKey = %q, want from-local", cfg.APIKey)
	}
	if err := cfg.RequireKey(); err != nil {
		t.Errorf("RequireKey: %v", err)
	}
}

func TestRealEnvironmentBeatsDotenv(t *testing.T) {
	work, _ := isolate(t)
	writeFile(t, filepath.Join(work, ".env"), "MAGICMARKETS_API_KEY=from-file\n")
	t.Setenv("MAGICMARKETS_API_KEY", "from-env")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.APIKey != "from-env" {
		t.Errorf("APIKey = %q, want from-env: the real environment must win", cfg.APIKey)
	}
}

func TestLocalDotenvBeatsHomeDotenv(t *testing.T) {
	work, home := isolate(t)
	writeFile(t, filepath.Join(work, ".env"), "MAGICMARKETS_API_KEY=from-cwd\n")
	writeFile(t, filepath.Join(home, ".magicmarkets", ".env"), "MAGICMARKETS_API_KEY=from-magicmarkets-dir\n")
	writeFile(t, filepath.Join(home, ".env"), "MAGICMARKETS_API_KEY=from-home\n")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.APIKey != "from-cwd" {
		t.Errorf("APIKey = %q, want from-cwd", cfg.APIKey)
	}
	if len(cfg.Loaded) != 3 {
		t.Errorf("Loaded = %v, want all three files recorded", cfg.Loaded)
	}
}

func TestHomeMagicMarketsDotenvUsedWhenNoLocalFile(t *testing.T) {
	_, home := isolate(t)
	writeFile(t, filepath.Join(home, ".magicmarkets", ".env"), "MAGICMARKETS_API_KEY=from-magicmarkets-dir\n")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.APIKey != "from-magicmarkets-dir" {
		t.Errorf("APIKey = %q, want from-magicmarkets-dir", cfg.APIKey)
	}
}

func TestWSURLDerivedFromCustomAPIURL(t *testing.T) {
	isolate(t)
	t.Setenv("MAGICMARKETS_API_URL", "https://staging.example.com/v2/")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if want := "https://staging.example.com/v2"; cfg.APIURL != want {
		t.Errorf("APIURL = %q, want %q (trailing slash trimmed)", cfg.APIURL, want)
	}
	if want := "wss://staging.example.com/v2/stream"; cfg.WSURL != want {
		t.Errorf("WSURL = %q, want %q", cfg.WSURL, want)
	}
}

func TestExplicitWSURLNotOverridden(t *testing.T) {
	isolate(t)
	t.Setenv("MAGICMARKETS_WS_URL", "ws://localhost:9000/v2/stream")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if want := "ws://localhost:9000/v2/stream"; cfg.WSURL != want {
		t.Errorf("WSURL = %q, want %q", cfg.WSURL, want)
	}
}

func TestInvalidTimeoutIsAnError(t *testing.T) {
	isolate(t)
	t.Setenv("MAGICMARKETS_TIMEOUT", "not-a-duration")

	if _, err := Load(); err == nil {
		t.Error("Load should reject an unparseable MAGICMARKETS_TIMEOUT")
	}
}

func TestAllowTradingDefaultsOff(t *testing.T) {
	isolate(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AllowTrading {
		t.Error("AllowTrading must default to false — trading is opt-in")
	}
}

func TestAllowTradingAcceptsTruthyValues(t *testing.T) {
	for _, v := range []string{"1", "true", "TRUE", "yes", "y", "on"} {
		t.Run(v, func(t *testing.T) {
			isolate(t)
			t.Setenv("MAGICMARKETS_ALLOW_TRADING", v)

			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if !cfg.AllowTrading {
				t.Errorf("MAGICMARKETS_ALLOW_TRADING=%q should enable trading", v)
			}
		})
	}
}

func TestAllowTradingAcceptsFalsyValues(t *testing.T) {
	for _, v := range []string{"0", "false", "no", "off"} {
		t.Run(v, func(t *testing.T) {
			isolate(t)
			t.Setenv("MAGICMARKETS_ALLOW_TRADING", v)

			cfg, err := Load()
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.AllowTrading {
				t.Errorf("MAGICMARKETS_ALLOW_TRADING=%q should not enable trading", v)
			}
		})
	}
}

func TestAllowTradingRejectsGarbage(t *testing.T) {
	// A typo must not silently read as "off": the whole point of the setting is
	// to remove doubt about whether betting is enabled.
	for _, v := range []string{"maybe", "2", "enabled", "-"} {
		isolate(t)
		t.Setenv("MAGICMARKETS_ALLOW_TRADING", v)

		if _, err := Load(); err == nil {
			t.Errorf("MAGICMARKETS_ALLOW_TRADING=%q should be rejected, not treated as false", v)
		}
	}
}

func TestDeriveWSURL(t *testing.T) {
	cases := map[string]string{
		"https://magicmarkets.com/v2": "wss://magicmarkets.com/v2/stream",
		"http://localhost:8080/v2":    "ws://localhost:8080/v2/stream",
		"https://host/v2/":            "wss://host/v2/stream",
	}
	for in, want := range cases {
		if got := deriveWSURL(in); got != want {
			t.Errorf("deriveWSURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseDotenvLine(t *testing.T) {
	cases := []struct {
		line     string
		key, val string
		ok       bool
	}{
		{`MAGICMARKETS_API_KEY=abc123`, "MAGICMARKETS_API_KEY", "abc123", true},
		{`  MAGICMARKETS_API_KEY = abc123 `, "MAGICMARKETS_API_KEY", "abc123", true},
		{`export MAGICMARKETS_API_KEY=abc123`, "MAGICMARKETS_API_KEY", "abc123", true},
		{`MAGICMARKETS_API_KEY="abc 123"`, "MAGICMARKETS_API_KEY", "abc 123", true},
		{`MAGICMARKETS_API_KEY='abc 123'`, "MAGICMARKETS_API_KEY", "abc 123", true},
		{`MAGICMARKETS_API_KEY=abc123 # inline comment`, "MAGICMARKETS_API_KEY", "abc123", true},
		// A quoted value keeps everything inside the quotes, '#' included.
		{`MAGICMARKETS_API_KEY="abc#123"`, "MAGICMARKETS_API_KEY", "abc#123", true},
		{`# a comment`, "", "", false},
		{``, "", "", false},
		{`   `, "", "", false},
		{`no_equals_sign`, "", "", false},
		{`=novalue`, "", "", false},
	}
	for _, c := range cases {
		key, val, ok := parseDotenvLine(c.line)
		if ok != c.ok || key != c.key || val != c.val {
			t.Errorf("parseDotenvLine(%q) = (%q, %q, %v), want (%q, %q, %v)",
				c.line, key, val, ok, c.key, c.val, c.ok)
		}
	}
}

func TestRedactedKey(t *testing.T) {
	cases := map[string]string{
		"":                "(unset)",
		"abc":             "***",
		"abcd":            "****",
		"secret-key-1234": "***********1234",
	}
	for key, want := range cases {
		cfg := &Config{APIKey: key}
		if got := cfg.RedactedKey(); got != want {
			t.Errorf("RedactedKey(%q) = %q, want %q", key, got, want)
		}
	}

	// Whatever the length, the secret must not survive redaction intact.
	cfg := &Config{APIKey: "supersecretvalue"}
	if strings.Contains(cfg.RedactedKey(), "supersecret") {
		t.Error("RedactedKey leaked the key")
	}
}
