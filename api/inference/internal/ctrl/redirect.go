package ctrl

import (
	"errors"
	"net/http"
)

// sameOriginRedirectsOnly is the forwarding client's redirect policy: follow a redirect only
// to the origin (scheme and host:port) the request was first sent to, and hand any other one
// back to the caller as the response it is.
//
// Following is not neutral. On a 307/308 Go replays the request body — the decrypted prompt —
// to the new location, and on every redirect it re-sends the request's headers, dropping only
// Authorization and Cookie when the host changes: a vendor key set through additionalSecret
// (x-api-key and the like) goes along. The upstream set a TEE deployment records bounds where
// plaintext may go by host, so a redirect to another host takes both outside it, unrecorded.
// A same-origin redirect (a trailing slash, a versioned path) stays where it was going.
//
// The 3xx is then answered as a 502 with a fixed body (handleServiceError), so neither its
// Location (also stripped by isUpstreamLeakHeader) nor a "moved to" page reveals the host.
func sameOriginRedirectsOnly(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	first := via[0].URL
	if req.URL.Scheme != first.Scheme || req.URL.Host != first.Host {
		return http.ErrUseLastResponse
	}
	return nil
}
