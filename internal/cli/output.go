package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"magicmarkets-cli/internal/magicmarkets"
)

// Printer renders command output either as an aligned table or as JSON.
//
// Every command supports --json so output can be piped into jq.
type Printer struct {
	JSON bool
	Out  io.Writer
	Err  io.Writer
}

// Emit writes v as indented JSON. Commands call this on the --json path.
func (p *Printer) Emit(v any) error {
	enc := json.NewEncoder(p.Out)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return fmt.Errorf("encode JSON output: %w", err)
	}
	return nil
}

// Table writes rows under headers, aligned in columns.
func (p *Printer) Table(headers []string, rows [][]string) error {
	if len(rows) == 0 {
		fmt.Fprintln(p.Out, "(none)")
		return nil
	}

	tw := tabwriter.NewWriter(p.Out, 0, 4, 2, ' ', 0)
	if len(headers) > 0 {
		fmt.Fprintln(tw, strings.Join(headers, "\t"))
		rule := make([]string, len(headers))
		for i, h := range headers {
			rule[i] = strings.Repeat("-", len(h))
		}
		fmt.Fprintln(tw, strings.Join(rule, "\t"))
	}
	for _, row := range rows {
		fmt.Fprintln(tw, strings.Join(row, "\t"))
	}
	return tw.Flush()
}

// KV writes aligned key/value pairs, for single-record output.
func (p *Printer) KV(pairs [][2]string) error {
	tw := tabwriter.NewWriter(p.Out, 0, 4, 2, ' ', 0)
	for _, kv := range pairs {
		fmt.Fprintf(tw, "%s\t%s\n", kv[0], kv[1])
	}
	return tw.Flush()
}

// Printf writes a formatted line to stdout.
func (p *Printer) Printf(format string, args ...any) {
	fmt.Fprintf(p.Out, format, args...)
}

// Warnf writes a formatted line to stderr, keeping stdout clean for data.
func (p *Printer) Warnf(format string, args ...any) {
	fmt.Fprintf(p.Err, format, args...)
}

// ---------- formatting helpers ----------

// dash returns "-" for the empty string, so tables never show blank cells.
func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// price formats a decimal price, trimming pointless trailing zeros.
func price(v float64) string {
	if v == 0 {
		return "-"
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// pprice formats a possibly-absent price.
func pprice(v *float64) string {
	if v == nil {
		return "-"
	}
	return price(*v)
}

// pstr dereferences a string pointer, using "-" for nil.
func pstr(s *string) string {
	if s == nil || *s == "" {
		return "-"
	}
	return *s
}

// money formats a stake amount to 2 decimals without its currency, for columns
// that carry the currency in the header.
func money(s *magicmarkets.Stake) string {
	if s == nil {
		return "-"
	}
	return strconv.FormatFloat(s.Amount, 'f', 2, 64)
}

// localTime formats a timestamp for display, using "-" for the zero value.
func localTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Local().Format("2006-01-02 15:04:05")
}

// shortTime formats a timestamp without the date, for same-day streams.
func shortTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Local().Format("15:04:05")
}

// until renders how long remains before t, e.g. "12s" or "expired".
func until(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Until(t)
	if d <= 0 {
		return "expired"
	}
	return d.Round(time.Second).String()
}

// eventLabel renders an event for a table cell, preferring the team names.
func eventLabel(info *magicmarkets.EventInfo) string {
	if info == nil {
		return "-"
	}
	if info.HomeTeam != nil && info.AwayTeam != nil && *info.HomeTeam != "" {
		return *info.HomeTeam + " v " + *info.AwayTeam
	}
	return dash(info.EventName)
}
