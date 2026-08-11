package attestproxy

import (
	"context"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	commonconfig "github.com/0glabs/0g-serving-broker/common/config"
	"github.com/0glabs/0g-serving-broker/common/log"
)

func testLogger(t *testing.T) log.Logger {
	t.Helper()
	l, err := log.GetLogger(&commonconfig.LoggerConfig{Format: "text", Level: "error"})
	if err != nil {
		t.Fatalf("building logger: %v", err)
	}
	return l
}

// fakeDstack stands in for the guest agent, recording which methods actually reached it.
func fakeDstack(t *testing.T, dir string, reached *[]string) string {
	t.Helper()

	path := filepath.Join(dir, "dstack.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listening on the fake dstack socket: %v", err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*reached = append(*reached, r.URL.Path)
		_, _ = w.Write([]byte(`{"served":"` + r.URL.Path + `"}`))
	})}
	go func() { _ = srv.Serve(listener) }()
	t.Cleanup(func() { _ = srv.Close() })

	return path
}

// start runs a proxy and returns a client that dials it.
func start(t *testing.T, reached *[]string) *http.Client {
	t.Helper()

	dir := t.TempDir()
	dstackPath := fakeDstack(t, dir, reached)
	listenPath := filepath.Join(dir, "tee.sock")

	p := New(listenPath, dstackPath, testLogger(t))
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- p.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-served:
		case <-time.After(5 * time.Second):
			t.Error("Serve did not return after the context was cancelled")
		}
		_ = p.Close()
	})

	// Serve creates the socket asynchronously.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if conn, err := net.Dial("unix", listenPath); err == nil {
			_ = conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the proxy socket never appeared")
		}
		time.Sleep(10 * time.Millisecond)
	}

	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", listenPath)
		},
	}}
}

func post(t *testing.T, c *http.Client, method, path string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, "http://dstack"+path, strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("building the request: %v", err)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

// The whole point of the package: the three read-only methods reach dstack and nothing
// else does.
//
// /EmitEvent is the one that matters. It sits on the same dstack socket as GetQuote and
// extends RTMR3, so a broker that could reach it could append any record it liked about
// itself — which is exactly what giving the broker this socket instead of dstack's is meant
// to prevent. A proxy that forwarded it would be worse than no proxy, because the
// deployment would have taken the mount away believing the problem was solved.
func TestOnlyReadOnlyMethodsReachDstack(t *testing.T) {
	var reached []string
	c := start(t, &reached)

	for _, path := range []string{"/GetQuote", "/Info", "/GetKey"} {
		if code, body := post(t, c, http.MethodPost, path); code != http.StatusOK {
			t.Errorf("POST %s = %d %q, want 200", path, code, body)
		}
	}
	if len(reached) != 3 {
		t.Errorf("dstack saw %v, want all three forwarded", reached)
	}

	before := len(reached)
	refused := []string{
		"/EmitEvent", // writes RTMR3 — the reason this package exists
		"/GetTlsKey", // read-only, but the broker does not use it; the set is a fixed
		"/emitevent", // allowlist, not a denylist, so case tricks gain nothing
		"/GetQuote/", // and neither does a trailing slash
		"/../EmitEvent",
		"/",
		"/unknown",
	}
	for _, path := range refused {
		if code, _ := post(t, c, http.MethodPost, path); code != http.StatusNotFound {
			t.Errorf("POST %s = %d, want 404", path, code)
		}
	}
	if len(reached) != before {
		t.Errorf("dstack saw %v after the refused calls, want none of them through", reached[before:])
	}
}

// dstack's RPC is POST-only, so another verb is not a call this proxy is for. Checked
// because a handler keyed on the path alone would forward a GET that some future dstack
// version answers.
func TestNonPostIsRefused(t *testing.T) {
	var reached []string
	c := start(t, &reached)

	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		if code, _ := post(t, c, method, "/GetQuote"); code != http.StatusNotFound {
			t.Errorf("%s /GetQuote = %d, want 404", method, code)
		}
	}
	if len(reached) != 0 {
		t.Errorf("dstack saw %v, want nothing", reached)
	}
}

// The response has to arrive intact: TdxQuote parses it as dstack's own JSON, so a proxy
// that rewrote or truncated the body would break attestation in a way no allowlist test
// would catch.
func TestResponseIsPassedThrough(t *testing.T) {
	var reached []string
	c := start(t, &reached)

	code, body := post(t, c, http.MethodPost, "/GetQuote")
	if code != http.StatusOK || body != `{"served":"/GetQuote"}` {
		t.Errorf("POST /GetQuote = %d %q, want dstack's own body", code, body)
	}
}

// The socket path lives in a volume that outlives the container, so a previous run's file
// is there after any restart. Without removing it, every start after the first fails.
func TestServeReplacesAStaleSocket(t *testing.T) {
	var reached []string
	dir := t.TempDir()
	dstackPath := fakeDstack(t, dir, &reached)
	listenPath := filepath.Join(dir, "tee.sock")

	// A leftover file where the socket goes.
	stale, err := net.Listen("unix", listenPath)
	if err != nil {
		t.Fatalf("creating the stale socket: %v", err)
	}
	_ = stale.Close() // closing leaves the file behind

	p := New(listenPath, dstackPath, testLogger(t))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	served := make(chan error, 1)
	go func() { served <- p.Serve(ctx) }()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if conn, err := net.Dial("unix", listenPath); err == nil {
			_ = conn.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the proxy never took over the stale socket path")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-served:
		if err != nil {
			t.Errorf("Serve() = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("Serve did not return")
	}
}
