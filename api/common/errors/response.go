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
// For 5xx responses the underlying error text is NOT sent to the client —
// internal errors can contain driver messages, filesystem paths, schema
// details, etc., which must not leak to unauthenticated callers. The full
// error is logged server-side (via logrus, the project's standard logger)
// and the client receives a generic status-text body.
func Response(ctx *gin.Context, err error) {
	status := http.StatusBadRequest
	var httpErr *HTTPError
	if As(err, &httpErr) {
		status = httpErr.Status()
	}

	msg := err.Error()
	if status >= 500 {
		log.WithError(err).WithFields(log.Fields{
			"status": status,
			"path":   ctx.FullPath(),
			"method": ctx.Request.Method,
		}).Error("handler returned server error")
		msg = http.StatusText(status)
	}

	ctx.JSON(status, gin.H{"error": msg})
	ctx.Abort()
}
