// Package cli implements the magicmarkets command tree.
package cli

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"
	"magicmarkets-cli/internal/config"
	"magicmarkets-cli/internal/magicmarkets"
)

// App carries state shared by every command: configuration, the output printer,
// and a lazily built API client.
type App struct {
	cfg     *config.Config
	printer *Printer

	jsonOut bool
	verbose bool

	// flag overrides, applied over the loaded config
	apiKeyFlag string
	apiURLFlag string

	version string

	client *magicmarkets.Client
}

// Client returns the REST client, building it on first use.
//
// It fails when no API key is configured, so offline commands must not call it.
func (a *App) Client() (*magicmarkets.Client, error) {
	if a.client != nil {
		return a.client, nil
	}
	if err := a.cfg.RequireKey(); err != nil {
		return nil, err
	}

	opts := []magicmarkets.Option{
		magicmarkets.WithUserAgent("magicmarkets-cli/" + a.version),
	}
	if a.verbose {
		opts = append(opts, magicmarkets.WithTrace(func(format string, args ...any) {
			a.printer.Warnf("→ "+format+"\n", args...)
		}))
	}

	a.client = magicmarkets.New(a.cfg.APIURL, a.cfg.APIKey, a.cfg.Timeout, opts...)
	return a.client, nil
}

// Stream opens the price feed.
//
// The key is verified over REST first, because the WebSocket rejects a bad key
// at the handshake without a useful error.
func (a *App) Stream(ctx context.Context) (*magicmarkets.Stream, error) {
	client, err := a.Client()
	if err != nil {
		return nil, err
	}
	if err := client.VerifyKey(ctx); err != nil {
		return nil, fmt.Errorf("API key rejected: %w", err)
	}
	if a.verbose {
		a.printer.Warnf("→ WS %s\n", a.cfg.WSURL)
	}
	return magicmarkets.Dial(ctx, a.cfg.WSURL, a.cfg.APIKey, a.cfg.Lang)
}

// ExecuteContext builds the command tree and runs it under ctx.
//
// The context carries signal handling, so long-running commands such as
// `stream` and `heartbeat run` stop cleanly on Ctrl-C.
func ExecuteContext(parent context.Context, version string) error {
	app := &App{
		version: version,
		printer: &Printer{Out: os.Stdout, Err: os.Stderr},
	}

	root := &cobra.Command{
		Use:   "magicmarkets",
		Short: "Command-line interface for the Magic Markets API",
		Long: `magicmarkets is a command-line interface for the Magic Markets v2 API.

Stream live prices, quote selections as betslips, place and manage orders, and
inspect your position — all authenticated with a single API key.

Authentication:
  Set MAGICMARKETS_API_KEY in the environment or in ./.env, ~/.magicmarkets/.env or ~/.env.
  Create a key at magicmarkets.com under Settings → API.

The two-step bet flow:
  A betslip registers interest in one selection and receives a live quote; an
  order then commits a stake against that quote. Always create the betslip
  first.

  magicmarkets markets --sport fb                  find events that have prices
  magicmarkets offers fb 2026-06-15,1001,2002      list bet types and prices
  magicmarkets betslip create fb <event> <bet>     quote one selection
  magicmarkets order place --betslip <id> ...      commit a stake`,
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       version,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if app.apiKeyFlag != "" {
				cfg.APIKey = app.apiKeyFlag
			}
			if app.apiURLFlag != "" {
				cfg.APIURL = app.apiURLFlag
			}
			app.cfg = cfg
			app.printer.JSON = app.jsonOut
			return nil
		},
	}

	pf := root.PersistentFlags()
	pf.BoolVar(&app.jsonOut, "json", false, "output machine-readable JSON")
	pf.BoolVarP(&app.verbose, "verbose", "v", false, "log requests to stderr")
	pf.StringVar(&app.apiKeyFlag, "api-key", "", "API key (overrides MAGICMARKETS_API_KEY)")
	pf.StringVar(&app.apiURLFlag, "api-url", "", "REST base URL (overrides MAGICMARKETS_API_URL)")

	root.AddCommand(
		// Account
		app.newStatusCmd(),
		app.newBalanceCmd(),
		app.newXRatesCmd(),
		app.newPositionCmd(),

		// Discovery
		app.newMarketsCmd(),
		app.newOffersCmd(),
		app.newStreamCmd(),

		// Trading
		app.newBetslipCmd(),
		app.newOrderCmd(),
		app.newOrdersCmd(),

		// Risk
		app.newHeartbeatCmd(),

		// Reference
		app.newBetTypeCmd(),
		app.newTicksCmd(),
		app.newAPICmd(),

		// Integrations
		app.newMCPCmd(),
	)

	return root.ExecuteContext(parent)
}

// ctx returns the command's context, which cobra wires to signal handling in
// main.
func ctx(cmd *cobra.Command) context.Context {
	if c := cmd.Context(); c != nil {
		return c
	}
	return context.Background()
}

// withTimeout derives a bounded context for a single request.
func withTimeout(cmd *cobra.Command, d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx(cmd), d)
}
