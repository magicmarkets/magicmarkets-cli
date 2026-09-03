package cli

import (
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
	)

	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Serve the API as MCP tools over stdio",
		Long: `Expose the API as MCP tools over stdio so an LLM agent can use Magic Markets.

This is not a standalone server: it does not listen on a port. An MCP client
launches "magicmarkets mcp" as a subprocess and talks JSON-RPC on stdin/stdout.
There is no HTTP or SSE transport.

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

Verify which tools would be registered with: magicmarkets mcp --print-tools`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// stdout is the JSON-RPC stream, so every log line goes to stderr.
			log.SetOutput(os.Stderr)
			log.SetFlags(log.Ltime)

			trading := a.cfg.AllowTrading

			opts := mcpserver.Options{
				AllowTrading:    trading,
				Version:         a.version,
				SnapshotTimeout: timeout,
			}

			// --print-tools is a diagnostic: it answers "is trading on?" without
			// a client, and must not need a working key or open a stdio session.
			if printTools {
				srv := mcpserver.New(nil, a.cfg, opts)
				return a.printToolList(srv, trading)
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

			if trading {
				log.Printf("magicmarkets mcp starting (stdio), " +
					"trading ENABLED via MAGICMARKETS_ALLOW_TRADING — this process can place real bets")
			} else {
				log.Printf("magicmarkets mcp starting (stdio), read-only — " +
					"set MAGICMARKETS_ALLOW_TRADING=1 to permit betting")
			}

			srv := mcpserver.New(client, a.cfg, opts)
			return srv.Serve()
		},
	}

	fl := cmd.Flags()
	fl.DurationVar(&timeout, "snapshot-timeout", 30*time.Second,
		"how long the discovery tools wait on the stream")
	fl.BoolVar(&printTools, "print-tools", false,
		"list the tools that would be registered, then exit")
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
