package cli

import (
	"fmt"
	"log"
	"os"
	"time"

	"github.com/spf13/cobra"
	"magicmarkets-cli/internal/mcpserver"
)

func (a *App) newMCPCmd() *cobra.Command {
	var (
		timeout    time.Duration
		printTools bool
		httpMode   bool
		addr       string
	)

	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Serve the API as MCP tools over stdio or localhost HTTP",
		Long: `Expose the API as MCP tools so an LLM agent can use Magic Markets.

By default this speaks MCP over stdio: an MCP client launches "magicmarkets
mcp" as a subprocess and talks JSON-RPC on stdin/stdout. Pass --http to
instead serve the MCP streamable HTTP transport on --addr — useful for a
client that connects over the network, or one long-running server shared by
several clients instead of a subprocess per client.

Read-only tools are always available: balance, exchange rates, position, order
and betslip lookups, event and offer discovery, bet-type validation, and price
snapping.

Tools that spend money — create_betslip, place_order, close_order,
close_all_orders and the heartbeat tools — are registered only when
MAGICMARKETS_ALLOW_TRADING is set. Otherwise this process cannot place a bet,
and those tools do not appear in the client's tool list at all.

Register read-only with Claude Code:

  claude mcp add magicmarkets -e MAGICMARKETS_API_KEY=your-key -- magicmarkets mcp

Enable trading, replacing an existing registration:

  claude mcp remove magicmarkets
  claude mcp add magicmarkets -e MAGICMARKETS_API_KEY=your-key -e MAGICMARKETS_ALLOW_TRADING=1 -- magicmarkets mcp

Then restart the client — the subprocess's tool list is read at startup, so an
already-running session keeps the old one.

In a client's own MCP config file (mcpServers is the client's name for a
stdio subprocess, not a network service):

  {
    "mcpServers": {
      "magicmarkets": {
        "command": "magicmarkets",
        "args": ["mcp"],
        "env": {
          "MAGICMARKETS_API_KEY": "your-key",
          "MAGICMARKETS_ALLOW_TRADING": "1"
        }
      }
    }
  }

Serving over HTTP instead of stdio:

  magicmarkets mcp --http --addr 127.0.0.1:8383

--addr defaults to 127.0.0.1:8383 — loopback-only. The process does not
need MAGICMARKETS_API_KEY. Every MCP request must carry the caller's key
in an X-Api-Key header; that is the credential used against the Magic
Markets API. Point a client's MCP config at the URL:

  {
    "mcpServers": {
      "magicmarkets": {
        "url": "http://127.0.0.1:8383/mcp",
        "headers": { "X-Api-Key": "your-key" }
      }
    }
  }

Verify which tools would be registered with: magicmarkets mcp --print-tools`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// stdout is the JSON-RPC stream in stdio mode, so every log line
			// goes to stderr in both modes for consistency.
			log.SetOutput(os.Stderr)
			log.SetFlags(log.Ltime)

			trading := a.cfg.AllowTrading

			opts := mcpserver.Options{
				AllowTrading:    trading,
				Version:         a.version,
				SnapshotTimeout: timeout,
			}

			// --print-tools is a diagnostic: it answers "is trading on?" without
			// a client, and must not need a working key or open a session.
			if printTools {
				srv := mcpserver.New(nil, a.cfg, opts)
				return a.printToolList(srv, trading)
			}

			transport := "stdio"
			if httpMode {
				transport = fmt.Sprintf("http on %s", addr)
			}
			if trading {
				log.Printf("magicmarkets mcp starting (%s), "+
					"trading ENABLED via MAGICMARKETS_ALLOW_TRADING — this process can place real bets", transport)
			} else {
				log.Printf("magicmarkets mcp starting (%s), read-only — "+
					"set MAGICMARKETS_ALLOW_TRADING=1 to permit betting", transport)
			}

			if httpMode {
				// The process holds no API key. Callers send X-Api-Key; that
				// value is used for Magic Markets requests.
				return mcpserver.New(nil, a.cfg, opts).ServeHTTP(ctx(cmd), addr)
			}

			client, err := a.Client()
			if err != nil {
				return err
			}

			// Fail fast on a bad key rather than surfacing auth errors inside
			// every tool call.
			c, cancel := withTimeout(cmd, a.cfg.Timeout)
			defer cancel()
			if err := client.VerifyKey(c); err != nil {
				return err
			}

			return mcpserver.New(client, a.cfg, opts).Serve()
		},
	}

	fl := cmd.Flags()
	fl.DurationVar(&timeout, "snapshot-timeout", 30*time.Second,
		"how long the discovery tools wait on the stream")
	fl.BoolVar(&printTools, "print-tools", false,
		"list the tools that would be registered, then exit")
	fl.BoolVar(&httpMode, "http", false,
		"serve the MCP streamable HTTP transport instead of stdio")
	fl.StringVar(&addr, "addr", "127.0.0.1:8383",
		"address to listen on in --http mode")
	return cmd
}

// printToolList reports the mode and the tools that would be registered.
func (a *App) printToolList(srv *mcpserver.Server, trading bool) error {
	names := srv.ToolNames()

	if a.printer.JSON {
		return a.printer.Emit(map[string]any{
			"trading_enabled": trading,
			"enabled_by":      map[bool]any{true: "MAGICMARKETS_ALLOW_TRADING", false: nil}[trading],
			"tool_count":      len(names),
			"tools":           names,
		})
	}

	if trading {
		a.printer.Printf("mode: trading ENABLED (via MAGICMARKETS_ALLOW_TRADING) — this process can place real bets\n\n")
	} else {
		a.printer.Printf("mode: read-only — betting tools are NOT registered\n\n")
	}

	rows := make([][]string, 0, len(names))
	for _, n := range names {
		rows = append(rows, []string{n})
	}
	if err := a.printer.Table([]string{"TOOL"}, rows); err != nil {
		return err
	}

	if !trading {
		a.printer.Printf("\nTo allow betting, re-register magicmarkets mcp and restart your client:\n" +
			"  claude mcp remove magicmarkets\n" +
			"  claude mcp add magicmarkets -e MAGICMARKETS_API_KEY=your-key -e MAGICMARKETS_ALLOW_TRADING=1 -- magicmarkets mcp\n")
	}
	return nil
}
