package magicmarkets

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// Message type tags that can appear as entries in a frame's data array.
const (
	// Market data.
	MsgEvent       = "event"
	MsgRemoveEvent = "remove_event"
	MsgSync        = "sync"
	MsgOffer       = "offer"
	MsgRemoveOffer = "remove_offer"
	MsgClearEvents = "clear_events"

	// Live event state.
	MsgEventTime     = "event_time"
	MsgEventScore    = "event_score"
	MsgEventRedCards = "event_red_cards"
	MsgIRInfo        = "ir_info"
	MsgRemoveIRInfo  = "remove_ir_info"
	MsgDarkLiquidity = "event_exchange_dark_liquidity"

	// Account updates.
	MsgPMM           = "pmm"
	MsgBetslip       = "betslip"
	MsgBetslipClosed = "betslip_closed"
	MsgOrder         = "order"
	MsgBet           = "bet"
	MsgBalance       = "balance"
	MsgXRate         = "xrate"

	// Control.
	MsgResponse = "response"
	MsgInfo     = "info"
)

// Stream command names.
const (
	CmdRegisterEvent        = "register_event"
	CmdUnregisterEvent      = "unregister_event"
	CmdListRegisteredEvents = "list_registered_events"
	CmdEcho                 = "echo"
)

// Message is one entry from a frame's data array: a type tag and its payload.
type Message struct {
	// Type is the leading tag, e.g. "offer".
	Type string
	// Data is the payload, or nil for tags sent without one.
	Data json.RawMessage
}

// Decode unmarshals the message payload into v.
func (m Message) Decode(v any) error {
	if len(m.Data) == 0 {
		return fmt.Errorf("%s message has no payload", m.Type)
	}
	if err := json.Unmarshal(m.Data, v); err != nil {
		return fmt.Errorf("decode %s payload: %w", m.Type, err)
	}
	return nil
}

// Frame is one WebSocket message: a batch envelope carrying one or more
// messages.
//
// Batching boundaries are not semantically meaningful — iterate Messages and
// dispatch on each entry's Type. Never rely on ordering, grouping, or a type
// appearing exactly once per frame.
type Frame struct {
	// TS is the server's write timestamp, Unix seconds with microseconds.
	TS       float64
	Messages []Message
}

// StreamEvent is an event on the feed. Normal (match) events carry Home and
// Away; multirunner (outright) events carry Teams and EndTime instead.
type StreamEvent struct {
	EventType          string     `json:"event_type"`
	Sport              string     `json:"sport"`
	EventID            string     `json:"event_id"`
	CompetitionID      int        `json:"competition_id"`
	CompetitionName    string     `json:"competition_name"`
	CompetitionCountry string     `json:"competition_country"`
	Home               string     `json:"home"`
	Away               string     `json:"away"`
	EventName          string     `json:"event_name"`
	IRStatus           string     `json:"ir_status"`
	StartTime          time.Time  `json:"start_time"`
	EndTime            *time.Time `json:"end_time"`
	Teams              []Runner   `json:"teams"`
}

// Offer is the stake available at every price for one
// (sport, event_id, bet_type) triple.
//
// The bet_type string fully identifies the market side, so for and against on
// the same selection arrive as two separate offers.
type Offer struct {
	Sport      string `json:"sport"`
	EventID    string `json:"event_id"`
	BetType    string `json:"bet_type"`
	MarketType string `json:"market_type"`
	InRunning  bool   `json:"in_running"`
	// PriceList is ordered by decimal price descending, one entry per price.
	PriceList []PriceLevel `json:"price_list"`
}

// OfferKey identifies an offer.
type OfferKey struct {
	Sport   string `json:"sport"`
	EventID string `json:"event_id"`
	BetType string `json:"bet_type"`
}

// SyncPayload marks the end of the initial snapshot.
type SyncPayload struct {
	// SessionID identifies this stream, useful when correlating with REST
	// errors or contacting support.
	SessionID string `json:"session_id"`
}

// StreamResponse is the reply to a command, or an in-band error.
type StreamResponse struct {
	Status string          `json:"status"`
	Code   string          `json:"code"`
	Data   json.RawMessage `json:"data"`
}

// IsError reports whether the response carries an error.
func (r StreamResponse) IsError() bool { return r.Status == "error" }

// RegisteredEvents unpacks the reply to list_registered_events into
// (sport, event_id) pairs.
func (r StreamResponse) RegisteredEvents() ([][2]string, error) {
	var d struct {
		RegisteredEvents [][2]string `json:"registered_events"`
	}
	if err := json.Unmarshal(r.Data, &d); err != nil {
		return nil, fmt.Errorf("decode registered_events: %w", err)
	}
	return d.RegisteredEvents, nil
}

// BalanceUpdate is a balance change, in the account's native currency.
type BalanceUpdate struct {
	Balance   Stake `json:"balance"`
	OpenStake Stake `json:"open_stake"`
}

// InfoPayload is periodic feed status. The server sends it every few seconds,
// so an idle connection still receives regular traffic.
type InfoPayload struct {
	RegisteredEvents int `json:"registered_events"`
}

// BetslipClosed signals that a betslip expired or was closed; no further quotes
// will arrive for it.
type BetslipClosed struct {
	BetslipID   string `json:"betslip_id"`
	CloseReason string `json:"close_reason"`
}

// MatchClock is the in-play clock, sent as [period, minutes] or null.
type MatchClock struct {
	// Period is the football period: "1h", "2h" or "ht".
	Period  string
	Minutes int
	// Present is false when no clock is available.
	Present bool
}

func (c *MatchClock) UnmarshalJSON(b []byte) error {
	if string(b) == "null" {
		return nil
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return fmt.Errorf("clock: %w", err)
	}
	if len(raw) < 2 {
		return nil
	}
	if err := json.Unmarshal(raw[0], &c.Period); err != nil {
		return fmt.Errorf("clock period: %w", err)
	}
	if err := json.Unmarshal(raw[1], &c.Minutes); err != nil {
		return fmt.Errorf("clock minutes: %w", err)
	}
	c.Present = true
	return nil
}

// EventTime is an in-play clock update.
type EventTime struct {
	Sport   string     `json:"sport"`
	EventID string     `json:"event_id"`
	Time    MatchClock `json:"time"`
}

// EventScore is an in-play score update. The event_red_cards payload reuses the
// same shape, with Score holding red-card counts.
type EventScore struct {
	Sport   string `json:"sport"`
	EventID string `json:"event_id"`
	// Score is [home, away].
	Score []int `json:"score"`
}

// Stream is a connection to the price feed.
//
// One Stream wraps one WebSocket connection. Reads are not safe for concurrent
// use; writes are serialised internally.
type Stream struct {
	conn *websocket.Conn

	// writeMu serialises writes, since gorilla permits only one concurrent
	// writer.
	writeMu sync.Mutex

	// SessionID is filled in once the sync message arrives.
	SessionID string
}

// DialOptions carries the extras Client.DialStream threads through to dial,
// beyond the credential and resolver every entry point needs. The exported
// Dial/DialWithCredential entry points don't take these — they're for
// standalone use and have no basic-auth wall or trace sink to plumb through.
type DialOptions struct {
	// BasicAuth is an additional base64 "user:pass" token sent as
	// Authorization: Basic on the handshake request, mirroring
	// [WithBasicAuth] on REST — see that doc comment. Empty sends nothing.
	BasicAuth string
	// Trace, when non-nil, is called with a one-line summary of each step of
	// the handshake. Used by the --verbose flag.
	Trace func(format string, args ...any)
}

// Dial opens the price feed with a static API key.
//
// The key is sent both as the documented `api_key` query parameter and as
// X-Api-Key, the same header REST uses. A missing or invalid credential fails
// the upgrade rather than producing an in-band error. Verify it with a REST
// call first — see Client.VerifyKey.
func Dial(ctx context.Context, wsURL, apiKey, lang string) (*Stream, error) {
	return dial(ctx, wsURL, Credential{APIKey: apiKey}, nil, lang, DialOptions{})
}

// DialStream opens the price feed using this client's credential — the same
// [MeResolver] REST uses for a Bearer credential, so REST and the stream
// resolve to the same magic-metadata-jwt/session pair. It also carries this
// client's basic-auth wall credential (see [WithBasicAuth]) and --verbose
// trace sink through to the handshake, same as REST gets on every request.
func (c *Client) DialStream(ctx context.Context, wsURL, lang string) (*Stream, error) {
	return dial(ctx, wsURL, c.cred, c.meResolver, lang, DialOptions{BasicAuth: c.basicAuth, Trace: c.Trace})
}

// DialWithCredential opens the price feed. An API key is sent as X-Api-Key
// and as `api_key` on the query string (the documented handshake). A Bearer
// token is resolved via resolver to the token/jwt query parameters the
// stream actually requires — see [Credential.ApplyTo]; resolver must be
// non-nil whenever cred carries a Bearer token.
func DialWithCredential(ctx context.Context, wsURL string, cred Credential, resolver *MeResolver, lang string) (*Stream, error) {
	return dial(ctx, wsURL, cred, resolver, lang, DialOptions{})
}

func dial(ctx context.Context, wsURL string, cred Credential, resolver *MeResolver, lang string, opts DialOptions) (*Stream, error) {
	trace := opts.Trace
	if trace == nil {
		trace = func(string, ...any) {}
	}

	u, err := url.Parse(wsURL)
	if err != nil {
		return nil, fmt.Errorf("parse stream URL %q: %w", wsURL, err)
	}
	q := u.Query()
	header := make(http.Header)
	switch {
	case strings.TrimSpace(cred.APIKey) != "":
		key := strings.TrimSpace(cred.APIKey)
		q.Set("api_key", key)
		header.Set(APIKeyHeader, key)
		trace("stream dial: credential=api_key ...%s", redactTail(key))
	case strings.TrimSpace(cred.Bearer) != "":
		trace("stream dial: credential=bearer ...%s, resolving via MeResolver", redactTail(cred.Bearer))
		if resolver == nil {
			return nil, fmt.Errorf("magicmarkets: OAuth credential set but no token resolver configured")
		}
		me, err := resolver.Resolve(ctx, cred.Bearer)
		if err != nil {
			trace("stream dial: MeResolver.Resolve failed: %v", err)
			return nil, err
		}
		q.Set("token", me.Session)
		q.Set("jwt", me.JWT)
		trace("stream dial: resolved token=%s jwt=...%s", me.Session, redactTail(me.JWT))
	default:
		trace("stream dial: no credential set")
	}
	if lang != "" {
		q.Set("lang", lang)
	}
	u.RawQuery = q.Encode()

	if basicAuth := strings.TrimSpace(opts.BasicAuth); basicAuth != "" {
		header.Set(AuthorizationHeader, "Basic "+basicAuth)
		trace("stream dial: basic auth wall header attached (...%s)", redactTail(basicAuth))
	} else {
		trace("stream dial: no basic auth wall credential configured")
	}

	trace("stream dial: connecting to %s%s (query keys: %s)", u.Host, u.Path, strings.Join(sortedKeys(q), ","))

	dialer := websocket.Dialer{
		HandshakeTimeout: 20 * time.Second,
		Proxy:            http.ProxyFromEnvironment,
	}

	conn, resp, err := dialer.DialContext(ctx, u.String(), header)
	if err != nil {
		if resp != nil {
			trace("stream dial: handshake failed, HTTP %d, WWW-Authenticate=%q", resp.StatusCode, resp.Header.Get("Www-Authenticate"))
			return nil, fmt.Errorf("stream handshake failed (HTTP %d): %w — check the credentials", resp.StatusCode, err)
		}
		trace("stream dial: handshake failed before a response: %v", err)
		return nil, fmt.Errorf("stream handshake failed: %w", err)
	}
	trace("stream dial: handshake ok, HTTP %d", resp.StatusCode)
	return &Stream{conn: conn}, nil
}

// sortedKeys returns q's parameter names, sorted, so a trace line lists what
// was sent without leaking secret values.
func sortedKeys(q url.Values) []string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// ReadFrame reads and parses the next batch envelope.
func (s *Stream) ReadFrame() (*Frame, error) {
	_, raw, err := s.conn.ReadMessage()
	if err != nil {
		return nil, fmt.Errorf("stream read: %w", err)
	}

	var env struct {
		TS   float64           `json:"ts"`
		Data []json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("decode frame: %w", err)
	}

	frame := &Frame{TS: env.TS, Messages: make([]Message, 0, len(env.Data))}
	for _, entry := range env.Data {
		msg, err := parseMessage(entry)
		if err != nil {
			// A malformed entry should not kill the connection.
			continue
		}
		if msg.Type == MsgSync && s.SessionID == "" {
			var sync SyncPayload
			if msg.Decode(&sync) == nil {
				s.SessionID = sync.SessionID
			}
		}
		frame.Messages = append(frame.Messages, msg)
	}
	return frame, nil
}

// parseMessage unpacks a ["type", payload] array entry.
func parseMessage(entry json.RawMessage) (Message, error) {
	var parts []json.RawMessage
	if err := json.Unmarshal(entry, &parts); err != nil {
		return Message{}, fmt.Errorf("message is not an array: %w", err)
	}
	if len(parts) == 0 {
		return Message{}, fmt.Errorf("empty message array")
	}
	var typ string
	if err := json.Unmarshal(parts[0], &typ); err != nil {
		return Message{}, fmt.Errorf("message type is not a string: %w", err)
	}
	msg := Message{Type: typ}
	if len(parts) > 1 {
		msg.Data = parts[1]
	}
	return msg, nil
}

// Send writes a raw command array.
func (s *Stream) Send(cmd ...any) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	if err := s.conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
		return fmt.Errorf("set write deadline: %w", err)
	}
	if err := s.conn.WriteJSON(cmd); err != nil {
		return fmt.Errorf("stream write: %w", err)
	}
	return nil
}

// RegisterEvent subscribes to offers on an event.
//
// The server replies with one offer per active bet type (the snapshot) followed
// by an ok response. Registering an event with no prices succeeds with an empty
// snapshot.
func (s *Stream) RegisterEvent(sport, eventID string) error {
	return s.Send(CmdRegisterEvent, sport, eventID)
}

// UnregisterEvent stops offer updates for an event. Unregistering something
// that was never registered is not an error.
func (s *Stream) UnregisterEvent(sport, eventID string) error {
	return s.Send(CmdUnregisterEvent, sport, eventID)
}

// ListRegisteredEvents asks for the events this session is registered for. The
// answer arrives as a response message.
func (s *Stream) ListRegisteredEvents() error {
	return s.Send(CmdListRegisteredEvents)
}

// Echo sends a keepalive. The payload is echoed back verbatim.
func (s *Stream) Echo(payload any) error {
	return s.Send(CmdEcho, payload)
}

// Close shuts the connection down, sending a close frame first.
func (s *Stream) Close() error {
	s.writeMu.Lock()
	_ = s.conn.WriteControl(
		websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""),
		time.Now().Add(time.Second),
	)
	s.writeMu.Unlock()
	return s.conn.Close()
}

// SetReadDeadline bounds how long ReadFrame will block.
func (s *Stream) SetReadDeadline(t time.Time) error {
	return s.conn.SetReadDeadline(t)
}

// Snapshot drains the initial event dump and returns the currently-priced
// events, stopping at the sync marker.
//
// The snapshot is not the full fixture list — it holds only events that
// currently have live prices. It may span several frames.
func (s *Stream) Snapshot(ctx context.Context, timeout time.Duration) ([]StreamEvent, error) {
	if err := s.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return nil, err
	}
	defer func() { _ = s.SetReadDeadline(time.Time{}) }()

	var events []StreamEvent
	for {
		if err := ctx.Err(); err != nil {
			return events, err
		}
		frame, err := s.ReadFrame()
		if err != nil {
			return events, err
		}
		for _, msg := range frame.Messages {
			switch msg.Type {
			case MsgEvent:
				var ev StreamEvent
				if err := msg.Decode(&ev); err == nil {
					events = append(events, ev)
				}
			case MsgClearEvents:
				// The feed lost its upstream: discard and wait for the
				// fresh snapshot that follows.
				events = events[:0]
			case MsgSync:
				return events, nil
			}
		}
	}
}

// CollectOffers registers an event and gathers its offer snapshot, returning
// once the acknowledging response arrives.
func (s *Stream) CollectOffers(ctx context.Context, sport, eventID string, timeout time.Duration) ([]Offer, error) {
	if err := s.RegisterEvent(sport, eventID); err != nil {
		return nil, err
	}
	if err := s.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return nil, err
	}
	defer func() { _ = s.SetReadDeadline(time.Time{}) }()

	var offers []Offer
	for {
		if err := ctx.Err(); err != nil {
			return offers, err
		}
		frame, err := s.ReadFrame()
		if err != nil {
			return offers, err
		}
		for _, msg := range frame.Messages {
			switch msg.Type {
			case MsgOffer:
				var o Offer
				if err := msg.Decode(&o); err == nil && o.EventID == eventID {
					offers = append(offers, o)
				}
			case MsgResponse:
				var r StreamResponse
				if err := msg.Decode(&r); err != nil {
					continue
				}
				if r.IsError() {
					return offers, fmt.Errorf("register_event %s %s: %s", sport, eventID, r.Code)
				}
				// The ok response closes the snapshot.
				return offers, nil
			}
		}
	}
}
