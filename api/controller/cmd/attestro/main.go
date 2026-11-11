// Package attestro is the 0g-attest-ro applet: the ingress's read-only attestation socket,
// on its own, for a deployment without the controller.
//
// dstack derives keys per app, not per container, so any container holding
// /var/run/dstack.sock can call GetKey on the broker's paths and get the broker's keys. The
// ingress needs that socket for two reads only (Info, GetQuote) and sits on the request
// path, so holding it there means a compromised ingress can unseal what passes through it.
// The controller used to serve it a socket that forwards those two reads and nothing else
// (attestproxy.NewReadOnly); without the controller, this applet is that socket's only
// server. The compose gives this container dstack.sock and the ingress the socket volume —
// the property comes from the ingress no longer mounting dstack.sock, as in attestproxy.
package attestro

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	commonconfig "github.com/0glabs/0g-serving-broker/common/config"
	"github.com/0glabs/0g-serving-broker/common/log"
	"github.com/0glabs/0g-serving-broker/common/tee"
	"github.com/0glabs/0g-serving-broker/controller/internal/attestproxy"
)

func Main() {
	logger, err := log.GetLogger(&commonconfig.LoggerConfig{Format: "text", Level: "info"})
	if err != nil {
		panic(err)
	}
	socket := os.Getenv(attestproxy.ReadOnlySocketEnvVar)
	if socket == "" {
		logger.Fatalf("%s is not set; nothing to serve", attestproxy.ReadOnlySocketEnvVar)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	proxy := attestproxy.NewReadOnly(socket, tee.DefaultDstackSocket, logger)
	defer func() { _ = proxy.Close() }()
	if err := proxy.Serve(ctx); err != nil {
		logger.Fatalf("Read-only attestation proxy on %s failed: %v", socket, err)
	}
}
