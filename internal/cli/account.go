package cli

import (
	"fmt"
	"os"
	"sort"
	"strconv"

	"github.com/spf13/cobra"
	"magicmarkets-cli/internal/magicmarkets"
)

func (a *App) newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show configuration and verify the API key",
		Long: `Show the resolved configuration and check the API key against the API.

The key is verified with GET /v2/xrates/, the cheapest authenticated endpoint.
Do this before opening a stream: the WebSocket rejects a bad key at the
handshake without a useful error.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			result := map[string]any{
				"api_url":   a.cfg.APIURL,
				"ws_url":    a.cfg.WSURL,
				"lang":      a.cfg.Lang,
				"api_key":   a.cfg.RedactedKey(),
				"env_files": a.cfg.Loaded,
				"version":   a.version,
			}

			// Verify only when a key is present, so `status` stays useful as a
			// way to diagnose a missing key.
			if a.cfg.APIKey == "" {
				result["authenticated"] = false
				result["error"] = "no API key configured"
			} else {
				client, err := a.Client()
				if err != nil {
					return err
				}
				c, cancel := withTimeout(cmd, a.cfg.Timeout)
				defer cancel()

				if err := client.VerifyKey(c); err != nil {
					result["authenticated"] = false
					result["error"] = err.Error()
				} else {
					result["authenticated"] = true
				}
			}

			if a.printer.JSON {
				return a.printer.Emit(result)
			}

			envFiles := "(none)"
			if len(a.cfg.Loaded) > 0 {
				envFiles = fmt.Sprint(a.cfg.Loaded)
			}
			auth := "yes"
			if result["authenticated"] != true {
				auth = "no — " + fmt.Sprint(result["error"])
			}
			if err := a.printer.KV([][2]string{
				{"version", a.version},
				{"api url", a.cfg.APIURL},
				{"ws url", a.cfg.WSURL},
				{"lang", a.cfg.Lang},
				{"api key", a.cfg.RedactedKey()},
				{"env files", envFiles},
				{"authenticated", auth},
			}); err != nil {
				return err
			}
			if result["authenticated"] != true {
				return fmt.Errorf("not authenticated")
			}
			return nil
		},
	}
}

func (a *App) newBalanceCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "balance",
		Short:   "Show account balance and open stake",
		Args:    cobra.NoArgs,
		Aliases: []string{"bal"},
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := a.Client()
			if err != nil {
				return err
			}
			c, cancel := withTimeout(cmd, a.cfg.Timeout)
			defer cancel()

			bal, err := client.GetBalance(c)
			if err != nil {
				return err
			}
			if a.printer.JSON {
				return a.printer.Emit(bal)
			}

			rows := [][2]string{
				{"balance", bal.Balance.String()},
				{"open stake", bal.OpenStake.String()},
			}
			if bal.SmartCredit != nil {
				rows = append(rows, [2]string{"smart credit", bal.SmartCredit.String()})
			}
			// Available is what the account can still commit.
			rows = append(rows, [2]string{"available",
				magicmarkets.Stake{Currency: bal.Balance.Currency, Amount: bal.Balance.Amount - bal.OpenStake.Amount}.String()})
			return a.printer.KV(rows)
		},
	}
}

func (a *App) newXRatesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "xrates",
		Short: "Show exchange rates to USDT",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := a.Client()
			if err != nil {
				return err
			}
			c, cancel := withTimeout(cmd, a.cfg.Timeout)
			defer cancel()

			rates, err := client.GetXRates(c)
			if err != nil {
				return err
			}
			if a.printer.JSON {
				return a.printer.Emit(rates)
			}

			sort.Slice(rates, func(i, j int) bool { return rates[i].Ccy < rates[j].Ccy })
			rows := make([][]string, 0, len(rates))
			for _, r := range rates {
				rows = append(rows, []string{r.Ccy, strconv.FormatFloat(r.Rate, 'f', -1, 64)})
			}
			return a.printer.Table([]string{"CCY", "RATE (USDT)"}, rows)
		},
	}
}

func (a *App) newPositionCmd() *cobra.Command {
	var (
		filter   magicmarkets.OrderFilter
		cashout  bool
		showGrid bool
	)

	cmd := &cobra.Command{
		Use:   "position",
		Short: "Show aggregate profit/loss position",
		Long: `Show the aggregate profit and loss position over the matching orders.

Without filters this covers every order, which is rarely useful — narrow it to
an event to get a readable payoff grid:

  magicmarkets position --sport fb --event 2026-06-15,1001,2002 --grid

Cashout valuations are offered on football only.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := a.Client()
			if err != nil {
				return err
			}
			c, cancel := withTimeout(cmd, a.cfg.Timeout)
			defer cancel()

			pos, err := client.GetPosition(c, filter, cashout)
			if err != nil {
				return err
			}
			if a.printer.JSON {
				return a.printer.Emit(pos)
			}
			return a.renderPosition(pos, showGrid)
		},
	}

	addOrderFilterFlags(cmd, &filter)
	cmd.Flags().BoolVar(&cashout, "cashout", false, "include a cashout valuation (football only)")
	cmd.Flags().BoolVar(&showGrid, "grid", false, "print the payoff grid per scoreline")
	return cmd
}

// renderPosition prints a position as a summary, a per-bet-type table, and
// optionally the payoff grid.
func (a *App) renderPosition(pos *magicmarkets.Position, showGrid bool) error {
	head := [][2]string{}
	if pos.Sport != "" {
		head = append(head, [2]string{"sport", pos.Sport})
	}
	if pos.EventID != "" {
		head = append(head, [2]string{"event", pos.EventID})
	}
	if pos.EventInfo != nil {
		head = append(head, [2]string{"match", eventLabel(pos.EventInfo)})
	}
	if pos.UnknownBetsNum > 0 {
		head = append(head, [2]string{"unprojected bets", strconv.Itoa(pos.UnknownBetsNum)})
	}
	if len(head) > 0 {
		if err := a.printer.KV(head); err != nil {
			return err
		}
		a.printer.Printf("\n")
	}

	if len(pos.Totals) == 0 {
		a.printer.Printf("No position — no orders matched the filters.\n")
		return nil
	}

	betTypes := make([]string, 0, len(pos.Totals))
	for bt := range pos.Totals {
		betTypes = append(betTypes, bt)
	}
	sort.Strings(betTypes)

	rows := make([][]string, 0, len(betTypes))
	for _, bt := range betTypes {
		t := pos.Totals[bt]
		rows = append(rows, []string{
			bt,
			dash(t.BetTypeDescription),
			pprice(t.GotPrice),
			money(t.GotStake),
			pprice(t.UnknownPrice),
			money(t.UnknownStake),
		})
	}
	if err := a.printer.Table(
		[]string{"BET TYPE", "DESCRIPTION", "PRICE", "STAKE", "UNK PRICE", "UNK STAKE"},
		rows,
	); err != nil {
		return err
	}

	if pos.CashoutInfo != nil {
		a.printer.Printf("\nCashout:\n")
		ci := pos.CashoutInfo
		kv := [][2]string{{"allowed", strconv.FormatBool(ci.Allowed)}}
		if ci.Reason != nil {
			kv = append(kv, [2]string{"reason", *ci.Reason})
		}
		if ci.Valuation != nil {
			kv = append(kv, [2]string{"valuation", ci.Valuation.String()})
		}
		if ci.Stake != nil {
			kv = append(kv, [2]string{"stake at risk", ci.Stake.String()})
		}
		if ci.SmartCreditDelta != nil {
			kv = append(kv, [2]string{"smart credit delta", ci.SmartCreditDelta.String()})
		}
		if err := a.printer.KV(kv); err != nil {
			return err
		}
	}

	if showGrid && pos.PayoffGrid != nil {
		a.printer.Printf("\nPayoff grid (%s), rows = home score, columns = away score:\n", pos.PayoffGrid.CcyCode)
		if err := a.renderGrid(pos.PayoffGrid); err != nil {
			return err
		}
	}
	return nil
}

// renderGrid prints a payoff grid as a matrix indexed by scoreline.
func (a *App) renderGrid(g *magicmarkets.PositionGrid) error {
	if g == nil || len(g.Values) == 0 {
		a.printer.Printf("(empty)\n")
		return nil
	}

	width := 0
	for _, row := range g.Values {
		if len(row) > width {
			width = len(row)
		}
	}

	headers := make([]string, 0, width+1)
	headers = append(headers, "H\\A")
	for i := 0; i < width; i++ {
		headers = append(headers, strconv.Itoa(i))
	}

	rows := make([][]string, 0, len(g.Values))
	for home, row := range g.Values {
		cells := make([]string, 0, width+1)
		cells = append(cells, strconv.Itoa(home))
		for away := 0; away < width; away++ {
			if away >= len(row) {
				cells = append(cells, "")
				continue
			}
			cells = append(cells, strconv.FormatFloat(row[away], 'f', 0, 64))
		}
		rows = append(rows, cells)
	}
	return a.printer.Table(headers, rows)
}

// addOrderFilterFlags wires the filter flags shared by `orders` and `position`.
func addOrderFilterFlags(cmd *cobra.Command, f *magicmarkets.OrderFilter) {
	fl := cmd.Flags()
	fl.StringSliceVar(&f.Status, "status", nil, "filter by status (open, pending, done, failed)")
	fl.StringSliceVar(&f.Sport, "sport", nil, "filter by sport code, e.g. fb")
	fl.StringSliceVar(&f.EventID, "event", nil, "filter by event ID")
	fl.StringSliceVar(&f.OrderType, "type", nil, "filter by order type (normal, lay, parlay)")
	fl.StringVar(&f.DateFrom, "from", "", "start of date range (ISO 8601)")
	fl.StringVar(&f.DateTo, "to", "", "end of date range (ISO 8601)")
	fl.StringVar(&f.Search, "search", "", "free-text search")
}

// confirm prompts for a yes/no answer on stderr, so piped stdout stays clean.
func confirm(prompt string) (bool, error) {
	fmt.Fprintf(os.Stderr, "%s [y/N]: ", prompt)
	var answer string
	if _, err := fmt.Scanln(&answer); err != nil {
		// A bare newline or closed stdin counts as "no".
		return false, nil
	}
	switch answer {
	case "y", "Y", "yes", "YES", "Yes":
		return true, nil
	}
	return false, nil
}
