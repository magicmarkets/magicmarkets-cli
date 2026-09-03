package cli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"magicmarkets-cli/internal/magicmarkets"
)

func (a *App) newBetTypeCmd() *cobra.Command {
	var (
		homeTeam string
		awayTeam string
		showGrid bool
	)

	cmd := &cobra.Command{
		Use:     "bet-type <sport> <bet-type>",
		Short:   "Validate a bet type and show its payoff grid",
		Aliases: []string{"bt"},
		Long: `Parse and validate a bet_type string against a sport.

A successful response carries a human-readable description and the win/loss
grid. A validation error means the string did not parse.

  magicmarkets bet-type fb for,h
  magicmarkets bet-type fb for,ah,h,-4 --grid
  magicmarkets bet-type fb for,over,2.5 --home Arsenal --away Chelsea

Bet type grammar (see docs/api-reference.md for the full reference):
  Direction     for (back) or against (lay), always the first token
  Match result  for,h  for,d  for,a  for,dnb,h  for,dc,h,d
  Totals        for,over,2.5  for,under,2.5  for,overeq,3
  Asian hcap    for,ah,h,-4   — lines are integers equal to 4x the real line,
                so -4 means home -1.0 and 2 means +0.5
  Correct score for,cs,2,1
  Handicaps always refer to the home team.

In practice you rarely build these by hand: take the bet_type verbatim from
` + "`magicmarkets offers`" + ` or the stream.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			sport, betType := args[0], args[1]

			client, err := a.Client()
			if err != nil {
				return err
			}
			c, cancel := withTimeout(cmd, a.cfg.Timeout)
			defer cancel()

			info, err := client.GetBetTypeInfo(c, sport, betType, homeTeam, awayTeam)
			if err != nil {
				if magicmarkets.HasCode(err, magicmarkets.CodeValidationError) {
					return fmt.Errorf("%q is not a valid bet type for sport %q:\n%w", betType, sport, err)
				}
				return err
			}

			if a.printer.JSON {
				return a.printer.Emit(info)
			}

			dir := magicmarkets.DirectionOf(betType)
			side := "back (wins if it happens)"
			if dir == magicmarkets.Lay {
				side = "lay (wins if it does not happen)"
			}
			if err := a.printer.KV([][2]string{
				{"sport", dash(info.Sport)},
				{"bet type", betType},
				{"description", dash(info.BetTypeDescription)},
				{"direction", side},
				{"valid", "yes"},
			}); err != nil {
				return err
			}

			if showGrid {
				a.printer.Printf("\nOutcome grid (rows = home score, columns = away score):\n")
				a.printer.Printf("w = win, l = loss, p = push, v = void\n\n")
				if err := a.renderWinLossGrid(info.WinLossGrid); err != nil {
					return err
				}
			} else if len(info.WinLossGrid) > 0 {
				a.printer.Printf("\n(pass --grid to print the %dx%d outcome grid)\n",
					len(info.WinLossGrid), len(info.WinLossGrid[0]))
			}
			return nil
		},
	}

	fl := cmd.Flags()
	fl.StringVar(&homeTeam, "home", "", "home team name, for display labels")
	fl.StringVar(&awayTeam, "away", "", "away team name, for display labels")
	fl.BoolVar(&showGrid, "grid", false, "print the win/loss grid")
	return cmd
}

// renderWinLossGrid prints the outcome grid indexed by scoreline.
func (a *App) renderWinLossGrid(grid [][]string) error {
	if len(grid) == 0 {
		a.printer.Printf("(empty)\n")
		return nil
	}

	width := 0
	for _, row := range grid {
		if len(row) > width {
			width = len(row)
		}
	}

	headers := make([]string, 0, width+1)
	headers = append(headers, "H\\A")
	for i := 0; i < width; i++ {
		headers = append(headers, strconv.Itoa(i))
	}

	rows := make([][]string, 0, len(grid))
	for home, row := range grid {
		cells := make([]string, 0, width+1)
		cells = append(cells, strconv.Itoa(home))
		for away := 0; away < width; away++ {
			if away >= len(row) {
				cells = append(cells, "")
				continue
			}
			cells = append(cells, strings.TrimSpace(row[away]))
		}
		rows = append(rows, cells)
	}
	return a.printer.Table(headers, rows)
}
