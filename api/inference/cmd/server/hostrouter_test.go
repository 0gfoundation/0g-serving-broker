package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func named(name string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(name))
	})
}

func TestHostRouterDispatchesByServingHost(t *testing.T) {
	r := &hostRouter{
		byHost:  map[string]http.Handler{"a.example": named("a"), "b.example": named("b"), "::2": named("b")},
		primary: named("a"),
	}
	for host, want := range map[string]string{
		"b.example":                       "b",
		"B.Example:443":                   "b",
		"b.example.":                      "b",
		"a.example":                       "a",
		"0g-serving-provider-broker:3080": "a", // health check, Prometheus
		"10.0.0.5:3080":                   "a",
		"unknown.example":                 "a",
		"":                                "a",
	} {
		req := httptest.NewRequest(http.MethodGet, "/v1/proxy/models", nil)
		req.Host = host
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if got := w.Body.String(); got != want {
			t.Errorf("Host %q went to %q, want %q", host, got, want)
		}
	}
}
