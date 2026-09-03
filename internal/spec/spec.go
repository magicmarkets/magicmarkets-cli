// Package spec embeds the Magic Markets OpenAPI 3.1 specification so the
// reference commands work offline.
//
// The canonical spec is served at https://magicmarkets.com/v2/openapi.json.
// Refresh the embedded copy with `make update-spec`.
package spec

import (
	"embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

//go:embed openapi.json
var files embed.FS

// httpMethods are the path-item keys that describe operations. Anything else in
// a path item (such as a shared "parameters" list) is not an operation.
var httpMethods = []string{"get", "put", "post", "delete", "patch", "options", "head", "trace"}

// Raw returns the embedded specification bytes.
func Raw() ([]byte, error) {
	return files.ReadFile("openapi.json")
}

// Parameter is one query, path or header parameter.
type Parameter struct {
	Name        string         `json:"name"`
	In          string         `json:"in"`
	Description string         `json:"description"`
	Required    bool           `json:"required"`
	Schema      map[string]any `json:"schema"`
}

// TypeName renders a parameter's schema type for display, including any enum.
func (p Parameter) TypeName() string {
	if p.Schema == nil {
		return "-"
	}
	typ, _ := p.Schema["type"].(string)
	if typ == "" {
		typ = "-"
	}
	if items, ok := p.Schema["items"].(map[string]any); ok && typ == "array" {
		if it, ok := items["type"].(string); ok {
			typ = it + "[]"
		}
	}
	if enum, ok := p.Schema["enum"].([]any); ok && len(enum) > 0 {
		opts := make([]string, 0, len(enum))
		for _, e := range enum {
			opts = append(opts, fmt.Sprint(e))
		}
		typ += " (" + strings.Join(opts, "|") + ")"
	}
	return typ
}

// Default renders a parameter's default value, or the empty string.
func (p Parameter) Default() string {
	if p.Schema == nil {
		return ""
	}
	if d, ok := p.Schema["default"]; ok {
		return fmt.Sprint(d)
	}
	return ""
}

// Example is a named request or response example.
type Example struct {
	Summary string          `json:"summary"`
	Value   json.RawMessage `json:"value"`
}

// MediaType describes one content type of a body.
type MediaType struct {
	Schema   json.RawMessage    `json:"schema"`
	Example  json.RawMessage    `json:"example"`
	Examples map[string]Example `json:"examples"`
}

// Body is a request or response body.
type Body struct {
	Description string               `json:"description"`
	Required    bool                 `json:"required"`
	Content     map[string]MediaType `json:"content"`
}

// JSON returns the application/json media type, when present.
func (b *Body) JSON() (MediaType, bool) {
	if b == nil {
		return MediaType{}, false
	}
	mt, ok := b.Content["application/json"]
	return mt, ok
}

// Operation is one method on one path.
type Operation struct {
	// Method is upper-case, e.g. "POST".
	Method string `json:"-"`
	Path   string `json:"-"`

	OperationID string          `json:"operationId"`
	Summary     string          `json:"summary"`
	Description string          `json:"description"`
	Tags        []string        `json:"tags"`
	Parameters  []Parameter     `json:"parameters"`
	RequestBody *Body           `json:"requestBody"`
	Responses   map[string]Body `json:"responses"`
}

// Document is the parsed specification.
type Document struct {
	OpenAPI string `json:"openapi"`
	Info    struct {
		Title       string `json:"title"`
		Version     string `json:"version"`
		Description string `json:"description"`
	} `json:"info"`

	Paths      map[string]map[string]json.RawMessage `json:"paths"`
	Components struct {
		Schemas map[string]json.RawMessage `json:"schemas"`
	} `json:"components"`

	// operations is the flattened operation list, built once on load.
	operations []Operation
}

var (
	loadOnce sync.Once
	loaded   *Document
	loadErr  error
)

// Load parses the embedded specification. The result is cached.
func Load() (*Document, error) {
	loadOnce.Do(func() {
		raw, err := Raw()
		if err != nil {
			loadErr = fmt.Errorf("read embedded spec: %w", err)
			return
		}
		var doc Document
		if err := json.Unmarshal(raw, &doc); err != nil {
			loadErr = fmt.Errorf("parse embedded spec: %w", err)
			return
		}
		doc.buildOperations()
		loaded = &doc
	})
	return loaded, loadErr
}

// buildOperations flattens paths into a sorted operation list.
func (d *Document) buildOperations() {
	for path, item := range d.Paths {
		for _, method := range httpMethods {
			raw, ok := item[method]
			if !ok {
				continue
			}
			var op Operation
			if err := json.Unmarshal(raw, &op); err != nil {
				continue
			}
			op.Method = strings.ToUpper(method)
			op.Path = path
			d.operations = append(d.operations, op)
		}
	}
	sort.Slice(d.operations, func(i, j int) bool {
		if d.operations[i].Path != d.operations[j].Path {
			return d.operations[i].Path < d.operations[j].Path
		}
		return d.operations[i].Method < d.operations[j].Method
	})
}

// Operations returns every operation, sorted by path then method.
func (d *Document) Operations() []Operation {
	return d.operations
}

// Find looks up operations by path, optionally narrowed to one method.
//
// The path may be given with or without the /v2 prefix and with or without the
// trailing slash, so `magicmarkets api show orders` finds POST and GET /v2/orders/.
func (d *Document) Find(path, method string) []Operation {
	want := normalizePath(path)
	method = strings.ToUpper(strings.TrimSpace(method))

	var exact, partial []Operation
	for _, op := range d.operations {
		if method != "" && op.Method != method {
			continue
		}
		got := normalizePath(op.Path)
		switch {
		case got == want:
			exact = append(exact, op)
		case strings.Contains(got, want):
			partial = append(partial, op)
		}
	}
	if len(exact) > 0 {
		return exact
	}
	return partial
}

// normalizePath reduces a path to a comparable form: no /v2 prefix, no leading
// or trailing slash, lower case.
func normalizePath(p string) string {
	p = strings.ToLower(strings.TrimSpace(p))
	p = strings.Trim(p, "/")
	p = strings.TrimPrefix(p, "v2/")
	return strings.Trim(p, "/")
}

// Resolve follows a local $ref to the schema it points at.
//
// Request and response bodies in this spec are almost always a bare
// {"$ref": "#/components/schemas/X"}, which is useless to print as-is. Only
// local component refs are resolved; anything else is returned unchanged.
func (d *Document) Resolve(raw json.RawMessage) json.RawMessage {
	const prefix = "#/components/schemas/"

	// Follow a short chain in case a schema aliases another, with a low bound
	// so a self-referential spec cannot spin here.
	for range 8 {
		var ref struct {
			Ref string `json:"$ref"`
		}
		if err := json.Unmarshal(raw, &ref); err != nil || ref.Ref == "" {
			return raw
		}
		name, ok := strings.CutPrefix(ref.Ref, prefix)
		if !ok {
			return raw
		}
		next, _, found := d.Schema(name)
		if !found {
			return raw
		}
		raw = next
	}
	return raw
}

// RefName returns the component name a schema points at, when it is a bare
// local $ref. It lets callers label a resolved schema.
func (d *Document) RefName(raw json.RawMessage) string {
	var ref struct {
		Ref string `json:"$ref"`
	}
	if err := json.Unmarshal(raw, &ref); err != nil || ref.Ref == "" {
		return ""
	}
	name, ok := strings.CutPrefix(ref.Ref, "#/components/schemas/")
	if !ok {
		return ""
	}
	return name
}

// SchemaNames returns the component schema names, sorted.
func (d *Document) SchemaNames() []string {
	names := make([]string, 0, len(d.Components.Schemas))
	for n := range d.Components.Schemas {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Schema returns one component schema by name, matched case-insensitively.
func (d *Document) Schema(name string) (json.RawMessage, string, bool) {
	if raw, ok := d.Components.Schemas[name]; ok {
		return raw, name, true
	}
	for n, raw := range d.Components.Schemas {
		if strings.EqualFold(n, name) {
			return raw, n, true
		}
	}
	return nil, "", false
}

// SearchResult is one hit from Search.
type SearchResult struct {
	// Kind is "operation" or "schema".
	Kind string
	Name string
	// Detail is the matching text, e.g. a summary line.
	Detail string
}

// Search finds operations and schemas matching a case-insensitive term.
func (d *Document) Search(term string) []SearchResult {
	needle := strings.ToLower(strings.TrimSpace(term))
	if needle == "" {
		return nil
	}

	var out []SearchResult
	for _, op := range d.operations {
		haystack := strings.ToLower(strings.Join([]string{
			op.Path, op.Method, op.OperationID, op.Summary, op.Description,
			strings.Join(op.Tags, " "),
		}, " "))
		if strings.Contains(haystack, needle) {
			out = append(out, SearchResult{
				Kind:   "operation",
				Name:   op.Method + " " + op.Path,
				Detail: op.Summary,
			})
		}
	}
	for _, name := range d.SchemaNames() {
		raw := d.Components.Schemas[name]
		if strings.Contains(strings.ToLower(name), needle) ||
			strings.Contains(strings.ToLower(string(raw)), needle) {
			out = append(out, SearchResult{
				Kind:   "schema",
				Name:   name,
				Detail: schemaDescription(raw),
			})
		}
	}
	return out
}

// schemaDescription pulls the description out of a schema, for search output.
func schemaDescription(raw json.RawMessage) string {
	var s struct {
		Description string `json:"description"`
		Type        string `json:"type"`
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		return ""
	}
	if s.Description != "" {
		return firstLine(s.Description)
	}
	return s.Type
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i])
	}
	return strings.TrimSpace(s)
}
