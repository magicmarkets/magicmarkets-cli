package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"magicmarkets-cli/internal/magicmarkets"
)

func (a *App) newStreamCmd() *cobra.Command {
	var (
		register  []string
		types     []string
		raw       bool
		duration  time.Duration
		keepalive time.Duration
	)

	cmd := &cobra.Command{
		Use:   "stream",
		Short: "Tail the live price and account feed",
		Long: `Connect to the WebSocket and print messages as they arrive.

Account updates — order, bet, balance, pmm, betslip — arrive without any
registration. Market data for a specific event requires registering it:

  magicmarkets stream --register fb:2026-06-15,1001,2002
  magicmarkets stream --type order,bet          # only order activity
  magicmarkets stream --raw --json              # full payloads, one JSON object per line

Registered-event syntax is sport:event_id, repeatable.

The connection drops silently on backpressure, so keep the consumer fast. This
command does not reconnect; wrap it in a loop if you need that.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c := ctx(cmd)

			registrations, err := parseRegistrations(register)
			if err != nil {
				return err
			}

			wanted := make(map[string]bool, len(types))
			for _, t := range types {
				wanted[strings.TrimSpace(t)] = true
			}

			stream, err := a.Stream(c)
			if err != nil {
				return err
			}
			defer stream.Close()

			// Deadline for the whole session, when --for was given.
			var deadline time.Time
			if duration > 0 {
				deadline = time.Now().Add(duration)
			}

			// Registering has to wait for the initial sync, otherwise the
			// register acknowledgement is lost inside the opening dump.
			if len(registrations) > 0 {
				if _, err := stream.Snapshot(c, 30*time.Second); err != nil {
					return fmt.Errorf("waiting for initial sync: %w", err)
				}
				for _, r := range registrations {
					if err := stream.RegisterEvent(r[0], r[1]); err != nil {
						return err
					}
					a.printer.Warnf("registered %s %s\n", r[0], r[1])
				}
			}

			// Keepalive echoes are optional: the server sends info messages
			// every few seconds, so an idle connection is not silent.
			if keepalive > 0 {
				go func() {
					t := time.NewTicker(keepalive)
					defer t.Stop()
					for {
						select {
						case <-c.Done():
							return
						case <-t.C:
							if err := stream.Echo("keepalive"); err != nil {
								return
							}
						}
					}
				}()
			}

			for {
				if err := c.Err(); err != nil {
					return nil // context cancelled by Ctrl-C; a clean exit
				}
				if !deadline.IsZero() {
					if time.Now().After(deadline) {
						return nil
					}
					if err := stream.SetReadDeadline(deadline); err != nil {
						return err
					}
				}

				frame, err := stream.ReadFrame()
				if err != nil {
					if c.Err() != nil || isDeadline(err) {
						return nil
					}
					return err
				}

				ts := frameTime(frame.TS)
				for _, msg := range frame.Messages {
					if len(wanted) > 0 && !wanted[msg.Type] {
						continue
					}
					if err := a.printStreamMessage(ts, msg, raw); err != nil {
						return err
					}
				}
			}
		},
	}

	fl := cmd.Flags()
	fl.StringSliceVar(&register, "register", nil, "events to register, as sport:event_id (repeatable)")
	fl.StringSliceVar(&types, "type", nil, "only these message types, e.g. offer,order,balance")
	fl.BoolVar(&raw, "raw", false, "print full payloads instead of one-line summaries")
	fl.DurationVar(&duration, "for", 0, "exit after this long (0 to run until interrupted)")
	fl.DurationVar(&keepalive, "keepalive", 0, "send an echo command on this interval")
	return cmd
}

// printStreamMessage writes one message, either as a summary line or in full.
func (a *App) printStreamMessage(ts string, msg magicmarkets.Message, raw bool) error {
	if a.printer.JSON {
		return a.printer.Emit(map[string]any{
			"ts":   ts,
			"type": msg.Type,
			"data": json.RawMessage(orNull(msg.Data)),
		})
	}
	if raw {
		a.printer.Printf("%s  %-14s %s\n", ts, msg.Type, compactJSON(msg.Data))
		return nil
	}
	a.printer.Printf("%s  %-14s %s\n", ts, msg.Type, summarize(msg))
	return nil
}

// summarize renders a one-line human summary of a stream message, falling back
// to compact JSON for types without a bespoke rendering.
func summarize(msg magicmarkets.Message) string {
	switch msg.Type {
	case magicmarkets.MsgEvent:
		var e magicmarkets.StreamEvent
		if msg.Decode(&e) != nil {
			break
		}
		return fmt.Sprintf("%s %s  %s  [%s]", e.Sport, e.EventID, eventName(e), dash(e.IRStatus))

	case magicmarkets.MsgRemoveEvent, magicmarkets.MsgRemoveIRInfo:
		var k magicmarkets.OfferKey
		if msg.Decode(&k) != nil {
			break
		}
		return fmt.Sprintf("%s %s", k.Sport, k.EventID)

	case magicmarkets.MsgOffer:
		var o magicmarkets.Offer
		if msg.Decode(&o) != nil {
			break
		}
		return fmt.Sprintf("%s %s  %-28s %s", o.Sport, o.EventID, o.BetType, formatLevels(o.PriceList))

	case magicmarkets.MsgRemoveOffer:
		var k magicmarkets.OfferKey
		if msg.Decode(&k) != nil {
			break
		}
		return fmt.Sprintf("%s %s  %s", k.Sport, k.EventID, k.BetType)

	case magicmarkets.MsgSync:
		var s magicmarkets.SyncPayload
		if msg.Decode(&s) != nil {
			break
		}
		return "session " + s.SessionID

	case magicmarkets.MsgBalance:
		var b magicmarkets.BalanceUpdate
		if msg.Decode(&b) != nil {
			break
		}
		return fmt.Sprintf("balance %s  open %s", b.Balance, b.OpenStake)

	case magicmarkets.MsgXRate:
		var x magicmarkets.XRate
		if msg.Decode(&x) != nil {
			break
		}
		return fmt.Sprintf("%s = %g USDT", x.Ccy, x.Rate)

	case magicmarkets.MsgOrder:
		var o magicmarkets.Order
		if msg.Decode(&o) != nil {
			break
		}
		return fmt.Sprintf("order %d  %-8s %s @ %s  stake %s  %s",
			o.OrderID, o.Status, dash(o.BetType), price(o.WantPrice),
			magicmarkets.StakeString(o.WantStake), pstr(o.CloseReason))

	case magicmarkets.MsgBet:
		var b magicmarkets.Bet
		if msg.Decode(&b) != nil {
			break
		}
		return fmt.Sprintf("bet %d (order %d)  %s  got %s @ %s",
			b.BetID, b.OrderID, b.Status.Code, magicmarkets.StakeString(b.GotStake), pprice(b.GotPrice))

	case magicmarkets.MsgPMM, magicmarkets.MsgBetslip:
		var bs magicmarkets.Betslip
		if msg.Decode(&bs) != nil {
			break
		}
		return fmt.Sprintf("betslip %s  %-28s %s", bs.BetslipID, bs.BetType, formatLevels(bs.PriceList))

	case magicmarkets.MsgBetslipClosed:
		var bc magicmarkets.BetslipClosed
		if msg.Decode(&bc) != nil {
			break
		}
		return fmt.Sprintf("betslip %s closed (%s)", bc.BetslipID, dash(bc.CloseReason))

	case magicmarkets.MsgEventTime:
		var et magicmarkets.EventTime
		if msg.Decode(&et) != nil {
			break
		}
		if !et.Time.Present {
			return fmt.Sprintf("%s %s  no clock", et.Sport, et.EventID)
		}
		return fmt.Sprintf("%s %s  %s %d'", et.Sport, et.EventID, et.Time.Period, et.Time.Minutes)

	case magicmarkets.MsgEventScore, magicmarkets.MsgEventRedCards:
		var es magicmarkets.EventScore
		if msg.Decode(&es) != nil {
			break
		}
		if len(es.Score) >= 2 {
			return fmt.Sprintf("%s %s  %d-%d", es.Sport, es.EventID, es.Score[0], es.Score[1])
		}

	case magicmarkets.MsgInfo:
		var i magicmarkets.InfoPayload
		if msg.Decode(&i) != nil {
			break
		}
		return fmt.Sprintf("%d registered events", i.RegisteredEvents)

	case magicmarkets.MsgResponse:
		var r magicmarkets.StreamResponse
		if msg.Decode(&r) != nil {
			break
		}
		if r.IsError() {
			return "error: " + r.Code
		}
		if pairs, err := r.RegisteredEvents(); err == nil && len(pairs) > 0 {
			labels := make([]string, 0, len(pairs))
			for _, p := range pairs {
				labels = append(labels, p[0]+":"+p[1])
			}
			return "ok  registered: " + strings.Join(labels, ", ")
		}
		return "ok  " + compactJSON(r.Data)

	case magicmarkets.MsgClearEvents:
		return "feed reset — discard cached events and await a fresh snapshot"
	}

	return compactJSON(msg.Data)
}

// parseRegistrations parses sport:event_id pairs.
//
// Event IDs contain commas, so the flag is parsed on the first colon only.
func parseRegistrations(specs []string) ([][2]string, error) {
	out := make([][2]string, 0, len(specs))
	for _, s := range specs {
		sport, eventID, ok := strings.Cut(strings.TrimSpace(s), ":")
		if !ok || sport == "" || eventID == "" {
			return nil, fmt.Errorf("invalid --register %q: want sport:event_id, e.g. fb:2026-06-15,1001,2002", s)
		}
		out = append(out, [2]string{sport, eventID})
	}
	return out, nil
}

// frameTime formats a frame's Unix timestamp for display.
func frameTime(ts float64) string {
	if ts == 0 {
		return time.Now().Local().Format("15:04:05.000")
	}
	sec := int64(ts)
	nsec := int64((ts - float64(sec)) * 1e9)
	return time.Unix(sec, nsec).Local().Format("15:04:05.000")
}

// compactJSON renders a payload on one line, using "-" when absent.
func compactJSON(data json.RawMessage) string {
	if len(data) == 0 {
		return "-"
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, data); err != nil {
		return string(data)
	}
	return buf.String()
}

func orNull(data json.RawMessage) json.RawMessage {
	if len(data) == 0 {
		return json.RawMessage("null")
	}
	return data
}

// isDeadline reports whether err is a read timeout rather than a real failure.
func isDeadline(err error) bool {
	var ne net.Error
	if errors.As(err, &ne) {
		return ne.Timeout()
	}
	return false
}
