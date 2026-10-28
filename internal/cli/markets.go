package cli

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"magicmarkets-cli/internal/magicmarkets"
)

func (a *App) newMarketsCmd() *cobra.Command {
	var (
		sports       []string
		competitions []int
		search       string
		limit        int
		inPlay       bool
		preEvent     bool
		from, to     string
	)

	cmd := &cobra.Command{
		Use:     "markets",
		Short:   "List events that currently have prices",
		Aliases: []string{"events"},
		Long: `List the events the feed is currently pricing (GET /v2/events/).

This is not the full fixture list: it holds only events that currently have
prices. Sport, competition, in-play status and start window are filtered by
the API; --search is matched locally while paging.

  magicmarkets markets --sport fb --limit 20
  magicmarkets markets --sport fb --from 2026-06-15 --to 2026-06-16
  magicmarkets markets --search arsenal
  magicmarkets markets --in-play`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := a.Client()
			if err != nil {
				return err
			}
			c := ctx(cmd)

			f := magicmarkets.EventFilter{
				Sport:         sports,
				CompetitionID: competitions,
				Lang:          a.cfg.Lang,
			}
			switch {
			case inPlay && preEvent:
				return fmt.Errorf("--in-play and --pre-event are mutually exclusive")
			case inPlay:
				f.IRStatus = magicmarkets.IRInRunning
			case preEvent:
				f.IRStatus = magicmarkets.IRPreEvent
			}
			if f.StartFrom, err = parseWhen("--from", from); err != nil {
				return err
			}
			if f.StartTo, err = parseWhen("--to", to); err != nil {
				return err
			}

			needle := strings.ToLower(search)
			pageSize := magicmarkets.MaxMarketPage
			if needle == "" && limit > 0 {
				pageSize = limit + 1
			}
			var (
				events    []magicmarkets.StreamEvent
				truncated bool
			)
			err = client.EachEvent(c, f, pageSize, "", func(e magicmarkets.StreamEvent) bool {
				if needle != "" && !eventMatches(e, needle) {
					return true
				}
				if limit > 0 && len(events) == limit {
					truncated = true
					return false
				}
				events = append(events, e)
				return true
			})
			if err != nil {
				return err
			}

			sort.SliceStable(events, func(i, j int) bool {
				return events[i].StartTime.Before(events[j].StartTime)
			})

			if a.printer.JSON {
				return a.printer.Emit(events)
			}

			rows := make([][]string, 0, len(events))
			for _, e := range events {
				rows = append(rows, []string{
					e.Sport,
					e.EventID,
					eventName(e),
					dash(e.CompetitionName),
					dash(e.IRStatus),
					localTime(e.StartTime),
				})
			}
			if err := a.printer.Table(
				[]string{"SPORT", "EVENT ID", "EVENT", "COMPETITION", "STATUS", "START"},
				rows,
			); err != nil {
				return err
			}
			if truncated {
				a.printer.Warnf("\n(showing %d events; raise --limit for more)\n", limit)
			}
			return nil
		},
	}

	fl := cmd.Flags()
	fl.StringSliceVar(&sports, "sport", nil, "only these sport codes, e.g. fb,tennis")
	fl.IntSliceVar(&competitions, "competition", nil, "only these competition IDs (requires --sport)")
	fl.StringVar(&search, "search", "", "case-insensitive match on event, team or competition name")
	fl.IntVar(&limit, "limit", 50, "maximum events to print (0 for no limit)")
	fl.BoolVar(&inPlay, "in-play", false, "only events that are in play")
	fl.BoolVar(&preEvent, "pre-event", false, "only events that have not started")
	fl.StringVar(&from, "from", "", "only events starting at or after this time (RFC 3339 or YYYY-MM-DD, UTC)")
	fl.StringVar(&to, "to", "", "only events starting before this time (RFC 3339 or YYYY-MM-DD, UTC)")
	fl.Duration("timeout", 30*time.Second, "unused")
	_ = fl.MarkDeprecated("timeout", "events are now read over REST; the request timeout is MAGICMARKETS_TIMEOUT")
	return cmd
}

// parseWhen accepts an RFC 3339 instant or a bare UTC date.
func parseWhen(flag, v string) (time.Time, error) {
	if v == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse(time.RFC3339, v); err == nil {
		return t, nil
	}
	if t, err := time.Parse(time.DateOnly, v); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("%s: want RFC 3339 (2026-06-15T15:00:00Z) or a date (2026-06-15), got %q", flag, v)
}

// eventName renders a feed event, preferring the home/away pair.
func eventName(e magicmarkets.StreamEvent) string {
	if e.Home != "" && e.Away != "" {
		return e.Home + " v " + e.Away
	}
	if e.EventName != "" {
		return e.EventName
	}
	if len(e.Teams) > 0 {
		return fmt.Sprintf("(%d runners)", len(e.Teams))
	}
	return "-"
}

// eventMatches reports whether any display field of e contains needle, which
// must already be lowercased.
func eventMatches(e magicmarkets.StreamEvent, needle string) bool {
	fields := []string{e.EventName, e.Home, e.Away, e.CompetitionName, e.EventID, e.CompetitionCountry}
	for _, f := range fields {
		if strings.Contains(strings.ToLower(f), needle) {
			return true
		}
	}
	for _, t := range e.Teams {
		if strings.Contains(strings.ToLower(t.Name), needle) {
			return true
		}
	}
	return false
}

func (a *App) newOffersCmd() *cobra.Command {
	var (
		markets      []string
		betTypes     []string
		minLiquidity float64
		limit        int
		depth        int
	)

	cmd := &cobra.Command{
		Use:   "offers <sport> <event-id>...",
		Short: "List the priced bet types on one or more events",
		Long: `Print the offers on one or more events of a sport (GET /v2/offers/).

Each offer covers one (event_id, bet_type) pair. The bet_type string fully
identifies the market side, so back and lay on the same selection appear as
two separate rows.

Copy a bet_type straight from this output into ` + "`magicmarkets betslip create`" + ` — it
never needs to be constructed by hand.

Prices can trail the live market by a few seconds. Use ` + "`magicmarkets stream --register`" + `
when every tick matters.

  magicmarkets offers fb 2026-06-15,1001,2002
  magicmarkets offers fb 2026-06-15,1001,2002 --market ah --depth 3
  magicmarkets offers fb 2026-06-15,1001,2002 2026-06-15,1003,2004 --min-liquidity 500
  magicmarkets offers fb 2026-06-15,1001,2002 --bet-type for,ah,h,-4`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			sport, eventIDs := args[0], args[1:]
			client, err := a.Client()
			if err != nil {
				return err
			}

			offers, err := client.AllOffers(ctx(cmd), magicmarkets.OfferFilter{
				Sport:        sport,
				EventID:      eventIDs,
				BetType:      betTypes,
				MarketType:   markets,
				MinLiquidity: minLiquidity,
			})
			if err != nil {
				return err
			}

			sort.SliceStable(offers, func(i, j int) bool {
				if offers[i].EventID != offers[j].EventID {
					return offers[i].EventID < offers[j].EventID
				}
				if offers[i].MarketType != offers[j].MarketType {
					return offers[i].MarketType < offers[j].MarketType
				}
				return offers[i].BetType < offers[j].BetType
			})

			if limit > 0 && len(offers) > limit {
				offers = offers[:limit]
			}

			if a.printer.JSON {
				return a.printer.Emit(offers)
			}
			if len(offers) == 0 {
				a.printer.Printf("No offers — the feed has no matching prices for %s %s.\n", sport, strings.Join(eventIDs, " "))
				return nil
			}

			multi := len(eventIDs) > 1
			header := []string{"BET TYPE", "MARKET", "IR", "PRICES (stake @ price)", "TOTAL"}
			if multi {
				header = append([]string{"EVENT ID"}, header...)
			}
			rows := make([][]string, 0, len(offers))
			for _, o := range offers {
				levels := o.PriceList
				if depth > 0 && len(levels) > depth {
					levels = levels[:depth]
				}
				row := []string{
					o.BetType,
					dash(o.MarketType),
					boolMark(o.InRunning),
					formatLevels(levels),
					money(totalAvailable(o.PriceList)),
				}
				if multi {
					row = append([]string{o.EventID}, row...)
				}
				rows = append(rows, row)
			}
			return a.printer.Table(header, rows)
		},
	}

	fl := cmd.Flags()
	fl.StringSliceVar(&markets, "market", nil, "only these market types, e.g. ah,ou")
	// bet_type values contain commas, so each --bet-type is taken verbatim.
	fl.StringArrayVar(&betTypes, "bet-type", nil, "only this exact bet type (repeatable), e.g. for,ah,h,-4")
	fl.Float64Var(&minLiquidity, "min-liquidity", 0, "only markets with at least this much USDT available across all prices")
	fl.IntVar(&limit, "limit", 0, "maximum offers to print (0 for no limit)")
	fl.IntVar(&depth, "depth", 3, "price levels to show per offer (0 for all)")
	fl.Duration("timeout", 30*time.Second, "unused")
	_ = fl.MarkDeprecated("timeout", "offers are now read over REST; the request timeout is MAGICMARKETS_TIMEOUT")
	return cmd
}

// formatLevels renders price levels as "max@price" pairs, best price first.
func formatLevels(levels []magicmarkets.PriceLevel) string {
	if len(levels) == 0 {
		return "-"
	}
	parts := make([]string, 0, len(levels))
	for _, l := range levels {
		parts = append(parts, fmt.Sprintf("%s@%s", money(l.Effective.Max), price(l.Effective.Price)))
	}
	return strings.Join(parts, "  ")
}

// totalAvailable sums the max stake across every price level.
func totalAvailable(levels []magicmarkets.PriceLevel) *magicmarkets.Stake {
	if len(levels) == 0 {
		return nil
	}
	total := magicmarkets.Stake{Currency: "USDT"}
	for _, l := range levels {
		if l.Effective.Max != nil {
			total.Currency = l.Effective.Max.Currency
			total.Amount += l.Effective.Max.Amount
		}
	}
	return &total
}

func boolMark(b bool) string {
	if b {
		return "yes"
	}
	return "-"
}

func (a *App) newTicksCmd() *cobra.Command {
	var lay bool

	cmd := &cobra.Command{
		Use:   "ticks <price>",
		Short: "Show where a price lands on the tick schedule",
		Long: `Snap a decimal price onto the API's tick schedule.

All prices lie on a fixed schedule whose tick widens as the price grows. An
off-tick limit on a normal betslip moves up to the next tick, as the server
does: 7.15 becomes 7.20. That holds for "for" and "against" bet types alike.
On a lay betslip (betslip create --lay) the price is a lay price, lower is
better, and an off-tick limit moves down: 2.31 becomes 2.30.

Use this to see the price an order will actually run with before placing it.

  magicmarkets ticks 2.345          # order on a normal betslip
  magicmarkets ticks 2.345 --lay    # order on a lay betslip`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := strconv.ParseFloat(args[0], 64)
			if err != nil {
				return fmt.Errorf("invalid price %q: %w", args[0], err)
			}

			betslipType := magicmarkets.BetslipNormal
			if lay {
				betslipType = magicmarkets.BetslipLay
			}
			snapped := magicmarkets.SnapOrderPrice(p, betslipType)

			result := map[string]any{
				"requested":     p,
				"betslip_type":  betslipType,
				"snapped":       snapped,
				"tick":          magicmarkets.TickAt(snapped),
				"on_tick":       magicmarkets.IsOnTick(p),
				"implied_cents": magicmarkets.ImpliedCents(snapped),
			}
			if a.printer.JSON {
				return a.printer.Emit(result)
			}

			return a.printer.KV([][2]string{
				{"requested", price(p)},
				{"betslip type", betslipType},
				{"already on tick", strconv.FormatBool(magicmarkets.IsOnTick(p))},
				{"snapped price", price(snapped)},
				{"tick at price", strconv.FormatFloat(magicmarkets.TickAt(snapped), 'f', -1, 64)},
				{"implied cents", strconv.FormatFloat(magicmarkets.ImpliedCents(snapped), 'f', 2, 64)},
			})
		},
	}

	cmd.Flags().BoolVar(&lay, "lay", false, "snap for an order on a lay betslip, rounding down")
	return cmd
}
