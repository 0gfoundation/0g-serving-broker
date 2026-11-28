package server

import (
	"context"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestAdminSocketTransport(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "broker-admin-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "overlay.sock")
	listener, err := listenAdminOverlay(path)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})}
	t.Cleanup(func() { _ = server.Close() })
	t.Cleanup(func() { _ = listener.Close() })
	go func() { _ = server.Serve(listener) }()

	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}}
	t.Cleanup(transport.CloseIdleConnections)
	client := &http.Client{Transport: transport}
	resp, err := client.Get("http://localhost/v1/admin/overlay")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("Unix HTTP status = %d, want 204", resp.StatusCode)
	}

	shared := filepath.Join(dir, "shared")
	if err := os.Mkdir(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	if listener, err := listenAdminOverlay(filepath.Join(shared, "overlay.sock")); err == nil {
		listener.Close()
		t.Fatal("started admin listener in a shared directory")
	}
}
