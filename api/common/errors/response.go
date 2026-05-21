package errors

import (
	"net/http"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// Response writes err to the gin context as a JSON error body. If err (or any
// error in its chain) is an *HTTPError, its status code is used; otherwise the
// default is 400 Bad Request.
//
// Only 500 Internal Server Error is sanitized: its body is the generic status
// text and the full error is logged server-side via logrus. Other 5xx codes
// (503 Service Unavailable, 504 Gateway Timeout, etc.) have client-facing
// semantics where the message is actionable — those are passed through as-is,
// but still logged at Error level so operators see them too.
func Response(ctx *gin.Context, err error) {
	status := http.StatusBadRequest
	var httpErr *HTTPError
	if As(err, &httpErr) {
		status = httpErr.Status()
	}

	msg := err.Error()
	if status >= 500 {
		// FullPath() is the registered route template ("/user/:addr/task/:id")
		// and is empty when no route matched (middleware rejects, tests using
		// gin.CreateTestContext without a router). Fall back to the raw URL
		// path so diagnostics never lose the request target.
		path := ctx.FullPath()
		if path == "" && ctx.Request != nil && ctx.Request.URL != nil {
			path = ctx.Request.URL.Path
		}
		log.WithError(err).WithFields(log.Fields{
			"status": status,
			"path":   path,
			"method": ctx.Request.Method,
		}).Error("handler returned server error")
		if status == http.StatusInternalServerError {
			msg = http.StatusText(status)
		}
	}

	ctx.JSON(status, gin.H{"error": msg})
	ctx.Abort()
}
