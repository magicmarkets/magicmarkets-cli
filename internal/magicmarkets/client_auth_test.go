package magicmarkets

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestClientSendsAPIKeyHeader(t *testing.T) {
	var gotKey, gotAuth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get(APIKeyHeader)
		gotAuth = r.Header.Get(AuthorizationHeader)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":"ok","data":[]}`)
	}))
	t.Cleanup(ts.Close)

	c := New(ts.URL, "caller-key", time.Second)
	if err := c.VerifyKey(context.Background()); err != nil {
		t.Fatalf("VerifyKey: %v", err)
	}
	if gotKey != "caller-key" {
		t.Errorf("X-Api-Key = %q, want caller-key", gotKey)
	}
	if gotAuth != "" {
		t.Errorf("Authorization = %q, want empty", gotAuth)
	}
}

func TestClientSendsMagicMetadataJWTAndSessionForBearer(t *testing.T) {
	firebaseExchange := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"firebase_token":"me-jwt"}`)
	}))
	t.Cleanup(firebaseExchange.Close)

	identity := stubIdentityToolkit(t, "me-id-token")

	var gotKey, gotJWT, gotSession string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get(APIKeyHeader)
		gotJWT = r.Header.Get(MagicMetadataJWTHeader)
		gotSession = r.Header.Get(SessionHeader)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":"ok","data":[]}`)
	}))
	t.Cleanup(ts.Close)

	resolver := NewMeResolver(firebaseExchange.URL, "5", "web-api-key", identityToolkitClient(t, identity.URL))
	c := NewWithCredential(ts.URL, Credential{Bearer: fakeAccessToken("user-uuid")}, time.Second, WithMeResolver(resolver))
	if err := c.VerifyKey(context.Background()); err != nil {
		t.Fatalf("VerifyKey: %v", err)
	}
	if gotJWT != "me-id-token" {
		t.Errorf("magic-metadata-jwt = %q, want me-id-token", gotJWT)
	}
	if gotSession != "m-5-user-uuid" {
		t.Errorf("session = %q, want m-5-user-uuid", gotSession)
	}
	if gotKey != "" {
		t.Errorf("X-Api-Key = %q, want empty", gotKey)
	}
}

func TestClientBearerWithoutResolverFails(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":"ok","data":[]}`)
	}))
	t.Cleanup(ts.Close)

	c := NewWithCredential(ts.URL, Credential{Bearer: "oauth-tok"}, time.Second)
	if err := c.VerifyKey(context.Background()); err == nil {
		t.Fatal("expected an error with no /me resolver configured")
	}
}

func TestClientAPIKeyWinsOverBearerOnSameCredential(t *testing.T) {
	var gotKey, gotAuth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get(APIKeyHeader)
		gotAuth = r.Header.Get(AuthorizationHeader)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"status":"ok","data":[]}`)
	}))
	t.Cleanup(ts.Close)

	c := NewWithCredential(ts.URL, Credential{APIKey: "k", Bearer: "tok"}, time.Second)
	if err := c.VerifyKey(context.Background()); err != nil {
		t.Fatalf("VerifyKey: %v", err)
	}
	if gotKey != "k" {
		t.Errorf("X-Api-Key = %q, want k", gotKey)
	}
	if gotAuth != "" {
		t.Errorf("Authorization = %q, want empty when an API key is set", gotAuth)
	}
}

func TestClientRejectsNonJSONAuthError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, "nope")
	}))
	t.Cleanup(ts.Close)

	c := NewWithCredential(ts.URL, Credential{Bearer: "bad"}, time.Second)
	if err := c.VerifyKey(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}
