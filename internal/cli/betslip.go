package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"magicmarkets-cli/internal/magicmarkets"
)

func (a *App) newBetslipCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "betslip",
		Short:   "Create and inspect betslips",
		Aliases: []string{"bs"},
		Long: `A betslip registers interest in one selection and receives a live quote.

Betslips are short-lived and carry no prices when created — the quote arrives
asynchronously. Use --wait to poll until it lands, or watch for ["pmm", ...]
messages on the stream.

A betslip is the prerequisite for an order: create one, read its price, then
place an order against it.`,
	}
	cmd.AddCommand(
		a.newBetslipCreateCmd(),
		a.newBetslipGetCmd(),
		a.newBetslipListCmd(),
		a.newBetslipRefreshCmd(),
	)
	return cmd
}

func (a *App) newBetslipCreateCmd() *cobra.Command {
	var (
		lay            bool
		parlay         []string
		userData       string
		excludeDanger  bool
		equivalentBets bool
		wait           time.Duration
	)

	cmd := &cobra.Command{
		Use:   "create [sport] [event-id] [bet-type]",
		Short: "Quote a selection",
		Long: `Create a betslip for one selection and print it.

Take the bet_type verbatim from ` + "`magicmarkets offers`" + ` or the stream — it encodes the
market, handicap, outcome and direction, and never needs to be built by hand.

  magicmarkets betslip create fb 2026-06-15,1001,2002 for,h
  magicmarkets betslip create fb 2026-06-15,1001,2002 for,ah,h,-4 --wait 5s
  magicmarkets betslip create --lay fb 2026-06-15,1001,2002 for,over,2.5

Parlays take 2–10 legs instead of positional arguments:

  magicmarkets betslip create \
    --leg fb:2026-06-15,1001,2002:for,h \
    --leg fb:2026-06-16,1003,2004:for,over,2.5`,
		Args: cobra.MaximumNArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			req := magicmarkets.CreateBetslipRequest{
				UserData:      userData,
				ExcludeDanger: excludeDanger,
			}
			if cmd.Flags().Changed("equivalent-bets") {
				req.EquivalentBets = &equivalentBets
			}

			switch {
			case len(parlay) > 0:
				if len(args) > 0 {
					return fmt.Errorf("use --leg for every leg of a parlay, not positional arguments")
				}
				legs, err := parseLegs(parlay)
				if err != nil {
					return err
				}
				req.BetslipType = magicmarkets.BetslipParlay
				req.Legs = legs

			default:
				if len(args) != 3 {
					return fmt.Errorf("need sport, event-id and bet-type (or --leg for a parlay)")
				}
				req.Sport, req.EventID, req.BetType = args[0], args[1], args[2]
				req.BetslipType = magicmarkets.BetslipNormal
				if lay {
					req.BetslipType = magicmarkets.BetslipLay
				}
			}

			client, err := a.Client()
			if err != nil {
				return err
			}
			c := ctx(cmd)

			bs, err := client.CreateBetslip(c, req)
			if err != nil {
				return err
			}

			// The create response never carries prices, so poll when asked.
			if wait > 0 {
				quoted, qerr := client.AwaitQuote(c, bs.BetslipID, wait, 250*time.Millisecond)
				if quoted != nil {
					bs = quoted
				}
				if qerr != nil {
					if a.printer.JSON {
						if err := a.printer.Emit(bs); err != nil {
							return err
						}
					} else if err := a.renderBetslip(bs); err != nil {
						return err
					}
					return qerr
				}
			}

			if a.printer.JSON {
				return a.printer.Emit(bs)
			}
			return a.renderBetslip(bs)
		},
	}

	fl := cmd.Flags()
	fl.BoolVar(&lay, "lay", false, "create a lay betslip")
	fl.StringArrayVar(&parlay, "leg", nil, "parlay leg as sport:event_id:bet_type (repeatable, 2–10)")
	fl.StringVar(&userData, "user-data", "", "opaque tag stored with the betslip (max 512 chars)")
	fl.BoolVar(&excludeDanger, "exclude-danger", false, "only quote from sources holding no bets in danger status")
	fl.BoolVar(&equivalentBets, "equivalent-bets", true, "include equivalent bet types in the quote")
	fl.DurationVar(&wait, "wait", 0, "poll until a price arrives, e.g. 5s")
	return cmd
}

// parseLegs parses parlay legs given as sport:event_id:bet_type.
//
// Event IDs and bet types both contain commas but no colons, so splitting on
// colons is unambiguous. Exactly three fields are required.
func parseLegs(specs []string) ([]magicmarkets.BetslipLeg, error) {
	legs := make([]magicmarkets.BetslipLeg, 0, len(specs))
	for _, s := range specs {
		parts := strings.Split(strings.TrimSpace(s), ":")
		if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
			return nil, fmt.Errorf("invalid --leg %q: want sport:event_id:bet_type, "+
				"e.g. fb:2026-06-15,1001,2002:for,h", s)
		}
		legs = append(legs, magicmarkets.BetslipLeg{Sport: parts[0], EventID: parts[1], BetType: parts[2]})
	}
	return legs, nil
}

func (a *App) newBetslipGetCmd() *cobra.Command {
	var wait time.Duration

	cmd := &cobra.Command{
		Use:   "get <betslip-id>",
		Short: "Show a betslip and its current prices",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.Client()
			if err != nil {
				return err
			}
			c := ctx(cmd)

			var bs *magicmarkets.Betslip
			if wait > 0 {
				bs, err = client.AwaitQuote(c, args[0], wait, 250*time.Millisecond)
			} else {
				bs, err = client.GetBetslip(c, args[0])
			}
			if bs == nil {
				return err
			}

			if a.printer.JSON {
				if emitErr := a.printer.Emit(bs); emitErr != nil {
					return emitErr
				}
			} else if renderErr := a.renderBetslip(bs); renderErr != nil {
				return renderErr
			}
			return err
		},
	}

	cmd.Flags().DurationVar(&wait, "wait", 0, "poll until a price arrives, e.g. 5s")
	return cmd
}

func (a *App) newBetslipListCmd() *cobra.Command {
	var expand bool

	cmd := &cobra.Command{
		Use:     "list",
		Short:   "List open betslip IDs",
		Aliases: []string{"ls"},
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := a.Client()
			if err != nil {
				return err
			}
			c := ctx(cmd)

			ids, err := client.ListBetslips(c)
			if err != nil {
				return err
			}

			if !expand {
				if a.printer.JSON {
					return a.printer.Emit(ids)
				}
				rows := make([][]string, 0, len(ids))
				for _, id := range ids {
					rows = append(rows, []string{id})
				}
				return a.printer.Table([]string{"BETSLIP ID"}, rows)
			}

			// --expand costs one request per betslip; the list endpoint
			// returns IDs only.
			slips := make([]*magicmarkets.Betslip, 0, len(ids))
			for _, id := range ids {
				bs, err := client.GetBetslip(c, id)
				if err != nil {
					a.printer.Warnf("warning: %s: %v\n", id, err)
					continue
				}
				slips = append(slips, bs)
			}

			if a.printer.JSON {
				return a.printer.Emit(slips)
			}
			rows := make([][]string, 0, len(slips))
			for _, bs := range slips {
				rows = append(rows, []string{
					bs.BetslipID,
					bs.Sport,
					bs.EventID,
					bs.BetType,
					price(bs.BestPrice()),
					money(bs.Total),
					until(bs.ExpiresAt()),
				})
			}
			return a.printer.Table(
				[]string{"BETSLIP ID", "SPORT", "EVENT ID", "BET TYPE", "BEST", "TOTAL", "EXPIRES"},
				rows,
			)
		},
	}

	cmd.Flags().BoolVar(&expand, "expand", false, "fetch each betslip in full (one request per ID)")
	return cmd
}

func (a *App) newBetslipRefreshCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "refresh <betslip-id>",
		Short: "Extend a betslip's expiry",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.Client()
			if err != nil {
				return err
			}
			c := ctx(cmd)

			bs, err := client.RefreshBetslip(c, args[0])
			if err != nil {
				return err
			}
			if a.printer.JSON {
				return a.printer.Emit(bs)
			}
			return a.renderBetslip(bs)
		},
	}
}

// renderBetslip prints a betslip as a summary block plus its price ladder.
func (a *App) renderBetslip(bs *magicmarkets.Betslip) error {
	kv := [][2]string{
		{"betslip id", bs.BetslipID},
		{"type", dash(bs.BetslipType)},
		{"sport", dash(bs.Sport)},
	}
	if bs.EventID != "" {
		kv = append(kv, [2]string{"event id", bs.EventID})
	}
	if bs.BetType != "" {
		kv = append(kv, [2]string{"bet type", bs.BetType})
	}
	if bs.BetTypeDescription != "" {
		kv = append(kv, [2]string{"description", bs.BetTypeDescription})
	}
	kv = append(kv,
		[2]string{"open", boolYesNo(bs.IsOpen)},
		[2]string{"expires", fmt.Sprintf("%s (%s)", localTime(bs.ExpiresAt()), until(bs.ExpiresAt()))},
	)
	if bs.CloseReason != nil && *bs.CloseReason != "" {
		kv = append(kv, [2]string{"close reason", *bs.CloseReason})
	}
	if bs.Total != nil {
		kv = append(kv, [2]string{"total available", bs.Total.String()})
	}
	if bs.UserData != nil && *bs.UserData != "" {
		kv = append(kv, [2]string{"user data", *bs.UserData})
	}
	if err := a.printer.KV(kv); err != nil {
		return err
	}

	if len(bs.Legs) > 0 {
		a.printer.Printf("\nLegs:\n")
		rows := make([][]string, 0, len(bs.Legs))
		for _, l := range bs.Legs {
			rows = append(rows, []string{
				l.Sport, l.EventID, l.BetType, dash(l.BetTypeDescription),
				pprice(l.Price), dash(l.Outcome),
			})
		}
		if err := a.printer.Table(
			[]string{"SPORT", "EVENT ID", "BET TYPE", "DESCRIPTION", "PRICE", "OUTCOME"}, rows); err != nil {
			return err
		}
	}

	a.printer.Printf("\nPrices:\n")
	if len(bs.PriceList) == 0 {
		a.printer.Printf("(no quote yet — quotes arrive asynchronously; retry with --wait 5s)\n")
		return nil
	}

	rows := make([][]string, 0, len(bs.PriceList))
	for _, l := range bs.PriceList {
		rows = append(rows, []string{
			price(l.Effective.Price),
			money(l.Effective.Min),
			money(l.Effective.Max),
		})
	}
	if err := a.printer.Table([]string{"PRICE", "MIN", "MAX"}, rows); err != nil {
		return err
	}

	best := bs.BestPrice()
	a.printer.Printf("\nPlace an order at the best price:\n  magicmarkets order place --betslip %s --price %s --stake <amount>\n",
		bs.BetslipID, price(best))
	return nil
}

func boolYesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
