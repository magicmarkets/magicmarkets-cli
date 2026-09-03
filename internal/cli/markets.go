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
		sports  []string
		search  string
		limit   int
		timeout time.Duration
		inPlay  bool
	)

	cmd := &cobra.Command{
		Use:     "markets",
		Short:   "List events that currently have prices",
		Aliases: []string{"events"},
		Long: `List the events the feed is currently pricing.

The v2 REST API has no event-listing endpoint — discovery happens over the
WebSocket. This command connects, drains the initial snapshot up to the sync
marker, prints the events, and disconnects.

The snapshot is not the full fixture list: it holds only events that currently
have live prices.

  magicmarkets markets --sport fb --limit 20
  magicmarkets markets --search arsenal
  magicmarkets markets --in-play`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c := ctx(cmd)

			stream, err := a.Stream(c)
			if err != nil {
				return err
			}
			defer stream.Close()

			events, err := stream.Snapshot(c, timeout)
			if err != nil {
				// A partial snapshot is still worth printing.
				if len(events) == 0 {
					return err
				}
				a.printer.Warnf("warning: snapshot incomplete: %v\n", err)
			}

			events = filterEvents(events, sports, search, inPlay)
			sort.Slice(events, func(i, j int) bool {
				if !events[i].StartTime.Equal(events[j].StartTime) {
					return events[i].StartTime.Before(events[j].StartTime)
				}
				return events[i].EventID < events[j].EventID
			})

			truncated := false
			if limit > 0 && len(events) > limit {
				events = events[:limit]
				truncated = true
			}

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
	fl.StringVar(&search, "search", "", "case-insensitive match on event, team or competition name")
	fl.IntVar(&limit, "limit", 50, "maximum events to print (0 for no limit)")
	fl.DurationVar(&timeout, "timeout", 30*time.Second, "how long to wait for the snapshot")
	fl.BoolVar(&inPlay, "in-play", false, "only events that are in play")
	return cmd
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

// filterEvents narrows a snapshot by sport, free text, and in-play status.
func filterEvents(events []magicmarkets.StreamEvent, sports []string, search string, inPlay bool) []magicmarkets.StreamEvent {
	wanted := make(map[string]bool, len(sports))
	for _, s := range sports {
		wanted[strings.ToLower(strings.TrimSpace(s))] = true
	}
	needle := strings.ToLower(search)

	out := make([]magicmarkets.StreamEvent, 0, len(events))
	for _, e := range events {
		if len(wanted) > 0 && !wanted[strings.ToLower(e.Sport)] {
			continue
		}
		// "pre_event" is the not-yet-started status; anything else with a
		// status set is live or finished.
		if inPlay && (e.IRStatus == "" || e.IRStatus == "pre_event") {
			continue
		}
		if needle != "" && !eventMatches(e, needle) {
			continue
		}
		out = append(out, e)
	}
	return out
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
		market  string
		timeout time.Duration
		limit   int
		depth   int
	)

	cmd := &cobra.Command{
		Use:   "offers <sport> <event-id>",
		Short: "List the priced bet types on an event",
		Long: `Register an event on the stream and print its offer snapshot.

Each offer covers one (sport, event_id, bet_type) triple. The bet_type string
fully identifies the market side, so back and lay on the same selection appear
as two separate rows.

Copy a bet_type straight from this output into ` + "`magicmarkets betslip create`" + ` — it
never needs to be constructed by hand.

  magicmarkets offers fb 2026-06-15,1001,2002
  magicmarkets offers fb 2026-06-15,1001,2002 --market ah --depth 3`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			sport, eventID := args[0], args[1]
			c := ctx(cmd)

			stream, err := a.Stream(c)
			if err != nil {
				return err
			}
			defer stream.Close()

			// Drain the initial snapshot first: register_event's reply cannot
			// be told apart from the opening dump otherwise.
			if _, err := stream.Snapshot(c, timeout); err != nil {
				return fmt.Errorf("waiting for initial sync: %w", err)
			}

			offers, err := stream.CollectOffers(c, sport, eventID, timeout)
			if err != nil {
				return err
			}

			if market != "" {
				filtered := offers[:0:0]
				for _, o := range offers {
					if strings.EqualFold(o.MarketType, market) {
						filtered = append(filtered, o)
					}
				}
				offers = filtered
			}

			sort.Slice(offers, func(i, j int) bool {
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
				a.printer.Printf("No offers — the feed has no prices for %s %s.\n", sport, eventID)
				return nil
			}

			rows := make([][]string, 0, len(offers))
			for _, o := range offers {
				levels := o.PriceList
				if depth > 0 && len(levels) > depth {
					levels = levels[:depth]
				}
				rows = append(rows, []string{
					o.BetType,
					dash(o.MarketType),
					boolMark(o.InRunning),
					formatLevels(levels),
					money(totalAvailable(o.PriceList)),
				})
			}
			return a.printer.Table(
				[]string{"BET TYPE", "MARKET", "IR", "PRICES (stake @ price)", "TOTAL"},
				rows,
			)
		},
	}

	fl := cmd.Flags()
	fl.StringVar(&market, "market", "", "only this market type, e.g. ah, ahou, 1x2")
	fl.DurationVar(&timeout, "timeout", 30*time.Second, "how long to wait for the snapshot")
	fl.IntVar(&limit, "limit", 0, "maximum offers to print (0 for no limit)")
	fl.IntVar(&depth, "depth", 3, "price levels to show per offer (0 for all)")
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
off-tick order price is rounded to the nearest valid tick that does not tighten
your limit: down for back (for) orders, up for lay (against) orders.

Use this to see the price an order will actually run with before placing it.

  magicmarkets ticks 2.345          # back order
  magicmarkets ticks 2.345 --lay    # lay order`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := strconv.ParseFloat(args[0], 64)
			if err != nil {
				return fmt.Errorf("invalid price %q: %w", args[0], err)
			}

			dir := magicmarkets.Back
			if lay {
				dir = magicmarkets.Lay
			}
			snapped := magicmarkets.SnapPrice(p, dir)

			result := map[string]any{
				"requested":     p,
				"direction":     string(dir),
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
				{"direction", string(dir)},
				{"already on tick", strconv.FormatBool(magicmarkets.IsOnTick(p))},
				{"snapped price", price(snapped)},
				{"tick at price", strconv.FormatFloat(magicmarkets.TickAt(snapped), 'f', -1, 64)},
				{"implied cents", strconv.FormatFloat(magicmarkets.ImpliedCents(snapped), 'f', 2, 64)},
			})
		},
	}

	cmd.Flags().BoolVar(&lay, "lay", false, "snap as a lay (against) order, rounding up")
	return cmd
}
