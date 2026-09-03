package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"
	"magicmarkets-cli/internal/magicmarkets"
)

func (a *App) newHeartbeatCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "heartbeat",
		Short:   "Manage dead-man's-switch heartbeats",
		Aliases: []string{"hb"},
		Long: `A heartbeat is a dead-man's switch: if it is not refreshed before it expires,
every open order is closed automatically.

Use it to protect an automated strategy against its own crash. The timeout is
10–300 seconds.

  magicmarkets heartbeat run --timeout 60     keep one alive in the foreground`,
	}
	cmd.AddCommand(
		a.newHeartbeatCreateCmd(),
		a.newHeartbeatListCmd(),
		a.newHeartbeatGetCmd(),
		a.newHeartbeatRefreshCmd(),
		a.newHeartbeatCancelCmd(),
		a.newHeartbeatRunCmd(),
	)
	return cmd
}

func (a *App) newHeartbeatCreateCmd() *cobra.Command {
	var timeout int

	cmd := &cobra.Command{
		Use:   "create",
		Short: "Start a heartbeat",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := a.Client()
			if err != nil {
				return err
			}
			c := ctx(cmd)

			hb, err := client.CreateHeartbeat(c, timeout)
			if err != nil {
				return err
			}
			if a.printer.JSON {
				return a.printer.Emit(hb)
			}
			return a.renderHeartbeat(hb)
		},
	}

	cmd.Flags().IntVar(&timeout, "timeout", 60, "seconds before expiry (10–300)")
	return cmd
}

func (a *App) newHeartbeatListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Short:   "List active heartbeats",
		Aliases: []string{"ls"},
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := a.Client()
			if err != nil {
				return err
			}
			c := ctx(cmd)

			hbs, err := client.ListHeartbeats(c)
			if err != nil {
				return err
			}
			if a.printer.JSON {
				return a.printer.Emit(hbs)
			}

			rows := make([][]string, 0, len(hbs))
			for _, hb := range hbs {
				rows = append(rows, []string{
					hb.HeartbeatID,
					localTime(hb.ExpiryTime),
					until(hb.ExpiryTime),
				})
			}
			return a.printer.Table([]string{"HEARTBEAT ID", "EXPIRES", "IN"}, rows)
		},
	}
}

func (a *App) newHeartbeatGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <heartbeat-id>",
		Short: "Show a heartbeat",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.Client()
			if err != nil {
				return err
			}
			c := ctx(cmd)

			hb, err := client.GetHeartbeat(c, args[0])
			if err != nil {
				return err
			}
			if a.printer.JSON {
				return a.printer.Emit(hb)
			}
			return a.renderHeartbeat(hb)
		},
	}
}

func (a *App) newHeartbeatRefreshCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "refresh <heartbeat-id>",
		Short: "Extend a heartbeat's expiry",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.Client()
			if err != nil {
				return err
			}
			c := ctx(cmd)

			hb, err := client.RefreshHeartbeat(c, args[0])
			if err != nil {
				return err
			}
			if a.printer.JSON {
				return a.printer.Emit(hb)
			}
			return a.renderHeartbeat(hb)
		},
	}
}

func (a *App) newHeartbeatCancelCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "cancel <heartbeat-id>",
		Short:   "Stop a heartbeat without closing orders",
		Aliases: []string{"delete", "rm"},
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := a.Client()
			if err != nil {
				return err
			}
			c := ctx(cmd)

			if err := client.CancelHeartbeat(c, args[0]); err != nil {
				return err
			}
			if a.printer.JSON {
				return a.printer.Emit(map[string]any{"heartbeat_id": args[0], "cancelled": true})
			}
			a.printer.Printf("Cancelled heartbeat %s\n", args[0])
			return nil
		},
	}
}

func (a *App) newHeartbeatRunCmd() *cobra.Command {
	var (
		timeout  int
		interval time.Duration
	)

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Create a heartbeat and keep refreshing it until interrupted",
		Long: `Create a heartbeat and refresh it on an interval until interrupted.

While this runs, your open orders are protected: if this process dies, the
heartbeat lapses and the API closes every open order.

On Ctrl-C the heartbeat is cancelled cleanly, so orders are left open.

The refresh interval defaults to a third of the timeout, which leaves room for
two missed refreshes before the switch fires.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			client, err := a.Client()
			if err != nil {
				return err
			}
			c := ctx(cmd)

			if interval <= 0 {
				interval = time.Duration(timeout) * time.Second / 3
			}
			if interval >= time.Duration(timeout)*time.Second {
				return fmt.Errorf("refresh interval %s must be shorter than the %ds timeout", interval, timeout)
			}

			hb, err := client.CreateHeartbeat(c, timeout)
			if err != nil {
				return err
			}
			a.printer.Warnf("heartbeat %s created, expires %s; refreshing every %s\n",
				hb.HeartbeatID, localTime(hb.ExpiryTime), interval)
			a.printer.Warnf("press Ctrl-C to cancel it and leave orders open\n")

			ticker := time.NewTicker(interval)
			defer ticker.Stop()

			for {
				select {
				case <-c.Done():
					// Cancel with a fresh context: the command's context is
					// already cancelled, so it cannot carry the request.
					cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					if err := client.CancelHeartbeat(cleanup, hb.HeartbeatID); err != nil {
						a.printer.Warnf("\nwarning: could not cancel heartbeat %s: %v\n", hb.HeartbeatID, err)
						a.printer.Warnf("it will lapse on its own, closing open orders\n")
						return nil
					}
					a.printer.Warnf("\ncancelled heartbeat %s\n", hb.HeartbeatID)
					return nil

				case <-ticker.C:
					refreshed, err := client.RefreshHeartbeat(c, hb.HeartbeatID)
					if err != nil {
						return fmt.Errorf("refresh heartbeat %s: %w", hb.HeartbeatID, err)
					}
					hb = refreshed
					if a.verbose {
						a.printer.Warnf("refreshed, expires %s\n", localTime(hb.ExpiryTime))
					}
				}
			}
		},
	}

	fl := cmd.Flags()
	fl.IntVar(&timeout, "timeout", 60, "seconds before expiry (10–300)")
	fl.DurationVar(&interval, "interval", 0, "refresh interval (default: a third of the timeout)")
	return cmd
}

func (a *App) renderHeartbeat(hb *magicmarkets.Heartbeat) error {
	return a.printer.KV([][2]string{
		{"heartbeat id", hb.HeartbeatID},
		{"expires", localTime(hb.ExpiryTime)},
		{"in", until(hb.ExpiryTime)},
	})
}
