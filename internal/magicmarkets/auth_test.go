package magicmarkets

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCredentialFromHeaders(t *testing.T) {
	tests := []struct {
		name   string
		header http.Header
		want   Credential
		ok     bool
	}{
		{name: "empty", header: http.Header{}, ok: false},
		{
			name:   "api key",
			header: http.Header{APIKeyHeader: []string{"k"}},
			want:   Credential{APIKey: "k"},
			ok:     true,
		},
		{
			name:   "bearer",
			header: http.Header{AuthorizationHeader: []string{"Bearer tok"}},
			want:   Credential{Bearer: "tok"},
			ok:     true,
		},
		{
			name:   "bearer is case-insensitive",
			header: http.Header{AuthorizationHeader: []string{"bearer tok"}},
			want:   Credential{Bearer: "tok"},
			ok:     true,
		},
		{
			name: "api key wins over bearer",
			header: http.Header{
				APIKeyHeader:        []string{"k"},
				AuthorizationHeader: []string{"Bearer tok"},
			},
			want: Credential{APIKey: "k"},
			ok:   true,
		},
		{
			name:   "empty bearer is missing",
			header: http.Header{AuthorizationHeader: []string{"Bearer "}},
			ok:     false,
		},
		{
			name:   "basic is not a bearer token",
			header: http.Header{AuthorizationHeader: []string{"Basic abc"}},
			ok:     false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := CredentialFromHeaders(tt.header)
			if ok != tt.ok {
				t.Fatalf("ok = %v, want %v", ok, tt.ok)
			}
			if got != tt.want {
				t.Errorf("credential = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestCredentialApplyToAPIKey(t *testing.T) {
	h := make(http.Header)
	if err := (Credential{APIKey: "k"}).ApplyTo(context.Background(), h, nil); err != nil {
		t.Fatalf("ApplyTo: %v", err)
	}
	if got := h.Get(APIKeyHeader); got != "k" {
		t.Errorf("X-Api-Key = %q, want k", got)
	}
	if got := h.Get(MagicMetadataJWTHeader); got != "" {
		t.Errorf("magic-metadata-jwt = %q, want empty when using an API key", got)
	}
	if got := h.Get(SessionHeader); got != "" {
		t.Errorf("session = %q, want empty when using an API key", got)
	}
}

func TestCredentialApplyToBearerRequiresResolver(t *testing.T) {
	h := make(http.Header)
	err := (Credential{Bearer: "tok"}).ApplyTo(context.Background(), h, nil)
	if err == nil {
		t.Fatal("expected an error with no resolver configured")
	}
}

func TestCredentialApplyToBearerResolvesViaFirebaseTokenExchange(t *testing.T) {
	token := fakeAccessToken("user-uuid")
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get(AuthorizationHeader); got != "Bearer "+token {
			t.Errorf("firebase-token exchange Authorization = %q, want Bearer %s", got, token)
		}
		if r.URL.Path != "/oauth2/firebase-token" {
			t.Errorf("path = %q, want /oauth2/firebase-token", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"firebase_token": "me-jwt",
		})
	}))
	t.Cleanup(ts.Close)
	identity := stubIdentityToolkit(t, "me-id-token")

	resolver := NewMeResolver(ts.URL, "5", "web-api-key", identityToolkitClient(t, identity.URL))
	h := make(http.Header)
	if err := (Credential{Bearer: token}).ApplyTo(context.Background(), h, resolver); err != nil {
		t.Fatalf("ApplyTo: %v", err)
	}
	if got := h.Get(MagicMetadataJWTHeader); got != "me-id-token" {
		t.Errorf("magic-metadata-jwt = %q, want me-id-token", got)
	}
	if got := h.Get(SessionHeader); got != "m-5-user-uuid" {
		t.Errorf("session = %q, want m-5-user-uuid", got)
	}
	if got := h.Get(APIKeyHeader); got != "" {
		t.Errorf("X-Api-Key = %q, want empty when using a bearer token", got)
	}
}
