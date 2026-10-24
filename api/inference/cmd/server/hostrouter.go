package server

import (
	"net"
	"net/http"
	"strings"
)

// hostRouter sends each request to the provider instance whose servingUrl host
// it arrived for, and everything else — no Host match, an IP, the compose
// service name the health check and Prometheus use — to the primary instance,
// so a process serving one instance behaves exactly as it did before.
//
// Sending an unmatched request to the primary is safe for billing: a session
// token names the provider it was issued for and ValidateSession refuses one
// for another address, so a request that reaches the wrong instance is
// rejected, not charged to it.
type hostRouter struct {
	byHost  map[string]http.Handler
	primary http.Handler
}

func (r *hostRouter) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if h, ok := r.byHost[requestHost(req.Host)]; ok {
		h.ServeHTTP(w, req)
		return
	}
	r.primary.ServeHTTP(w, req)
}

// requestHost is the lower-cased host of a Host header, without its port.
func requestHost(hostport string) string {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	return strings.ToLower(strings.TrimSuffix(host, "."))
}
