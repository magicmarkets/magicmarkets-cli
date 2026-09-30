package magicmarkets

import (
	"fmt"
	"net/http"
)

// NoRedirectClient returns an *http.Client that shares base's Transport, Timeout and Jar but
// refuses to follow HTTP redirects, failing loudly instead.
//
// net/http's default redirect policy downgrades a POST to a GET when following a 301, 302 or
// 303 (matching legacy browser behavior). Every Magic Markets/Firebase endpoint this package
// calls answers directly under correct configuration; a redirect here means the configured host
// is wrong — e.g. a stray "www." prefix that 301s to the canonical domain — and letting
// net/http follow it would silently turn the outgoing POST into a GET, surfacing as an unrelated,
// confusing 4xx from the wrong method instead of a clear misconfiguration signal. This is what
// happened in production: a www.-prefixed MAGICMARKETS_OAUTH_ISSUER made the
// /oauth2/firebase-token exchange 301 -> GET -> "405 Method Not Allowed".
//
// base may be nil, in which case http.DefaultClient's settings are used.
func NoRedirectClient(base *http.Client) *http.Client {
	if base == nil {
		base = http.DefaultClient
	}
	return &http.Client{
		Transport: base.Transport,
		Timeout:   base.Timeout,
		Jar:       base.Jar,
		CheckRedirect: func(req *http.Request, _ []*http.Request) error {
			return fmt.Errorf("unexpected redirect to %s (check the configured host for a stray %q prefix or scheme mismatch)",
				req.URL, "www.")
		},
	}
}
