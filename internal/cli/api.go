package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"magicmarkets-cli/internal/spec"
)

func (a *App) newAPICmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "api",
		Short: "Explore the API specification offline",
		Long: `Browse the embedded OpenAPI 3.1 specification.

These commands need no API key and make no network requests — the spec is
compiled into the binary.

  magicmarkets api endpoints                 list every endpoint
  magicmarkets api show orders               detail the /v2/orders/ operations
  magicmarkets api schema OrderResponse      print one schema
  magicmarkets api curl POST /v2/orders/     generate a runnable curl command
  magicmarkets api search heartbeat          search endpoints and schemas`,
	}
	cmd.AddCommand(
		a.newAPIEndpointsCmd(),
		a.newAPIShowCmd(),
		a.newAPISchemaCmd(),
		a.newAPICurlCmd(),
		a.newAPISearchCmd(),
		a.newAPISpecCmd(),
	)
	return cmd
}

func (a *App) newAPIEndpointsCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "endpoints",
		Short:   "List every API endpoint",
		Aliases: []string{"ls", "list"},
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			doc, err := spec.Load()
			if err != nil {
				return err
			}
			ops := doc.Operations()

			if a.printer.JSON {
				out := make([]map[string]string, 0, len(ops))
				for _, op := range ops {
					out = append(out, map[string]string{
						"method":  op.Method,
						"path":    op.Path,
						"summary": op.Summary,
					})
				}
				return a.printer.Emit(out)
			}

			rows := make([][]string, 0, len(ops))
			for _, op := range ops {
				rows = append(rows, []string{op.Method, op.Path, dash(op.Summary)})
			}
			return a.printer.Table([]string{"METHOD", "PATH", "SUMMARY"}, rows)
		},
	}
}

func (a *App) newAPIShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show <path> [method]",
		Short: "Show one endpoint in detail",
		Long: `Show an endpoint's parameters, request body and responses.

The path may be given with or without the /v2 prefix and trailing slash, and is
matched as a substring when there is no exact hit:

  magicmarkets api show /v2/orders/
  magicmarkets api show orders POST
  magicmarkets api show betslips`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			doc, err := spec.Load()
			if err != nil {
				return err
			}
			method := ""
			if len(args) == 2 {
				method = args[1]
			}

			ops := doc.Find(args[0], method)
			if len(ops) == 0 {
				return fmt.Errorf("no endpoint matches %q — try `magicmarkets api endpoints`", args[0])
			}
			if a.printer.JSON {
				return a.printer.Emit(ops)
			}

			for i, op := range ops {
				if i > 0 {
					a.printer.Printf("\n%s\n\n", strings.Repeat("─", 60))
				}
				if err := a.renderOperation(doc, op); err != nil {
					return err
				}
			}
			return nil
		},
	}
}

// renderOperation prints one operation's full detail. doc is used to resolve
// $ref pointers so bodies print as real schemas.
func (a *App) renderOperation(doc *spec.Document, op spec.Operation) error {
	a.printer.Printf("%s %s\n", op.Method, op.Path)
	if op.Summary != "" {
		a.printer.Printf("%s\n", op.Summary)
	}
	if op.Description != "" {
		a.printer.Printf("\n%s\n", strings.TrimSpace(op.Description))
	}

	if len(op.Parameters) > 0 {
		a.printer.Printf("\nParameters:\n")
		rows := make([][]string, 0, len(op.Parameters))
		for _, p := range op.Parameters {
			required := "-"
			if p.Required {
				required = "yes"
			}
			rows = append(rows, []string{
				p.Name, p.In, p.TypeName(), required,
				dash(p.Default()), dash(firstLineOf(p.Description)),
			})
		}
		if err := a.printer.Table(
			[]string{"NAME", "IN", "TYPE", "REQUIRED", "DEFAULT", "DESCRIPTION"}, rows); err != nil {
			return err
		}
	}

	if mt, ok := op.RequestBody.JSON(); ok {
		required := ""
		if op.RequestBody.Required {
			required = " (required)"
		}
		label := ""
		if name := doc.RefName(mt.Schema); name != "" {
			label = " (" + name + ")"
		}
		a.printer.Printf("\nRequest body%s%s:\n", required, label)
		if len(mt.Schema) > 0 {
			a.printer.Printf("%s\n", indentJSON(doc.Resolve(mt.Schema), "  "))
		}
		if ex := pickExample(mt); len(ex) > 0 {
			a.printer.Printf("\nExample:\n%s\n", indentJSON(ex, "  "))
		}
	}

	if len(op.Responses) > 0 {
		a.printer.Printf("\nResponses:\n")
		codes := make([]string, 0, len(op.Responses))
		for code := range op.Responses {
			codes = append(codes, code)
		}
		sort.Strings(codes)
		rows := make([][]string, 0, len(codes))
		for _, code := range codes {
			rows = append(rows, []string{code, dash(firstLineOf(op.Responses[code].Description))})
		}
		if err := a.printer.Table([]string{"CODE", "DESCRIPTION"}, rows); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) newAPISchemaCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "schema [name]",
		Short: "Print a component schema, or list them all",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			doc, err := spec.Load()
			if err != nil {
				return err
			}

			if len(args) == 0 {
				names := doc.SchemaNames()
				if a.printer.JSON {
					return a.printer.Emit(names)
				}
				rows := make([][]string, 0, len(names))
				for _, n := range names {
					rows = append(rows, []string{n})
				}
				return a.printer.Table([]string{"SCHEMA"}, rows)
			}

			raw, name, ok := doc.Schema(args[0])
			if !ok {
				return fmt.Errorf("no schema named %q — run `magicmarkets api schema` to list them", args[0])
			}
			if a.printer.JSON {
				a.printer.Printf("%s\n", indentJSON(raw, ""))
				return nil
			}
			a.printer.Printf("%s:\n%s\n", name, indentJSON(raw, "  "))
			return nil
		},
	}
}

func (a *App) newAPICurlCmd() *cobra.Command {
	var includeKey bool

	cmd := &cobra.Command{
		Use:   "curl <method> <path>",
		Short: "Generate a curl command for an endpoint",
		Long: `Print a runnable curl command for an endpoint.

The API key is referenced as $MAGICMARKETS_API_KEY rather than inlined, so the output
is safe to paste and share. Pass --include-key to inline the real key.

  magicmarkets api curl GET /v2/balance/
  magicmarkets api curl POST /v2/orders/`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			doc, err := spec.Load()
			if err != nil {
				return err
			}
			method := strings.ToUpper(args[0])

			ops := doc.Find(args[1], method)
			if len(ops) == 0 {
				return fmt.Errorf("no %s endpoint matches %q", method, args[1])
			}
			op := ops[0]

			key := "$MAGICMARKETS_API_KEY"
			if includeKey {
				if err := a.cfg.RequireKey(); err != nil {
					return err
				}
				key = a.cfg.APIKey
			}

			var b strings.Builder
			fmt.Fprintf(&b, "curl -sS -X %s '%s%s'", op.Method, a.cfg.APIURL, strings.TrimPrefix(op.Path, "/v2"))
			fmt.Fprintf(&b, " \\\n  -H 'X-Api-Key: %s'", key)

			if mt, ok := op.RequestBody.JSON(); ok {
				fmt.Fprintf(&b, " \\\n  -H 'Content-Type: application/json'")
				body := pickExample(mt)
				if len(body) == 0 {
					body = json.RawMessage("{}")
				}
				fmt.Fprintf(&b, " \\\n  -d '%s'", compactOneLine(body))
			}

			// Required query and path parameters are worth calling out: the
			// generated command will not work until they are filled in.
			var required []string
			for _, p := range op.Parameters {
				if p.Required {
					required = append(required, p.In+" "+p.Name)
				}
			}

			if a.printer.JSON {
				return a.printer.Emit(map[string]any{
					"method":              op.Method,
					"path":                op.Path,
					"curl":                b.String(),
					"required_parameters": required,
				})
			}

			a.printer.Printf("%s\n", b.String())
			if len(required) > 0 {
				a.printer.Warnf("\nfill in the required parameters: %s\n", strings.Join(required, ", "))
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&includeKey, "include-key", false, "inline the real API key instead of $MAGICMARKETS_API_KEY")
	return cmd
}

func (a *App) newAPISearchCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "search <term>",
		Short: "Search endpoints and schemas",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			doc, err := spec.Load()
			if err != nil {
				return err
			}
			results := doc.Search(args[0])
			if a.printer.JSON {
				return a.printer.Emit(results)
			}
			if len(results) == 0 {
				a.printer.Printf("No match for %q\n", args[0])
				return nil
			}
			rows := make([][]string, 0, len(results))
			for _, r := range results {
				rows = append(rows, []string{r.Kind, r.Name, dash(r.Detail)})
			}
			return a.printer.Table([]string{"KIND", "NAME", "DETAIL"}, rows)
		},
	}
}

func (a *App) newAPISpecCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "spec",
		Short: "Print the embedded OpenAPI specification",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			raw, err := spec.Raw()
			if err != nil {
				return err
			}
			doc, err := spec.Load()
			if err == nil && !a.printer.JSON {
				a.printer.Warnf("# %s %s (OpenAPI %s), %d endpoints\n",
					doc.Info.Title, doc.Info.Version, doc.OpenAPI, len(doc.Operations()))
			}
			a.printer.Printf("%s\n", strings.TrimRight(string(raw), "\n"))
			return nil
		},
	}
}

// ---------- helpers ----------

// pickExample returns a media type's example, preferring the single `example`
// then the first named entry of `examples`.
func pickExample(mt spec.MediaType) json.RawMessage {
	if len(mt.Example) > 0 {
		return mt.Example
	}
	if len(mt.Examples) == 0 {
		return nil
	}
	names := make([]string, 0, len(mt.Examples))
	for n := range mt.Examples {
		names = append(names, n)
	}
	sort.Strings(names)
	return mt.Examples[names[0]].Value
}

// indentJSON re-indents raw JSON, prefixing every line with prefix.
func indentJSON(raw json.RawMessage, prefix string) string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, prefix, "  "); err != nil {
		return prefix + string(raw)
	}
	return prefix + buf.String()
}

// compactOneLine renders JSON on a single line for embedding in a shell command.
func compactOneLine(raw json.RawMessage) string {
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return string(raw)
	}
	return buf.String()
}

// firstLineOf trims a description to its first line, for table cells.
func firstLineOf(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	const max = 80
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
}
