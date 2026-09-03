package cli

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"magicmarkets-cli/internal/magicmarkets"
)

func (a *App) newOrderCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "order",
		Short: "Place, inspect and cancel orders",
		Long: `An order commits a stake against a betslip's quote.

Create the betslip first — an order always references one. Order placement asks
for confirmation unless --yes is given.`,
	}
	cmd.AddCommand(
		a.newOrderPlaceCmd(),
		a.newOrderGetCmd(),
		a.newOrderListCmd(),
		a.newOrderTrackedCmd(),
		a.newOrderUpdatesCmd(),
		a.newOrderCloseCmd(),
		a.newOrderCloseManyCmd(),
		a.newOrderCloseAllCmd(),
	)
	return cmd
}

// newOrdersCmd is a top-level alias for `order list`, matching how the plural
// reads in day-to-day use.
func (a *App) newOrdersCmd() *cobra.Command {
	cmd := a.newOrderListCmd()
	cmd.Use = "orders"
	cmd.Short = "List orders (alias for `order list`)"
	cmd.Aliases = nil
	return cmd
}

func (a *App) newOrderPlaceCmd() *cobra.Command {
	var (
		betslipID    string
		priceFlag    float64
		stakeFlag    float64
		currency     string
		duration     int
		exchangeMode string
		keepOpenIR   bool
		userData     string
		requestUUID  string
		partialFill  bool
		betterPrice  bool
		forceWant    bool
		minTaker     float64
		currentScore string
		excludeDgr   bool
		placerType   string
		assumeYes    bool
		watch        time.Duration
	)

	cmd := &cobra.Command{
		Use:   "place",
		Short: "Place an order against a betslip",
		Long: `Place an order against an existing betslip.

This commits real money. The order is summarised and confirmed before it is
sent, unless --yes is given.

An off-tick price is snapped onto the tick schedule — down for back (for)
orders, up for lay (against) orders. The summary shows the snapped price, which
is the one the order runs with.

  magicmarkets order place --betslip <id> --price 2.10 --stake 50
  magicmarkets order place --betslip <id> --price 2.10 --stake 50 --yes --watch 20s

Use --request-uuid for idempotency: retrying with the same UUID will not create
a duplicate, and the order stays retrievable via ` + "`magicmarkets order tracked`" + ` for six
hours.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := a.Client()
			if err != nil {
				return err
			}
			c := ctx(cmd)

			// Fetch the betslip so the confirmation can show what is being bet
			// on, and so the price can be checked against the live quote.
			bs, err := client.GetBetslip(c, betslipID)
			if err != nil {
				return fmt.Errorf("look up betslip %s: %w", betslipID, err)
			}

			dir := magicmarkets.DirectionOf(bs.BetType)
			snapped := magicmarkets.SnapPrice(priceFlag, dir)

			score, err := parseCurrentScore(currentScore)
			if err != nil {
				return err
			}

			req := magicmarkets.CreateOrderRequest{
				BetslipID:      betslipID,
				Price:          snapped,
				Stake:          magicmarkets.Stake{Currency: currency, Amount: stakeFlag},
				Duration:       float64(duration),
				ExchangeMode:   exchangeMode,
				KeepOpenIR:     keepOpenIR,
				UserData:       userData,
				RequestUUID:    requestUUID,
				ForceWantPrice: forceWant,
				CurrentScore:   score,
				ExcludeDanger:  excludeDgr,
				PlacerType:     placerType,
			}
			if cmd.Flags().Changed("accept-partial-fill") {
				req.AcceptPartialFill = &partialFill
			}
			if cmd.Flags().Changed("accept-better-price") {
				req.AcceptBetterPrice = &betterPrice
			}
			if cmd.Flags().Changed("min-taker-stake") {
				s := magicmarkets.Stake{Currency: currency, Amount: minTaker}
				req.MinTakerWantStake = &s
			}
			if err := req.Validate(); err != nil {
				return err
			}

			if !assumeYes {
				ok, err := a.confirmOrder(bs, req, dir, priceFlag, snapped)
				if err != nil {
					return err
				}
				if !ok {
					a.printer.Warnf("aborted\n")
					return nil
				}
			}

			order, err := client.CreateOrder(c, req)
			if err != nil {
				// A reused idempotency key is a success in disguise: the
				// original order exists and can be fetched.
				if magicmarkets.HasCode(err, magicmarkets.CodeOrderAlreadyCreated) {
					a.printer.Warnf("this request_uuid already created an order; fetching it\n")
					if existing, gerr := client.GetOrderByUUID(c, requestUUID); gerr == nil {
						order = existing
					} else {
						return err
					}
				} else {
					return err
				}
			}

			if watch > 0 {
				order = a.watchOrder(c, client, order, watch)
			}

			if a.printer.JSON {
				return a.printer.Emit(order)
			}
			return a.renderOrder(order)
		},
	}

	fl := cmd.Flags()
	fl.StringVar(&betslipID, "betslip", "", "betslip ID to order against (required)")
	fl.Float64Var(&priceFlag, "price", 0, "desired decimal price (required)")
	fl.Float64Var(&stakeFlag, "stake", 0, "stake amount (required)")
	fl.StringVar(&currency, "currency", "USDT", "stake currency")
	fl.IntVar(&duration, "duration", 15, "seconds the order stays open")
	fl.StringVar(&exchangeMode, "exchange-mode", "", "make_and_take, take_only or dark")
	fl.BoolVar(&keepOpenIR, "keep-open-ir", false, "keep the order open when the event goes in-play")
	fl.StringVar(&userData, "user-data", "", "opaque tag stored with the order (max 512 chars)")
	fl.StringVar(&requestUUID, "request-uuid", "", "idempotency key")
	fl.BoolVar(&partialFill, "accept-partial-fill", true, "accept a partial fill")
	fl.BoolVar(&betterPrice, "accept-better-price", true, "accept a better price than requested")
	fl.BoolVar(&forceWant, "force-want-price", false, "do not improve on the requested price")
	fl.Float64Var(&minTaker, "min-taker-stake", 0, "minimum stake to take as a taker")
	fl.StringVar(&currentScore, "current-score", "", "current score as HOME-AWAY (e.g. 1-2), guarding in-play orders")
	fl.BoolVar(&excludeDgr, "exclude-danger", false, "only use sources holding no bets in danger status")
	fl.StringVar(&placerType, "placer-type", "", "optional caller-supplied tag recorded against the order")
	fl.BoolVarP(&assumeYes, "yes", "y", false, "skip the confirmation prompt")
	fl.DurationVar(&watch, "watch", 0, "poll the order until it settles, e.g. 20s")

	_ = cmd.MarkFlagRequired("betslip")
	_ = cmd.MarkFlagRequired("price")
	_ = cmd.MarkFlagRequired("stake")
	return cmd
}

// confirmOrder summarises an order and asks the user to approve it.
func (a *App) confirmOrder(bs *magicmarkets.Betslip, req magicmarkets.CreateOrderRequest, dir magicmarkets.Direction, requested, snapped float64) (bool, error) {
	side := "BACK"
	if dir == magicmarkets.Lay {
		side = "LAY"
	}

	kv := [][2]string{
		{"side", side},
		{"sport", dash(bs.Sport)},
		{"event id", dash(bs.EventID)},
		{"bet type", dash(bs.BetType)},
	}
	if bs.BetTypeDescription != "" {
		kv = append(kv, [2]string{"selection", bs.BetTypeDescription})
	}
	kv = append(kv, [2]string{"price", price(snapped)})
	if snapped != requested {
		kv = append(kv, [2]string{"", fmt.Sprintf("(requested %s, snapped %s to the tick schedule)",
			price(requested), map[bool]string{true: "up", false: "down"}[snapped > requested])})
	}
	kv = append(kv,
		[2]string{"stake", req.Stake.String()},
		[2]string{"duration", strconv.FormatFloat(req.Duration, 'f', -1, 64) + "s"},
	)
	if req.CurrentScore != nil {
		kv = append(kv, [2]string{"current score guard", fmt.Sprintf("%d-%d", req.CurrentScore[0], req.CurrentScore[1])})
	}
	// Payout on a winning back bet is stake x price; the profit is the rest.
	payout := req.Stake.Amount * snapped
	kv = append(kv,
		[2]string{"potential return", magicmarkets.Stake{Currency: req.Stake.Currency, Amount: payout}.String()},
		[2]string{"potential profit", magicmarkets.Stake{Currency: req.Stake.Currency, Amount: payout - req.Stake.Amount}.String()},
	)

	if best := bs.BestPrice(); best > 0 {
		kv = append(kv, [2]string{"best quoted", price(best)})
		if dir == magicmarkets.Back && snapped > best {
			kv = append(kv, [2]string{"", "note: your price is above the best quote; it may not fill"})
		}
	} else {
		kv = append(kv, [2]string{"best quoted", "(betslip is currently unquoted)"})
	}
	if !bs.IsOpen {
		kv = append(kv, [2]string{"warning", "this betslip is closed"})
	}

	a.printer.Warnf("\nOrder to place:\n")
	if err := a.printer.KV(kv); err != nil {
		return false, err
	}
	a.printer.Warnf("\n")
	return confirm("Place this order? This commits real money.")
}

// watchOrder polls an order until it leaves the open state or the budget runs
// out, returning the last state seen.
func (a *App) watchOrder(c context.Context, client *magicmarkets.Client, order *magicmarkets.Order, budget time.Duration) *magicmarkets.Order {
	if order == nil {
		return nil
	}
	deadline := time.Now().Add(budget)
	latest := order

	for time.Now().Before(deadline) {
		if latest.Status != magicmarkets.StatusOpen && latest.Status != magicmarkets.StatusPending {
			return latest
		}
		select {
		case <-c.Done():
			return latest
		case <-time.After(500 * time.Millisecond):
		}
		next, err := client.GetOrder(c, latest.OrderID)
		if err != nil {
			a.printer.Warnf("warning: polling order %d: %v\n", latest.OrderID, err)
			return latest
		}
		latest = next
	}
	return latest
}

func (a *App) newOrderGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <order-id>",
		Short: "Show a single order",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseOrderID(args[0])
			if err != nil {
				return err
			}
			client, err := a.Client()
			if err != nil {
				return err
			}
			c := ctx(cmd)

			order, err := client.GetOrder(c, id)
			if err != nil {
				return err
			}
			if a.printer.JSON {
				return a.printer.Emit(order)
			}
			return a.renderOrder(order)
		},
	}
}

func (a *App) newOrderTrackedCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tracked <request-uuid>",
		Short: "Show an order by the request UUID it was created with",
		Long: `Retrieve an order by its idempotency key.

Works for up to six hours after placement. Use this to recover from a timeout
or a retry without risking a duplicate order.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.Client()
			if err != nil {
				return err
			}
			c := ctx(cmd)

			order, err := client.GetOrderByUUID(c, args[0])
			if err != nil {
				return err
			}
			if a.printer.JSON {
				return a.printer.Emit(order)
			}
			return a.renderOrder(order)
		},
	}
}

func (a *App) newOrderListCmd() *cobra.Command {
	var (
		filter   magicmarkets.OrderFilter
		page     int
		pageSize int
		open     bool
	)

	cmd := &cobra.Command{
		Use:     "list",
		Short:   "List orders",
		Aliases: []string{"ls"},
		Long: `List orders, most useful when filtered.

  magicmarkets order list --open
  magicmarkets order list --status done --sport fb
  magicmarkets order list --event 2026-06-15,1001,2002`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if open {
				filter.Status = append(filter.Status, magicmarkets.StatusOpen, magicmarkets.StatusPending)
			}
			client, err := a.Client()
			if err != nil {
				return err
			}
			c := ctx(cmd)

			orders, err := client.ListOrders(c, filter, page, pageSize)
			if err != nil {
				return err
			}
			if a.printer.JSON {
				return a.printer.Emit(orders)
			}
			return a.renderOrderTable(orders)
		},
	}

	addOrderFilterFlags(cmd, &filter)
	fl := cmd.Flags()
	fl.IntVar(&page, "page", 1, "page number")
	fl.IntVar(&pageSize, "page-size", 25, "results per page")
	fl.BoolVar(&open, "open", false, "shorthand for --status open,pending")
	return cmd
}

func (a *App) newOrderUpdatesCmd() *cobra.Command {
	var (
		from   string
		to     string
		window time.Duration
	)

	cmd := &cobra.Command{
		Use:   "updates",
		Short: "List orders updated in a time window",
		Long: `List orders whose state changed within a time window.

The API requires both bounds to be at least 60 seconds in the past and no more
than 70 minutes apart. Without --from/--to this looks at the most recent legal
window, ending 60 seconds ago.

  magicmarkets order updates
  magicmarkets order updates --window 30m
  magicmarkets order updates --from 2026-07-28T10:00:00Z --to 2026-07-28T10:30:00Z`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// The window must end at least 60s in the past; leave a margin so
			// clock skew does not trip the server-side check.
			end := time.Now().Add(-65 * time.Second)
			start := end.Add(-window)

			var err error
			if to != "" {
				end, err = time.Parse(time.RFC3339, to)
				if err != nil {
					return fmt.Errorf("invalid --to %q: want RFC 3339, e.g. 2026-07-28T10:00:00Z", to)
				}
			}
			if from != "" {
				start, err = time.Parse(time.RFC3339, from)
				if err != nil {
					return fmt.Errorf("invalid --from %q: want RFC 3339, e.g. 2026-07-28T10:00:00Z", from)
				}
			} else if to != "" {
				start = end.Add(-window)
			}

			client, err := a.Client()
			if err != nil {
				return err
			}
			c := ctx(cmd)

			orders, err := client.OrderUpdates(c, start, end)
			if err != nil {
				return err
			}
			if a.printer.JSON {
				return a.printer.Emit(orders)
			}
			a.printer.Warnf("window %s → %s\n\n",
				start.UTC().Format(time.RFC3339), end.UTC().Format(time.RFC3339))
			return a.renderOrderTable(orders)
		},
	}

	fl := cmd.Flags()
	fl.StringVar(&from, "from", "", "window start (RFC 3339)")
	fl.StringVar(&to, "to", "", "window end (RFC 3339)")
	fl.DurationVar(&window, "window", 10*time.Minute, "window length when --from is omitted (max 70m)")
	return cmd
}

func (a *App) newOrderCloseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "close <order-id>",
		Short: "Cancel a single open order",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := parseOrderID(args[0])
			if err != nil {
				return err
			}
			client, err := a.Client()
			if err != nil {
				return err
			}
			c := ctx(cmd)

			if err := client.CloseOrder(c, id); err != nil {
				// order_closed means the order exists but was already done;
				// say so plainly rather than surfacing a bare error code.
				if magicmarkets.HasCode(err, magicmarkets.CodeOrderClosed) {
					return fmt.Errorf("order %d is already closed or settled", id)
				}
				return err
			}

			// The close response carries no data, so re-read the order to show
			// its final state.
			order, err := client.GetOrder(c, id)
			if err != nil {
				a.printer.Warnf("closed order %d, but could not re-read it: %v\n", id, err)
				return nil
			}
			if a.printer.JSON {
				return a.printer.Emit(order)
			}
			a.printer.Printf("Closed order %d\n\n", id)
			return a.renderOrder(order)
		},
	}
}

func (a *App) newOrderCloseManyCmd() *cobra.Command {
	var assumeYes bool

	cmd := &cobra.Command{
		Use:   "close-many <order-id>...",
		Short: "Cancel several orders by ID",
		Long:  "Cancel up to 500 orders in one request.",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ids := make([]int64, 0, len(args))
			for _, arg := range args {
				// Accept comma-separated lists as well as separate arguments.
				for _, part := range strings.Split(arg, ",") {
					part = strings.TrimSpace(part)
					if part == "" {
						continue
					}
					id, err := parseOrderID(part)
					if err != nil {
						return err
					}
					ids = append(ids, id)
				}
			}

			if !assumeYes {
				ok, err := confirm(fmt.Sprintf("Cancel %d order(s)?", len(ids)))
				if err != nil {
					return err
				}
				if !ok {
					a.printer.Warnf("aborted\n")
					return nil
				}
			}

			client, err := a.Client()
			if err != nil {
				return err
			}
			c := ctx(cmd)

			result, err := client.CloseManyOrders(c, ids)
			if err != nil {
				return err
			}
			if a.printer.JSON {
				return a.printer.Emit(result)
			}
			a.printer.Printf("Requested cancellation of %d order(s)\n", len(ids))
			if s := compactJSON(result); s != "-" && s != "null" {
				a.printer.Printf("%s\n", s)
			}
			return nil
		},
	}

	cmd.Flags().BoolVarP(&assumeYes, "yes", "y", false, "skip the confirmation prompt")
	return cmd
}

func (a *App) newOrderCloseAllCmd() *cobra.Command {
	var (
		sport     string
		eventID   string
		assumeYes bool
	)

	cmd := &cobra.Command{
		Use:   "close-all",
		Short: "Cancel every open order",
		Long: `Cancel every open order, optionally narrowed to a sport or event.

  magicmarkets order close-all
  magicmarkets order close-all --sport fb
  magicmarkets order close-all --sport fb --event 2026-06-15,1001,2002

Narrowing by event requires the sport too.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			scope := "every open order"
			switch {
			case eventID != "":
				scope = fmt.Sprintf("all open orders on %s %s", sport, eventID)
			case sport != "":
				scope = fmt.Sprintf("all open %s orders", sport)
			}

			if !assumeYes {
				ok, err := confirm("Cancel " + scope + "?")
				if err != nil {
					return err
				}
				if !ok {
					a.printer.Warnf("aborted\n")
					return nil
				}
			}

			client, err := a.Client()
			if err != nil {
				return err
			}
			c := ctx(cmd)

			result, err := client.CloseAllOrders(c, sport, eventID)
			if err != nil {
				return err
			}
			if a.printer.JSON {
				return a.printer.Emit(result)
			}
			a.printer.Printf("Requested cancellation of %s\n", scope)
			if s := compactJSON(result); s != "-" && s != "null" {
				a.printer.Printf("%s\n", s)
			}
			return nil
		},
	}

	fl := cmd.Flags()
	fl.StringVar(&sport, "sport", "", "only orders on this sport")
	fl.StringVar(&eventID, "event", "", "only orders on this event (requires --sport)")
	fl.BoolVarP(&assumeYes, "yes", "y", false, "skip the confirmation prompt")
	return cmd
}

// renderOrderTable prints orders as one row each.
func (a *App) renderOrderTable(orders []magicmarkets.Order) error {
	rows := make([][]string, 0, len(orders))
	for _, o := range orders {
		rows = append(rows, []string{
			strconv.FormatInt(o.OrderID, 10),
			dash(o.Status),
			dash(o.Sport),
			eventLabel(o.EventInfo),
			dash(o.BetType),
			price(o.WantPrice),
			money(o.WantStake),
			pprice(o.Price),
			money(o.Stake),
			money(o.ProfitLoss),
			localTime(o.PlacementTime),
		})
	}
	return a.printer.Table([]string{
		"ID", "STATUS", "SPORT", "EVENT", "BET TYPE",
		"WANT PX", "WANT STAKE", "GOT PX", "GOT STAKE", "P&L", "PLACED",
	}, rows)
}

// renderOrder prints a single order plus its constituent bets.
func (a *App) renderOrder(o *magicmarkets.Order) error {
	if o == nil {
		return nil
	}

	kv := [][2]string{
		{"order id", strconv.FormatInt(o.OrderID, 10)},
		{"status", dash(o.Status)},
		{"type", dash(o.OrderType)},
		{"sport", dash(o.Sport)},
		{"bet type", dash(o.BetType)},
	}
	if o.BetTypeDescription != "" {
		kv = append(kv, [2]string{"selection", o.BetTypeDescription})
	}
	if o.EventInfo != nil {
		kv = append(kv, [2]string{"event", eventLabel(o.EventInfo)})
		if o.EventInfo.EventID != nil {
			kv = append(kv, [2]string{"event id", *o.EventInfo.EventID})
		}
		if o.EventInfo.CompetitionName != "" {
			kv = append(kv, [2]string{"competition", o.EventInfo.CompetitionName})
		}
	}
	kv = append(kv,
		[2]string{"wanted", fmt.Sprintf("%s @ %s", magicmarkets.StakeString(o.WantStake), price(o.WantPrice))},
		[2]string{"matched", fmt.Sprintf("%s @ %s", magicmarkets.StakeString(o.Stake), pprice(o.Price))},
		[2]string{"profit/loss", magicmarkets.StakeString(o.ProfitLoss)},
		[2]string{"placed", localTime(o.PlacementTime)},
		[2]string{"expires", localTime(o.ExpiryTime)},
		[2]string{"closed", boolYesNo(o.Closed)},
	)
	if o.CloseReason != nil && *o.CloseReason != "" {
		kv = append(kv, [2]string{"close reason", *o.CloseReason})
	}
	if o.ExchangeMode != nil && *o.ExchangeMode != "" {
		kv = append(kv, [2]string{"exchange mode", *o.ExchangeMode})
	}
	if o.UserData != nil && *o.UserData != "" {
		kv = append(kv, [2]string{"user data", *o.UserData})
	}
	if err := a.printer.KV(kv); err != nil {
		return err
	}

	if len(o.Legs) > 0 {
		a.printer.Printf("\nLegs:\n")
		rows := make([][]string, 0, len(o.Legs))
		for _, l := range o.Legs {
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

	if len(o.Bets) == 0 {
		return nil
	}
	a.printer.Printf("\nBets:\n")
	rows := make([][]string, 0, len(o.Bets))
	for _, b := range o.Bets {
		rows = append(rows, []string{
			strconv.FormatInt(b.BetID, 10),
			dash(b.Status.Code),
			price(b.WantPrice),
			money(b.WantStake),
			pprice(b.GotPrice),
			money(b.GotStake),
			money(b.ProfitLoss),
			pstr(b.ExchangeRole),
		})
	}
	if err := a.printer.Table([]string{
		"BET ID", "STATUS", "WANT PX", "WANT STAKE", "GOT PX", "GOT STAKE", "P&L", "ROLE",
	}, rows); err != nil {
		return err
	}
	for _, b := range o.Bets {
		if b.Status.Reason != "" {
			a.printer.Printf("  bet %d failed: %s\n", b.BetID, b.Status.Reason)
		}
	}
	return nil
}

// parseCurrentScore parses a "HOME-AWAY" flag value into the [home, away]
// tuple the API expects, e.g. "1-2" -> [1, 2]. An empty string means the
// caller does not want the guard and yields a nil result.
func parseCurrentScore(s string) (*[2]int, error) {
	if s == "" {
		return nil, nil
	}
	home, away, ok := strings.Cut(s, "-")
	if !ok {
		return nil, fmt.Errorf("invalid current-score %q: want HOME-AWAY, e.g. 1-2", s)
	}
	h, err := strconv.Atoi(strings.TrimSpace(home))
	if err != nil {
		return nil, fmt.Errorf("invalid current-score %q: home score must be an integer", s)
	}
	a, err := strconv.Atoi(strings.TrimSpace(away))
	if err != nil {
		return nil, fmt.Errorf("invalid current-score %q: away score must be an integer", s)
	}
	return &[2]int{h, a}, nil
}

// parseOrderID parses an order ID argument.
func parseOrderID(s string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid order ID %q: must be an integer", s)
	}
	return id, nil
}
