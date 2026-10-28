package mcpserver

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"strings"
	"text/template"
)

// serverCardSource is servercard.json, served at GET /mcp/server-card — a
// fixed, build-time artifact, not something derived from a running server.
// Edit this file and rebuild to change it for a specific case. Its dynamic
// fields (.PublicURL) are filled in per request from the same MCPPublicURL
// that OAuth metadata already uses (MAGICMARKETS_MCP_PUBLIC_URL, or the
// incoming request's host when unset) — see publicBase.
//
//go:embed servercard.json
var serverCardSource string

var serverCardTemplate = template.Must(template.New("servercard.json").Parse(serverCardSource))

// serverCardData is the template data for serverCardTemplate.
type serverCardData struct {
	// PublicURL is this deployment's externally-reachable base, no trailing
	// slash (e.g. "https://mcp.example.test").
	PublicURL string
}

func init() {
	if _, err := renderServerCard(serverCardData{PublicURL: "https://example.invalid"}); err != nil {
		panic("mcpserver: servercard.json: " + err.Error())
	}
}

// renderServerCard executes serverCardTemplate and validates the result is
// well-formed JSON before it ever reaches a client.
func renderServerCard(data serverCardData) ([]byte, error) {
	var buf strings.Builder
	if err := serverCardTemplate.Execute(&buf, data); err != nil {
		return nil, err
	}
	body := buf.String()
	var v any
	if err := json.Unmarshal([]byte(body), &v); err != nil {
		return nil, err
	}
	return []byte(body), nil
}

func (s *Server) serveServerCard(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		writeCORS(w)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodGet {
		writeCORS(w)
		w.Header().Set("Allow", "GET, OPTIONS")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := renderServerCard(serverCardData{PublicURL: strings.TrimRight(s.publicBase(r), "/")})
	if err != nil {
		// Unreachable in practice: init already validated this template
		// against the same code path. Fail loudly rather than serve garbage
		// if that invariant ever breaks.
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":             "server_error",
			"error_description": "server card is misconfigured",
		})
		return
	}

	writeCORS(w)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
