package middleware

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

const MaxRequestSize = 32 << 20 // 32MB

func RequestSizeLimitMiddleware(maxSize int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxSize)
		c.Next()
	}
}