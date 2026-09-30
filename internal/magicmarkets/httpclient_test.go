package magicmarkets

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestNoRedirectClientRefusesRedirect is the regression test for the incident where a
// www.-prefixed MAGICMARKETS_OAUTH_ISSUER 301-redirected the firebase-token exchange's POST to
// the canonical bare domain; net/http's default client silently re-sent it as a GET (its
// standard behavior on a 301/302/303), and the real endpoint rejected the GET with a confusing
// "405 Method Not Allowed" that gave no hint the request had been redirected at all.
// NoRedirectClient must refuse to follow the redirect and say so plainly instead.
func TestNoRedirectClientRefusesRedirect(t *testing.T) {
	var targetHits int
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetHits++
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(target.Close)

	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusMovedPermanently)
	}))
	t.Cleanup(redirector.Close)

	req, err := http.NewRequest(http.MethodPost, redirector.URL, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	_, err = NoRedirectClient(nil).Do(req)
	if err == nil {
		t.Fatal("expected an error instead of silently following the redirect")
	}
	if !strings.Contains(err.Error(), "unexpected redirect") {
		t.Errorf("error = %q, want it to mention the unexpected redirect", err.Error())
	}
	if targetHits != 0 {
		t.Errorf("redirect target was hit %d times, want 0 — the client must not follow the redirect at all", targetHits)
	}
}
