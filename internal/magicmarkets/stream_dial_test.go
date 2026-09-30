package magicmarkets

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type streamHandshake struct {
	Header http.Header
	Query  string
}

func startStreamProbe(t *testing.T) (wsURL string, seen chan streamHandshake) {
	t.Helper()
	seen = make(chan streamHandshake, 1)
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- streamHandshake{Header: r.Header.Clone(), Query: r.URL.RawQuery}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		_ = conn.Close()
	}))
	t.Cleanup(ts.Close)
	return "ws" + strings.TrimPrefix(ts.URL, "http"), seen
}

func TestDialSendsAPIKeyQueryAndHeader(t *testing.T) {
	wsURL, seen := startStreamProbe(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	stream, err := Dial(ctx, wsURL, "k", "en")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	_ = stream.Close()

	h := <-seen
	if got := h.Header.Get(APIKeyHeader); got != "k" {
		t.Errorf("X-Api-Key = %q, want k", got)
	}
	if !strings.Contains(h.Query, "api_key=k") {
		t.Errorf("query = %q, want api_key=k", h.Query)
	}
	if !strings.Contains(h.Query, "lang=en") {
		t.Errorf("query = %q, want lang=en", h.Query)
	}
}

func TestDialBearerSendsTokenAndJWTQueryViaMe(t *testing.T) {
	me := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"firebase_token":"me-jwt"}`)
	}))
	t.Cleanup(me.Close)
	identity := stubIdentityToolkit(t, "me-id-token")
	resolver := NewMeResolver(me.URL, "5", "web-api-key", identityToolkitClient(t, identity.URL))

	wsURL, seen := startStreamProbe(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	stream, err := DialWithCredential(ctx, wsURL, Credential{Bearer: fakeAccessToken("user-uuid")}, resolver, "")
	if err != nil {
		t.Fatalf("DialWithCredential: %v", err)
	}
	_ = stream.Close()

	h := <-seen
	if got := h.Header.Get(AuthorizationHeader); got != "" {
		t.Errorf("Authorization = %q, want empty — the raw bearer token must never reach the stream", got)
	}
	if got := h.Header.Get(APIKeyHeader); got != "" {
		t.Errorf("X-Api-Key = %q, want empty", got)
	}
	if !strings.Contains(h.Query, "token=m-5-user-uuid") {
		t.Errorf("query = %q, want token=m-5-user-uuid", h.Query)
	}
	if !strings.Contains(h.Query, "jwt=me-id-token") {
		t.Errorf("query = %q, want jwt=me-id-token", h.Query)
	}
	if strings.Contains(h.Query, "api_key=") {
		t.Errorf("query = %q, Bearer handshake must not send api_key", h.Query)
	}
}

func TestClientDialStreamSendsBasicAuthWallHeader(t *testing.T) {
	wsURL, seen := startStreamProbe(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	c := NewWithCredential("http://unused", Credential{APIKey: "k"}, time.Second, WithBasicAuth("dXNlcjpwYXNz"))
	stream, err := c.DialStream(ctx, wsURL, "")
	if err != nil {
		t.Fatalf("DialStream: %v", err)
	}
	_ = stream.Close()

	h := <-seen
	if got := h.Header.Get(AuthorizationHeader); got != "Basic dXNlcjpwYXNz" {
		t.Errorf("Authorization = %q, want the infra basic-auth wall header — REST gets it via WithBasicAuth, "+
			"the stream handshake must carry it too or a dev/stg environment behind the wall rejects the upgrade", got)
	}
	if got := h.Header.Get(APIKeyHeader); got != "k" {
		t.Errorf("X-Api-Key = %q, want k (both headers must be present at once)", got)
	}
}

func TestDialBearerWithoutResolverFails(t *testing.T) {
	wsURL, _ := startStreamProbe(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if _, err := DialWithCredential(ctx, wsURL, Credential{Bearer: "tok"}, nil, ""); err == nil {
		t.Fatal("expected an error with no token resolver configured")
	}
}
