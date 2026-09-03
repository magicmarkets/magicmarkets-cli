package mcpserver

import (
	"sort"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"magicmarkets-cli/internal/config"
	"magicmarkets-cli/internal/magicmarkets"
)

// tradingTools are the tools that can move money. They must never be reachable
// unless trading was explicitly enabled.
var tradingTools = []string{
	"create_betslip",
	"place_order",
	"close_order",
	"close_all_orders",
	"create_heartbeat",
	"refresh_heartbeat",
	"cancel_heartbeat",
	"list_heartbeats",
}

// readOnlyTools must always be available.
var readOnlyTools = []string{
	"get_balance",
	"get_exchange_rates",
	"get_position",
	"validate_bet_type",
	"snap_price",
	"list_events",
	"list_event_offers",
	"list_orders",
	"get_order",
	"list_betslips",
	"get_betslip",
}

// newTestServer registers the tool set onto a throwaway MCP server and lists
// what a client would see.
func newTestServer(t *testing.T, allowTrading bool) map[string]*mcp.Tool {
	t.Helper()

	cfg := &config.Config{
		APIKey:  "test-key",
		APIURL:  "https://example.invalid/v2",
		WSURL:   "wss://example.invalid/v2/stream",
		Lang:    "en",
		Timeout: time.Second,
	}
	client := magicmarkets.New(cfg.APIURL, cfg.APIKey, cfg.Timeout)

	s := New(client, cfg, Options{AllowTrading: allowTrading, Version: "test"})
	listed, err := listRegisteredTools(s.newMCP())
	if err != nil {
		t.Fatalf("list registered tools: %v", err)
	}
	tools := make(map[string]*mcp.Tool, len(listed))
	for _, tool := range listed {
		tools[tool.Name] = tool
	}
	return tools
}

func TestReadOnlyByDefault(t *testing.T) {
	tools := newTestServer(t, false)

	for _, name := range readOnlyTools {
		if _, ok := tools[name]; !ok {
			t.Errorf("read-only tool %q should always be registered", name)
		}
	}

	for _, name := range tradingTools {
		if _, ok := tools[name]; ok {
			t.Errorf("trading tool %q must NOT be registered without AllowTrading", name)
		}
	}
}

func TestTradingToolsRequireOptIn(t *testing.T) {
	tools := newTestServer(t, true)

	for _, name := range append(append([]string{}, readOnlyTools...), tradingTools...) {
		if _, ok := tools[name]; !ok {
			t.Errorf("tool %q should be registered with AllowTrading", name)
		}
	}
}

func TestEveryToolHasADescription(t *testing.T) {
	// A tool with no description is unusable by an agent.
	tools := newTestServer(t, true)
	if len(tools) == 0 {
		t.Fatal("no tools registered")
	}

	names := make([]string, 0, len(tools))
	for name := range tools {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		if tools[name].Description == "" {
			t.Errorf("tool %q has no description", name)
		}
	}
}

func TestMoneySpendingToolsAreMarkedDestructive(t *testing.T) {
	// The annotations are what an MCP client uses to decide whether to prompt,
	// so the ones that cancel or place bets must not claim to be read-only.
	tools := newTestServer(t, true)

	for _, name := range []string{"place_order", "close_order", "close_all_orders"} {
		tool, ok := tools[name]
		if !ok {
			t.Fatalf("tool %q not registered", name)
		}
		if tool.Annotations == nil {
			t.Fatalf("tool %q has no annotations", name)
		}
		if tool.Annotations.ReadOnlyHint {
			t.Errorf("tool %q is annotated read-only but it changes state", name)
		}
		if tool.Annotations.DestructiveHint == nil || !*tool.Annotations.DestructiveHint {
			t.Errorf("tool %q should carry a destructive hint", name)
		}
	}
}

func TestToolNamesMatchesRegistration(t *testing.T) {
	cfg := &config.Config{
		APIKey:  "test-key",
		APIURL:  "https://example.invalid/v2",
		WSURL:   "wss://example.invalid/v2/stream",
		Lang:    "en",
		Timeout: time.Second,
	}
	s := New(magicmarkets.New(cfg.APIURL, cfg.APIKey, cfg.Timeout), cfg, Options{Version: "test"})
	got := s.ToolNames()
	if len(got) != len(readOnlyTools) {
		t.Fatalf("ToolNames() = %v, want %d read-only tools", got, len(readOnlyTools))
	}
}
