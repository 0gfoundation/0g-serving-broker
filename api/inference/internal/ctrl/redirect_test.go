package ctrl

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/0glabs/0g-serving-broker/inference/config"
)

// A redirect to another host would carry the decrypted body (307/308) and the vendor key
// headers outside the recorded upstream set. It must come back as the response instead.
func TestForwardingClientDoesNotFollowCrossOriginRedirects(t *testing.T) {
	var elsewhere atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		elsewhere.Add(1)
	}))
	defer other.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/v1/chat/completions", http.StatusTemporaryRedirect)
	}))
	defer upstream.Close()

	c := &http.Client{CheckRedirect: sameOriginRedirectsOnly}
	req, _ := http.NewRequest(http.MethodPost, upstream.URL+"/v1/chat/completions", strings.NewReader(`{"prompt":"secret"}`))
	req.Header.Set("x-api-key", "vendor-key")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("Do() = %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusTemporaryRedirect {
		t.Errorf("status %d, want the 307 handed back", resp.StatusCode)
	}
	if n := elsewhere.Load(); n != 0 {
		t.Errorf("the other host was reached %d times", n)
	}
}

// A redirect within the same origin stays inside the set and keeps working.
func TestForwardingClientFollowsSameOriginRedirects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/chat/completions" {
			http.Redirect(w, r, "/v1/chat/completions/", http.StatusPermanentRedirect)
			return
		}
		b, _ := io.ReadAll(r.Body)
		_, _ = w.Write(b)
	}))
	defer srv.Close()

	c := &http.Client{CheckRedirect: sameOriginRedirectsOnly}
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/chat/completions", strings.NewReader(`{"a":1}`))
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("Do() = %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(body) != `{"a":1}` {
		t.Errorf("= %d %q, want the same-origin redirect followed with the body", resp.StatusCode, body)
	}
}

// The policy is only worth anything on the client the broker forwards with.
func TestTheForwardingClientCarriesThePolicy(t *testing.T) {
	c := New(nil, nil, &config.Config{}, nil, nil, nil, testLogger())
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("the forwarding client followed a cross-origin redirect")
	}))
	defer other.Close()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	}))
	defer upstream.Close()
	resp, err := c.httpClient.Post(upstream.URL, "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("Post() = %v", err)
	}
	resp.Body.Close()
}
