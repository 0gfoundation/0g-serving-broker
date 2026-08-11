// Package attestproxy lets the broker obtain quotes and derived keys without holding
// the dstack socket itself.
//
// dstack serves EmitEvent on /var/run/dstack.sock from the same unauthenticated handler
// as GetQuote, and binds that socket 0777. So every container that mounts it can append
// to RTMR3 — which makes the ledger worthless for describing a container that mounts it,
// because the thing being described could have written the description. The broker mounts
// it today only because that is where GetQuote and DeriveKey live.
//
// This serves those, and only those, on a second socket. A deployment gives the broker
// that socket instead of dstack's, and the ledger becomes something only the controller
// can write — the controller being the one container whose image compose_hash pins and
// which cannot upgrade itself.
//
// The proxy is not what provides the property. Removing the mount is. A modified broker
// image does not run our code, so nothing written here constrains it; what this does is
// make the removal possible, by giving an honest broker somewhere else to ask. And a
// reader still cannot check the mount is gone — that is settled by the caller pinning
// compose_hash to a compose it reviewed, which is where the socket assignment is written.
package attestproxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/0glabs/0g-serving-broker/common/log"
)

// forwarded is every dstack method this proxy will pass on.
//
// Adding an entry is a security decision, not a convenience one. All three are read-only
// with respect to RTMR3: a quote is taken, a key is derived, TCB info is reported, and
// none of them extends a measurement register. The reason this package exists is that
// /EmitEvent sits on the same socket and does extend one, so it must never appear here —
// nor must anything else added later without the same argument.
//
// Deliberately a fixed set rather than a prefix or a denylist: a denylist is wrong by
// default the moment dstack adds a method.
var forwarded = map[string]bool{
	"/GetQuote": true, // the attestation itself
	"/Info":     true, // tcb_info, which TdxQuote assembles into its response
	"/GetKey":   true, // the signer and enclave encryption keys
}

// imageDigestPattern is what counts as a running-image digest. Lowercase hex only, the same
// shape the upgrade entry point accepts.
var imageDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// Proxy forwards the allowlisted dstack methods from one unix socket to another, and answers
// the signing operations itself.
type Proxy struct {
	listenPath     string
	dstackPath     string
	currentImageFn CurrentImageFunc
	server         *http.Server
	listener       net.Listener
	logger         log.Logger
}

// New prepares a proxy that listens on listenPath and forwards to dstackPath.
//
// Nothing is dialled or created yet; Serve does that.
func New(listenPath, dstackPath string, currentImage CurrentImageFunc, logger log.Logger) *Proxy {
	// A fixed host: the transport below ignores it and dials the socket, but net/http
	// still needs a syntactically valid URL to build requests against.
	target, _ := url.Parse("http://dstack")

	reverse := httputil.NewSingleHostReverseProxy(target)
	reverse.Transport = &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", dstackPath)
		},
	}
	reverse.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		logger.Errorf("[attestproxy] forwarding %s to %s: %v", r.URL.Path, dstackPath, err)
		w.WriteHeader(http.StatusBadGateway)
	}

	p := &Proxy{listenPath: listenPath, dstackPath: dstackPath, currentImageFn: currentImage, logger: logger}
	p.server = &http.Server{
		Handler:           p.guard(reverse),
		ReadHeaderTimeout: 10 * time.Second,
	}
	return p
}

// guard refuses everything outside the allowlist before the request can reach dstack.
func (p *Proxy) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// dstack's RPC is POST-only, so anything else is not a call this proxy is for.
		if r.Method == http.MethodPost && p.handleLocal(w, r) {
			return
		}
		if r.Method != http.MethodPost || !forwarded[r.URL.Path] {
			// Logged at warning: on a correctly deployed CVM nothing should ever ask.
			// A request for /EmitEvent in particular means something is trying to write
			// the ledger through the one path built to keep it from doing that.
			p.logger.Warnf("[attestproxy] refused %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Serve creates the socket and serves until the context is cancelled.
//
// A stale socket file is removed first: the path lives in a volume that outlives the
// container, so a previous run's file is there after any restart and would otherwise make
// every start after the first fail on "address already in use".
func (p *Proxy) Serve(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Dir(p.listenPath), 0o755); err != nil {
		return fmt.Errorf("creating the directory for %s: %w", p.listenPath, err)
	}
	if err := os.Remove(p.listenPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("removing the stale socket %s: %w", p.listenPath, err)
	}

	listener, err := net.Listen("unix", p.listenPath)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", p.listenPath, err)
	}
	p.listener = listener

	// Reachable by whoever mounts the volume, which is the boundary that matters and the
	// one written in the compose file. dstack does the same with its own socket; a mode
	// tied to a uid would only add a way for the broker to fail to connect.
	if err := os.Chmod(p.listenPath, 0o666); err != nil {
		return fmt.Errorf("setting the mode on %s: %w", p.listenPath, err)
	}

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = p.server.Shutdown(shutdownCtx)
	}()

	p.logger.Infof("[attestproxy] serving %v on %s", forwardedPaths(), p.listenPath)
	if err := p.server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// Close stops serving and removes the socket file.
func (p *Proxy) Close() error {
	if p.listener == nil {
		return nil
	}
	err := p.listener.Close()
	if rmErr := os.Remove(p.listenPath); rmErr != nil && !errors.Is(rmErr, os.ErrNotExist) && err == nil {
		err = rmErr
	}
	return err
}

func forwardedPaths() []string {
	paths := make([]string, 0, len(forwarded))
	for p := range forwarded {
		paths = append(paths, p)
	}
	return paths
}
