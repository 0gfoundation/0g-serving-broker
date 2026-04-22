package errors

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// Response writes err to the gin context as a JSON error body. If err (or any
// error in its chain) is an *HTTPError, its status code is used; otherwise the
// default is 400 Bad Request.
func Response(ctx *gin.Context, err error) {
	status := http.StatusBadRequest
	var httpErr *HTTPError
	if As(err, &httpErr) {
		status = httpErr.Status()
	}
	ctx.JSON(status, gin.H{"error": err.Error()})
	ctx.Abort()
}
