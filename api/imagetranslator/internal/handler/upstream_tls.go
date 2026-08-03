package handler

import (
	"github.com/gin-gonic/gin"

	teeutil "github.com/0glabs/0g-serving-broker/common/tee"
)

// NewEngine builds the sidecar's gin engine with the upstream-TLS report already
// installed. Every translator entrypoint must use it: registering the middleware
// by hand is the one step a new vendor's cmd/server can silently omit, and the
// symptom — a provider that keeps advertising verifiability while producing no
// routing proofs at all — surfaces only as a per-response broker log line.
//
// Duplicated from videotranslator/internal/handler/upstream_tls.go rather than
// imported — see image.go's package doc comment: api/imagetranslator is a
// structurally separate package tree from api/videotranslator, sharing no code.
func NewEngine() *gin.Engine {
	engine := gin.New()
	engine.Use(gin.Recovery(), UpstreamTLSReport())
	return engine
}

// UpstreamTLSReport reports the vendor TLS certificate this sidecar observed back
// to the broker on tee.HeaderUpstreamCertFingerprint.
//
// Why a middleware and not a line in each handler: the broker's centralized
// routing proof binds the leaf certificate of the connection that reached the real
// upstream, read from resp.TLS. Putting a translation shim in front of the vendor
// moves that handshake in here — the broker's own hop is plaintext HTTP inside the
// CVM — so without this report a translated provider can never be `centralized`
// and loses TEE verification entirely. Doing it once here means no vendor handler
// (present or future) can forget to report and silently downgrade the proof.
//
// CreateImage's internal poll loop (create, repeated GetTask, then FetchImage
// batches) makes several vendor calls per request, all sharing c.Request.Context()
// — CertCapture keeps only the FIRST observation, which is the create call's own
// TLS handshake, exactly the one the routing proof needs to bind.
//
// The header is set lazily on the first write because gin handlers build the body
// (c.JSON / c.Status) after the vendor calls return, and a header set after the
// status line is written is dropped.
func UpstreamTLSReport() gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, capture := teeutil.WithCertCapture(c.Request.Context())
		c.Request = c.Request.WithContext(ctx)
		c.Writer = &upstreamTLSWriter{ResponseWriter: c.Writer, capture: capture}
		c.Next()
	}
}

type upstreamTLSWriter struct {
	gin.ResponseWriter
	capture *teeutil.CertCapture
	// reported is unsynchronized: a gin handler writes its response from the
	// request goroutine, so all the hooks below run on one goroutine. A handler
	// that writes from a goroutine it spawned would need a sync.Once here.
	reported bool
}

func (w *upstreamTLSWriter) report() {
	if w.reported {
		return
	}
	w.reported = true
	// Del first, unconditionally: these headers are the broker's evidence, so a
	// value we did not put there must never survive. No handler copies vendor
	// response headers today, but the whole point of doing this in middleware is
	// that a future one cannot get it wrong.
	w.Header().Del(teeutil.HeaderUpstreamCertFingerprint)
	w.Header().Del(teeutil.HeaderUpstreamCertHost)
	fp := w.capture.Fingerprint()
	if fp == "" {
		return
	}
	w.Header().Set(teeutil.HeaderUpstreamCertFingerprint, fp)
	if host := w.capture.ServerName(); host != "" {
		w.Header().Set(teeutil.HeaderUpstreamCertHost, host)
	}
}

func (w *upstreamTLSWriter) WriteHeader(code int) {
	w.report()
	w.ResponseWriter.WriteHeader(code)
}

// WriteHeaderNow is gin's flush point and can be reached without any Write at all
// (a body-less render, or c.Abort mid-handler), so hook it too. Not airtight: gin's
// engine can flush c.writermem directly, bypassing this wrapper entirely. The
// current handler always writes a JSON body, so that path is unreachable here.
func (w *upstreamTLSWriter) WriteHeaderNow() {
	w.report()
	w.ResponseWriter.WriteHeaderNow()
}

// Flush would otherwise reach the EMBEDDED writer's WriteHeaderNow, not the
// override above — a handler that flushed before writing would send headers with
// no report. No sidecar handler streams today, so this closes the gap for a
// streaming handler someone adds later rather than a live one.
func (w *upstreamTLSWriter) Flush() {
	w.report()
	w.ResponseWriter.Flush()
}

func (w *upstreamTLSWriter) Write(b []byte) (int, error) {
	w.report()
	return w.ResponseWriter.Write(b)
}

func (w *upstreamTLSWriter) WriteString(s string) (int, error) {
	w.report()
	return w.ResponseWriter.WriteString(s)
}
